package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"

	"github.com/gugumanager/gugumanager/internal/agentca"
	"github.com/gugumanager/gugumanager/internal/agentrpc"
	"github.com/gugumanager/gugumanager/internal/config"
	"github.com/gugumanager/gugumanager/internal/httpapi"
	"github.com/gugumanager/gugumanager/internal/migrations"
	"github.com/gugumanager/gugumanager/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	logger = newLogger(os.Stdout, cfg.LogLevel, cfg.LogFormat)

	service, err := buildService(context.Background(), cfg, logger)
	if err != nil {
		logger.Error("initialize service", "environment", cfg.Environment, "error", err)
		os.Exit(1)
	}
	readiness, err := buildDependencyReadiness(context.Background(), cfg, service)
	if err != nil {
		logger.Error("initialize dependency readiness", "error", err)
		os.Exit(1)
	}
	if readiness != nil {
		defer readiness.Close()
	}
	defer func() {
		if closer, ok := service.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()

	// 节点存活探测对实现了 ReconcileNodeLiveness 的适配器（Memory 与
	// Postgres）每 5 秒对账一次：超过 30 秒无心跳的节点标记 offline，
	// 并禁止继续向该节点下发任务。
	livenessCtx, stopLiveness := context.WithCancel(context.Background())
	defer stopLiveness()
	if reconciler, ok := service.(interface{ ReconcileNodeLiveness(time.Time) }); ok {
		go reconcileNodeLiveness(livenessCtx, reconciler)
	} else {
		logger.Info("node liveness reconciliation disabled", "reason", "store adapter has no ReconcileNodeLiveness")
	}

	// production 额外启动 Agent 的 mTLS gRPC 网关（任务分发内嵌在
	// Agent Connect 流的 claim loop 中，无需额外的分发协程）。
	// agentServer 实现 httpapi.CommandDispatcher，控制台命令经 gRPC 帧下发到
	// Agent；development 无 Agent 网关，dispatcher 为 nil 时命令仅落审计。
	agentCtx, stopAgent := context.WithCancel(context.Background())
	defer stopAgent()
	var agentServer *agentrpc.Server
	if cfg.Environment == config.Production {
		pg, ok := service.(*store.Postgres)
		if !ok {
			logger.Error("agent gRPC requires a postgres-backed store", "type", fmt.Sprintf("%T", service))
			os.Exit(1)
		}
		agentServer, err = buildAgentGRPCServer(cfg, pg, logger)
		if err != nil {
			logger.Error("initialize agent gRPC server", "error", err)
			os.Exit(1)
		}
		// 将 Agent gRPC 服务器注入 store，文件操作经 Connect 流转发到容器执行。
		pg.SetFileDispatcher(agentServer)
		go func() {
			if serveErr := agentServer.ListenAndServe(agentCtx, agentGRPCAddr(cfg), nil, nil); serveErr != nil {
				logger.Error("agent gRPC server stopped", "error", serveErr)
				os.Exit(1)
			}
		}()
		// 多副本后台对账：租约回收器把 Agent 失联/超时未回报的过期任务
		// 重新入队或按重试上限判失败；Outbox 发布器消费任务生命周期事件。
		// 两个循环都幂等且基于 SKIP LOCKED，多副本并发运行安全。
		outboxCtx, stopOutbox := context.WithCancel(context.Background())
		defer stopOutbox()
		go reconcileTaskLeases(outboxCtx, pg, logger)
		go publishOutboxEvents(outboxCtx, pg, logger)
	}

	apiOptions := controlPlaneHTTPOptions(cfg.Environment, agentServer, readiness)
	api := httpapi.New(service, logger, apiOptions...)
	handler := spa(api, cfg.WebRoot, logger)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}

	go func() {
		logger.Info("control plane listening", "addr", cfg.HTTPAddr, "environment", cfg.Environment, "adapter", adapterName(service))
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", serveErr)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	stopLiveness()
	stopAgent()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown", "error", err)
	}
}

func controlPlaneHTTPOptions(environment string, agentServer *agentrpc.Server, readiness httpapi.ReadinessChecker) []httpapi.Option {
	options := []httpapi.Option{httpapi.WithEnvironment(environment)}
	// A typed nil *agentrpc.Server becomes a non-nil interface value. Do not
	// install it in development mode: the first console command would otherwise
	// dispatch through a nil receiver and panic.
	if agentServer != nil {
		options = append(options, httpapi.WithCommandDispatcher(agentServer))
	}
	if readiness != nil {
		options = append(options, httpapi.WithReadinessChecker(readiness))
	}
	return options
}

type readinessProbe struct {
	name  string
	check func(context.Context) error
}

type dependencyReadiness struct {
	probes []readinessProbe
	redis  *redis.Client
}

func (d *dependencyReadiness) Readiness(ctx context.Context) error {
	for _, probe := range d.probes {
		if err := probe.check(ctx); err != nil {
			return fmt.Errorf("%s: %w", probe.name, err)
		}
	}
	return nil
}

func (d *dependencyReadiness) Close() error {
	if d == nil || d.redis == nil {
		return nil
	}
	return d.redis.Close()
}

func buildDependencyReadiness(ctx context.Context, cfg config.Config, service httpapi.ControlPlane) (*dependencyReadiness, error) {
	readiness := &dependencyReadiness{}
	if checker, ok := service.(httpapi.ReadinessChecker); ok {
		readiness.probes = append(readiness.probes, readinessProbe{name: "control-plane store", check: checker.Readiness})
	}
	if strings.TrimSpace(cfg.RedisURL) != "" {
		options, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("parse redis URL: %w", err)
		}
		client := redis.NewClient(options)
		readiness.redis = client
		readiness.probes = append(readiness.probes, readinessProbe{name: "redis", check: func(checkCtx context.Context) error {
			pingCtx, cancel := context.WithTimeout(checkCtx, 3*time.Second)
			defer cancel()
			return client.Ping(pingCtx).Err()
		}})
		if err := readiness.Readiness(ctx); err != nil {
			_ = readiness.Close()
			return nil, fmt.Errorf("initial dependency check: %w", err)
		}
	}
	if cfg.Environment == config.Production && len(readiness.probes) == 0 {
		return nil, errors.New("production readiness has no dependency probes")
	}
	if len(readiness.probes) == 0 {
		return nil, nil
	}
	return readiness, nil
}

// buildService 根据配置构建 httpapi.ControlPlane 实例。
// production：连接 PostgreSQL → 执行迁移 → 构造 Postgres store 并注入
// bootstrap token；development：内存适配器（可选 bootstrap 模式）。
func buildService(ctx context.Context, cfg config.Config, logger *slog.Logger) (httpapi.ControlPlane, error) {
	switch cfg.Environment {
	case config.Production:
		return buildProductionService(ctx, cfg, logger)
	default:
		return buildDevelopmentService(cfg, logger)
	}
}

// buildDevelopmentService 保留原有的内存适配器逻辑。
func buildDevelopmentService(cfg config.Config, logger *slog.Logger) (httpapi.ControlPlane, error) {
	if cfg.DevBootstrapToken != "" {
		development, err := store.NewMemoryForSetupAt(
			cfg.Environment, cfg.DevBootstrapToken, time.Now().UTC().Add(15*time.Minute),
			cfg.AgentToken, cfg.OperationLatency, cfg.DevDataRoot,
		)
		if err != nil {
			return nil, fmt.Errorf("initialize development setup adapter: %w", err)
		}
		return development, nil
	}
	development, err := store.NewMemoryAt(cfg.Environment, cfg.AdminEmail, cfg.AdminPassword, cfg.AgentToken, cfg.OperationLatency, cfg.DevDataRoot)
	if err != nil {
		return nil, fmt.Errorf("initialize development adapter: %w", err)
	}
	return development, nil
}

// buildProductionService 连接 PostgreSQL、执行迁移并构造 Postgres store。
// 迁移与 store 各自维护独立的连接池：RunMigrations 需要 *sql.DB，
// NewPostgres 内部自行建立连接，两者不共享连接。
func buildProductionService(ctx context.Context, cfg config.Config, logger *slog.Logger) (httpapi.ControlPlane, error) {
	migrationPlan, err := migrations.LoadMigrations(migrationsDir())
	if err != nil {
		return nil, fmt.Errorf("load canonical migrations: %w", err)
	}
	migrationDB, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open migration database: %w", err)
	}
	defer migrationDB.Close()
	if err := migrationDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping migration database: %w", err)
	}
	if err := migrations.RunMigrations(ctx, migrationDB, migrationsDir(), "up"); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	bootstrapToken := readBootstrapToken(cfg)
	// production 的 agent token（HTTP 心跳校验）不暴露在 config 校验中，
	// 直接读环境变量；未设置时与 bootstrap token 保持一致。
	agentToken := strings.TrimSpace(os.Getenv("GUGU_AGENT_TOKEN"))
	if agentToken == "" {
		agentToken = bootstrapToken
	}
	fileRoot := strings.TrimSpace(os.Getenv("GUGU_FILE_ROOT"))
	if fileRoot == "" {
		fileRoot = "var/production/servers"
	}

	pg, err := store.NewPostgres(ctx, cfg.DatabaseURL, cfg.Environment, agentToken, fileRoot)
	if err != nil {
		return nil, fmt.Errorf("initialize postgres store: %w", err)
	}
	pg.SetRequiredMigrationVersions(migrations.Versions(migrationPlan))
	pg.SetBootstrapToken(bootstrapToken)
	if err := configureSecretCipher(pg, cfg); err != nil {
		return nil, err
	}
	// 恢复上次运行期间持久化的控制台日志与指标（启动一次）。
	pg.RestoreTelemetry(ctx)
	logger.Info("production service ready", "file_root", fileRoot, "adapter", "postgres")
	return pg, nil
}

type secretKeyringFile struct {
	Active string            `json:"active"`
	Keys   map[string]string `json:"keys"`
}

func configureSecretCipher(pg *store.Postgres, cfg config.Config) error {
	if cfg.EncryptionKeyringFile != "" {
		contents, err := os.ReadFile(cfg.EncryptionKeyringFile)
		if err != nil {
			return fmt.Errorf("read encryption keyring file: %w", err)
		}
		var file secretKeyringFile
		if err := json.Unmarshal(contents, &file); err != nil {
			return fmt.Errorf("parse encryption keyring file: %w", err)
		}
		keys := make(map[string][]byte, len(file.Keys))
		for keyID, material := range file.Keys {
			keys[keyID] = []byte(material)
		}
		if err := pg.SetSecretKeyring(file.Active, keys); err != nil {
			return fmt.Errorf("initialize secret keyring: %w", err)
		}
		return nil
	}
	encryptionKey, err := os.ReadFile(cfg.EncryptionKeyFile)
	if err != nil {
		return fmt.Errorf("read encryption key file: %w", err)
	}
	if err := pg.SetSecretCipher(bytes.TrimSpace(encryptionKey)); err != nil {
		return fmt.Errorf("initialize secret cipher: %w", err)
	}
	return nil
}

// buildAgentGRPCServer 构造 Agent 的 mTLS gRPC 服务器（仅 production）。
// CA 目录优先取 GUGU_AGENT_CA_DIR，否则从 GUGU_AGENT_CA_CERT_FILE 所在目录
// 推断；注册令牌取 GUGU_AGENT_REGISTRATION_TOKEN，未设置时回退 bootstrap
// token。返回的 server 同时实现 httpapi.CommandDispatcher，供控制台命令
// 经 Connect 流下发到 Agent。
func buildAgentGRPCServer(cfg config.Config, pg *store.Postgres, logger *slog.Logger) (*agentrpc.Server, error) {
	caDir := strings.TrimSpace(os.Getenv("GUGU_AGENT_CA_DIR"))
	if caDir == "" {
		caDir = filepath.Dir(cfg.AgentCACertFile)
	}
	ca, err := agentca.NewCA(caDir)
	if err != nil {
		return nil, fmt.Errorf("initialize agent ca: %w", err)
	}

	registrationToken := strings.TrimSpace(os.Getenv("GUGU_AGENT_REGISTRATION_TOKEN"))
	if registrationToken == "" {
		registrationToken = readBootstrapToken(cfg)
	}

	// 面板位于 NAT/端口转发之后时，Agent 用公网 IP 校验证书；监听地址只是
	// 内网 IP，需通过 GUGU_AGENT_GRPC_PUBLIC_IPS 显式补充公网 IP SAN。
	var publicIPs []net.IP
	for _, raw := range strings.Split(strings.TrimSpace(os.Getenv("GUGU_AGENT_GRPC_PUBLIC_IPS")), ",") {
		if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
			publicIPs = append(publicIPs, ip)
		}
	}

	return agentrpc.NewServer(ca, pg, logger,
		agentrpc.WithRegistrationToken(registrationToken),
		agentrpc.WithServerIPs(publicIPs),
	), nil
}

// agentGRPCAddr 返回 Agent gRPC 监听地址，默认 127.0.0.1:8443。
func agentGRPCAddr(cfg config.Config) string {
	addr := strings.TrimSpace(os.Getenv("GUGU_AGENT_GRPC_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8443"
	}
	return addr
}

// readBootstrapToken 读取 bootstrap token 文件内容并去除首尾空白；
// 未配置时返回空串（对应跳过校验的 development 模式）。
func readBootstrapToken(cfg config.Config) string {
	if cfg.BootstrapTokenFile == "" {
		return ""
	}
	content, err := os.ReadFile(cfg.BootstrapTokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

// migrationsDir 定位仓库根下的 migrations 目录。
// 优先读取 GUGU_MIGRATIONS_DIR 环境变量（生产部署时迁移文件随二进制分发，
// 源码路径在交叉编译/发布后不存在）；否则基于当前源码文件推导，不依赖进程工作目录。
func migrationsDir() string {
	if override := strings.TrimSpace(os.Getenv("GUGU_MIGRATIONS_DIR")); override != "" {
		return override
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "migrations"
	}
	return filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations")
}

// adapterName 返回当前适配器的日志名。
func adapterName(service httpapi.ControlPlane) string {
	switch service.(type) {
	case *store.Postgres:
		return "postgres"
	case *store.Memory:
		return "development-memory"
	default:
		return "unknown"
	}
}

func newLogger(output io.Writer, level string, format string) *slog.Logger {
	configuredLevel := slog.LevelInfo
	switch level {
	case "debug":
		configuredLevel = slog.LevelDebug
	case "warn":
		configuredLevel = slog.LevelWarn
	case "error":
		configuredLevel = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: configuredLevel}
	if format == "text" {
		return slog.New(slog.NewTextHandler(output, options))
	}
	return slog.New(slog.NewJSONHandler(output, options))
}

func reconcileNodeLiveness(ctx context.Context, reconciler interface{ ReconcileNodeLiveness(time.Time) }) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			reconciler.ReconcileNodeLiveness(now.UTC())
		}
	}
}

// reconcileTaskLeases 周期回收过期任务租约（多副本恢复）：Agent 失联或
// 超时未回报的任务重新入队等待重试，重试次数耗尽的任务判失败。
func reconcileTaskLeases(ctx context.Context, pg *store.Postgres, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			pg.ReconcileTaskLeases(now.UTC())
		}
	}
}

// publishOutboxEvents 周期消费 outbox_events 中未发布的任务事件并标记
// published_at，防止未发布积压无限增长；SKIP LOCKED 保证多副本只消费一次。
func publishOutboxEvents(ctx context.Context, pg *store.Postgres, logger *slog.Logger) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			published, err := pg.PublishOutboxEvents(ctx, 50)
			if err != nil {
				logger.Warn("publish outbox events", "error", err)
				continue
			}
			if published > 0 {
				logger.Debug("outbox events published", "count", published)
			}
		}
	}
}

func spa(api http.Handler, root string, logger *slog.Logger) http.Handler {
	staticRoot, err := filepath.Abs(root)
	if err != nil {
		logger.Error("resolve web root", "error", err)
		return api
	}
	fileServer := http.FileServer(http.Dir(staticRoot))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			api.ServeHTTP(w, r)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		if strings.Contains(clean, "..") {
			http.NotFound(w, r)
			return
		}
		candidate := filepath.Join(staticRoot, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		index := filepath.Join(staticRoot, "index.html")
		if _, statErr := os.Stat(index); statErr != nil {
			http.Error(w, "web build not found; run npm run build in web", http.StatusServiceUnavailable)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = "/"
		fileServer.ServeHTTP(w, clone)
	})
}

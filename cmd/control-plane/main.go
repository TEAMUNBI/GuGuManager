package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	defer func() {
		if closer, ok := service.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()

	api := httpapi.New(service, logger)
	handler := spa(api, cfg.WebRoot, logger)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}

	// 节点存活探测只对内存适配器实现；Postgres 适配器目前没有
	// ReconcileNodeLiveness，production 下不启动该协程。
	livenessCtx, stopLiveness := context.WithCancel(context.Background())
	defer stopLiveness()
	if development, ok := service.(*store.Memory); ok {
		go reconcileNodeLiveness(livenessCtx, development)
	} else {
		logger.Info("node liveness reconciliation disabled", "reason", "postgres store has no ReconcileNodeLiveness yet")
	}

	// production 额外启动 Agent 的 mTLS gRPC 网关（任务分发内嵌在
	// Agent Connect 流的 claim loop 中，无需额外的分发协程）。
	agentCtx, stopAgent := context.WithCancel(context.Background())
	defer stopAgent()
	if cfg.Environment == config.Production {
		pg, ok := service.(*store.Postgres)
		if !ok {
			logger.Error("agent gRPC requires a postgres-backed store", "type", fmt.Sprintf("%T", service))
			os.Exit(1)
		}
		go func() {
			if serveErr := serveAgentGRPC(agentCtx, cfg, pg, logger); serveErr != nil {
				logger.Error("agent gRPC server stopped", "error", serveErr)
				os.Exit(1)
			}
		}()
	}

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
	pg.SetBootstrapToken(bootstrapToken)
	logger.Info("production service ready", "file_root", fileRoot, "adapter", "postgres")
	return pg, nil
}

// serveAgentGRPC 启动 Agent 的 mTLS gRPC 网关（仅 production）。
// CA 目录优先取 GUGU_AGENT_CA_DIR，否则从 GUGU_AGENT_CA_CERT_FILE 所在目录
// 推断；注册令牌取 GUGU_AGENT_REGISTRATION_TOKEN，未设置时回退 bootstrap
// token；监听地址取 GUGU_AGENT_GRPC_ADDR，默认 127.0.0.1:8443。
func serveAgentGRPC(ctx context.Context, cfg config.Config, pg *store.Postgres, logger *slog.Logger) error {
	caDir := strings.TrimSpace(os.Getenv("GUGU_AGENT_CA_DIR"))
	if caDir == "" {
		caDir = filepath.Dir(cfg.AgentCACertFile)
	}
	ca, err := agentca.NewCA(caDir)
	if err != nil {
		return fmt.Errorf("initialize agent ca: %w", err)
	}

	registrationToken := strings.TrimSpace(os.Getenv("GUGU_AGENT_REGISTRATION_TOKEN"))
	if registrationToken == "" {
		registrationToken = readBootstrapToken(cfg)
	}

	addr := strings.TrimSpace(os.Getenv("GUGU_AGENT_GRPC_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8443"
	}

	server := agentrpc.NewServer(ca, pg, logger, agentrpc.WithRegistrationToken(registrationToken))
	return server.ListenAndServe(ctx, addr, nil, nil)
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

// migrationsDir 定位仓库根下的 migrations 目录，不依赖进程工作目录。
func migrationsDir() string {
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

func reconcileNodeLiveness(ctx context.Context, development *store.Memory) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			development.ReconcileNodeLiveness(now.UTC())
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

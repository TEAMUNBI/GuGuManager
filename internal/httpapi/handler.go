package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
	"github.com/gugumanager/gugumanager/internal/identity"
)

const (
	sessionCookie             = "gugu_session"
	maxJSONBodyBytes          = 12 << 20
	maxAnonymousJSONBodyBytes = 64 << 10
)

var errUnsupportedMediaType = errors.New("request Content-Type must be application/json")

type ControlPlane interface {
	SetupStatus() domain.SetupStatus
	SetupAdmin(input domain.SetupAdminInput) (domain.User, error)
	Login(email string, password string) (domain.SessionView, string, error)
	Session(token string) (domain.SessionView, error)
	Logout(token string)
	ResetPassword(token string, password string) error
	ValidateCSRF(token string, csrf string) bool
	ValidateAgentToken(token string) bool
	Users() []domain.User
	UserByID(userID string) (domain.User, error)
	CreateUser(input domain.CreateUserInput, actor domain.User) (domain.User, error)
	UpdateUser(userID string, input domain.UpdateUserInput, actor domain.User) (domain.User, error)
	IssuePasswordResetToken(userID string, actor domain.User) (domain.PasswordResetToken, error)
	ServerMembership(serverID string, userID string) (domain.ServerMembership, error)
	PutServerMembership(serverID string, userID string, permissions []string, actor domain.User) (domain.ServerMembership, error)
	DeleteServerMembership(serverID string, userID string, actor domain.User) error
	AuthorizeServer(userID string, serverID string, permission string) error
	EffectiveServerPermissions(userID string, serverID string) (domain.ServerPermissions, error)
	VisibleServers(userID string, query string) []domain.Server
	Overview() domain.Overview
	Servers(query string) []domain.Server
	Server(serverID string) (domain.Server, error)
	CreateServer(input domain.CreateServerInput, idempotencyKey string, actor domain.User) (domain.Operation, error)
	RequestPower(serverID string, action domain.PowerAction, idempotencyKey string, actor domain.User) (domain.Operation, error)
	Allocations(serverID string) ([]domain.Allocation, error)
	CreateAllocation(serverID string, input domain.CreateAllocationInput, expectedGeneration int64, idempotencyKey string, actor domain.User) (domain.Operation, error)
	SetPrimaryAllocation(serverID string, allocationID string, expectedGeneration int64, idempotencyKey string, actor domain.User) (domain.Operation, error)
	DeleteAllocation(serverID string, allocationID string, expectedGeneration int64, idempotencyKey string, actor domain.User) (domain.Operation, error)
	Startup(serverID string) (domain.Startup, error)
	UpdateStartup(serverID string, updates map[string]any, expectedGeneration int64, idempotencyKey string, actor domain.User) (domain.Operation, error)
	VisibleOperations(userID string) []domain.Operation
	Operation(operationID string) (domain.Operation, error)
	Nodes() []domain.Node
	GameDefinitions() []domain.GameDefinition
	AuditEvents() []domain.AuditEvent
	Console(serverID string) ([]domain.ConsoleLine, error)
	SendConsoleCommand(serverID string, command string, actor domain.User) error
	Files(serverID string, requestedPath string) ([]domain.FileEntry, error)
	ReadFile(serverID string, requestedPath string) (domain.FileContent, error)
	WriteFile(serverID string, requestedPath string, content []byte, actor domain.User) error
	CreateDirectory(serverID string, requestedPath string, actor domain.User) error
	MoveFile(serverID string, source string, destination string, replace bool, actor domain.User) error
	DeleteFile(serverID string, requestedPath string, recursive bool, actor domain.User) error
	Backups(serverID string) ([]domain.Backup, error)
	CreateBackup(serverID string, idempotencyKey string, actor domain.User) (domain.Operation, error)
	RestoreBackup(serverID string, backupID string, idempotencyKey string, actor domain.User) (domain.Operation, error)
	DeleteBackup(serverID string, backupID string, idempotencyKey string, actor domain.User) (domain.Operation, error)
	DownloadBackup(serverID string, backupID string, actor domain.User) (domain.BackupContent, error)
	Heartbeat(nodeName string, agentVersion string) error
}

// CommandDispatcher 向节点 Connect 流下发控制台命令帧。
// 生产环境由 agentrpc.Server 实现；开发环境可注入 nil（仅校验+审计）。
type CommandDispatcher interface {
	SendConsoleCommand(nodeID, serverID, command string) error
}

// Option 配置 Handler。
type Option func(*Handler)

// WithCommandDispatcher 注入命令下发器，使控制台命令经 gRPC 帧到达 Agent。
func WithCommandDispatcher(d CommandDispatcher) Option {
	return func(h *Handler) { h.dispatcher = d }
}

// WithEnvironment 注入运行环境（development/production），供 readyz 探针报告适配器。
func WithEnvironment(environment string) Option {
	return func(h *Handler) { h.environment = environment }
}

type Handler struct {
	service          ControlPlane
	logger           *slog.Logger
	dispatcher       CommandDispatcher
	loginLimiter     *identity.AttemptLimiter
	sensitiveLimiter *identity.AttemptLimiter
	environment      string
}

type principal struct {
	Token   string
	Session domain.SessionView
}

type contextKey string

const principalKey contextKey = "principal"

func New(service ControlPlane, logger *slog.Logger, opts ...Option) http.Handler {
	h := &Handler{
		service: service,
		logger:  logger,
		loginLimiter: identity.NewAttemptLimiter(identity.AttemptLimit{
			Maximum: 5, Window: 5 * time.Minute, BlockFor: 15 * time.Minute,
		}, time.Now),
		sensitiveLimiter: identity.NewAttemptLimiter(identity.AttemptLimit{
			Maximum: 5, Window: 5 * time.Minute, BlockFor: 15 * time.Minute,
		}, time.Now),
	}
	for _, opt := range opts {
		opt(h)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /readyz", h.ready)
	mux.HandleFunc("GET /api/v1/setup/status", h.setupStatus)
	mux.HandleFunc("POST /api/v1/setup/admin", h.setupAdmin)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/password-reset", h.passwordReset)
	mux.HandleFunc("GET /api/v1/auth/session", h.auth(h.session))
	mux.HandleFunc("POST /api/v1/auth/logout", h.auth(h.logout))
	mux.HandleFunc("GET /api/v1/users", h.auth(h.users))
	mux.HandleFunc("POST /api/v1/users", h.auth(h.createUser))
	mux.HandleFunc("GET /api/v1/users/{userID}", h.auth(h.user))
	mux.HandleFunc("PATCH /api/v1/users/{userID}", h.auth(h.updateUser))
	mux.HandleFunc("POST /api/v1/users/{userID}/password-reset-tokens", h.auth(h.issuePasswordResetToken))
	mux.HandleFunc("GET /api/v1/overview", h.auth(h.overview))
	mux.HandleFunc("GET /api/v1/servers", h.auth(h.servers))
	mux.HandleFunc("POST /api/v1/servers", h.auth(h.createServer))
	mux.HandleFunc("GET /api/v1/servers/{serverID}", h.auth(h.server))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/permissions", h.auth(h.serverPermissions))
	mux.HandleFunc("POST /api/v1/servers/{serverID}/power", h.auth(h.power))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/allocations", h.auth(h.allocations))
	mux.HandleFunc("POST /api/v1/servers/{serverID}/allocations", h.auth(h.createAllocation))
	mux.HandleFunc("PATCH /api/v1/servers/{serverID}/allocations/{allocationID}", h.auth(h.setPrimaryAllocation))
	mux.HandleFunc("DELETE /api/v1/servers/{serverID}/allocations/{allocationID}", h.auth(h.deleteAllocation))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/startup", h.auth(h.startup))
	mux.HandleFunc("PUT /api/v1/servers/{serverID}/startup", h.auth(h.updateStartup))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/members/{userID}", h.auth(h.serverMembership))
	mux.HandleFunc("PUT /api/v1/servers/{serverID}/members/{userID}", h.auth(h.putServerMembership))
	mux.HandleFunc("DELETE /api/v1/servers/{serverID}/members/{userID}", h.auth(h.deleteServerMembership))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/console", h.auth(h.console))
	mux.HandleFunc("POST /api/v1/servers/{serverID}/console/commands", h.auth(h.consoleCommand))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/files", h.auth(h.files))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/files/content", h.auth(h.fileContent))
	mux.HandleFunc("PUT /api/v1/servers/{serverID}/files/content", h.auth(h.writeFileContent))
	mux.HandleFunc("POST /api/v1/servers/{serverID}/files/directories", h.auth(h.createDirectory))
	mux.HandleFunc("POST /api/v1/servers/{serverID}/files/moves", h.auth(h.moveFile))
	mux.HandleFunc("DELETE /api/v1/servers/{serverID}/files", h.auth(h.deleteFile))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/backups", h.auth(h.backups))
	mux.HandleFunc("POST /api/v1/servers/{serverID}/backups", h.auth(h.createBackup))
	mux.HandleFunc("POST /api/v1/servers/{serverID}/backups/{backupID}/restore", h.auth(h.restoreBackup))
	mux.HandleFunc("DELETE /api/v1/servers/{serverID}/backups/{backupID}", h.auth(h.deleteBackup))
	mux.HandleFunc("GET /api/v1/servers/{serverID}/backups/{backupID}/download", h.auth(h.downloadBackup))
	mux.HandleFunc("GET /api/v1/nodes", h.auth(h.nodes))
	mux.HandleFunc("GET /api/v1/game-definitions", h.auth(h.games))
	mux.HandleFunc("GET /api/v1/operations", h.auth(h.operations))
	mux.HandleFunc("GET /api/v1/operations/{operationID}", h.auth(h.operation))
	mux.HandleFunc("GET /api/v1/audit-events", h.auth(h.audit))
	return h.middleware(mux)
}

func (h *Handler) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		traceID := id.New()
		r.Header.Set("X-Trace-Id", traceID)
		w.Header().Set("X-Trace-Id", traceID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		defer func() {
			if recovered := recover(); recovered != nil {
				h.logger.Error("request panic", "traceId", traceID, "error", recovered)
				h.writeError(w, traceID, domain.NewProblem("INTERNAL_ERROR", "服务器内部错误", true))
			}
			h.logger.Info("request", "method", r.Method, "path", r.URL.Path, "traceId", traceID, "durationMs", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			h.writeError(w, traceID(r), domain.NewProblem("AUTH_REQUIRED", "请先登录", false))
			return
		}
		session, err := h.service.Session(cookie.Value)
		if err != nil {
			h.writeError(w, traceID(r), err)
			return
		}
		ctx := context.WithValue(r.Context(), principalKey, principal{Token: cookie.Value, Session: session})
		next(w, r.WithContext(ctx))
	}
}

func (h *Handler) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	actor := principalFrom(r)
	if !h.service.ValidateCSRF(actor.Token, r.Header.Get("X-CSRF-Token")) {
		h.writeError(w, traceID(r), domain.NewProblem("CSRF_FAILED", "请求安全令牌无效，请刷新页面后重试", false))
		return false
	}
	return true
}

func (h *Handler) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) bool {
	for _, role := range principalFrom(r).Session.User.Roles {
		if role == "platform_admin" {
			return true
		}
	}
	h.writeError(w, traceID(r), domain.NewProblem("FORBIDDEN", "需要平台管理员权限", false))
	return false
}

func (h *Handler) authorizeServer(w http.ResponseWriter, r *http.Request, permission string) bool {
	actor := principalFrom(r).Session.User
	if err := h.service.AuthorizeServer(actor.ID, r.PathValue("serverID"), permission); err != nil {
		h.writeError(w, traceID(r), err)
		return false
	}
	return true
}

func (h *Handler) allowSensitiveRequest(w http.ResponseWriter, r *http.Request, scope string) (*identity.AttemptReservation, string, bool) {
	key := scope + ":ip:" + requestSourceIP(r)
	reservation, allowed, retryAfter := h.sensitiveLimiter.Reserve(key)
	if !allowed {
		seconds := max(int64(1), int64((retryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		h.writeError(w, traceID(r), domain.NewProblem("RATE_LIMITED", "敏感操作尝试过于频繁，请稍后重试", true))
		return nil, key, false
	}
	return reservation, key, true
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *Handler) ready(w http.ResponseWriter, _ *http.Request) {
	adapter := h.environment
	if adapter == "" {
		adapter = "development-memory"
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "adapter": adapter})
}

func (h *Handler) setupStatus(w http.ResponseWriter, _ *http.Request) {
	h.writeData(w, http.StatusOK, h.service.SetupStatus())
}

func (h *Handler) setupAdmin(w http.ResponseWriter, r *http.Request) {
	reservation, limitKey, allowed := h.allowSensitiveRequest(w, r, "setup")
	if !allowed {
		return
	}
	defer reservation.CompleteFailure()
	var request domain.SetupAdminInput
	if err := decodeAnonymousJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "初始化请求格式无效")
		return
	}
	user, err := h.service.SetupAdmin(request)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	reservation.CompleteSuccess(limitKey)
	h.writeData(w, http.StatusCreated, user)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeAnonymousJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "登录请求格式无效")
		return
	}
	request.Email = strings.TrimSpace(request.Email)
	parsedEmail, emailErr := mail.ParseAddress(request.Email)
	passwordLength := len([]rune(request.Password))
	if emailErr != nil || parsedEmail.Address != request.Email || len([]rune(request.Email)) > 254 || passwordLength < 8 || passwordLength > 1024 {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "邮箱或密码格式无效", false))
		return
	}
	accountKey := "account:" + strings.ToLower(request.Email)
	sourceKey := "ip:" + requestSourceIP(r)
	reservation, allowed, retryAfter := h.loginLimiter.Reserve(accountKey, sourceKey)
	if !allowed {
		seconds := max(int64(1), int64((retryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		h.writeError(w, traceID(r), domain.NewProblem("RATE_LIMITED", "登录尝试过于频繁，请稍后重试", true))
		return
	}
	defer reservation.CompleteFailure()
	session, token, err := h.service.Login(request.Email, request.Password)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	reservation.CompleteSuccess(accountKey)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: 12 * 60 * 60, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: session.Environment == "production"})
	h.writeData(w, http.StatusOK, session)
}

func (h *Handler) passwordReset(w http.ResponseWriter, r *http.Request) {
	reservation, limitKey, allowed := h.allowSensitiveRequest(w, r, "password-reset")
	if !allowed {
		return
	}
	defer reservation.CompleteFailure()
	var request struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeAnonymousJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "密码重置请求格式无效")
		return
	}
	if err := h.service.ResetPassword(request.Token, request.Password); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	reservation.CompleteSuccess(limitKey)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	h.writeData(w, http.StatusOK, principalFrom(r).Session)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	actor := principalFrom(r)
	h.service.Logout(actor.Token)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: actor.Session.Environment == "production"})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) users(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	h.writeData(w, http.StatusOK, h.service.Users())
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) || !h.requireCSRF(w, r) {
		return
	}
	var request domain.CreateUserInput
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "用户创建请求格式无效")
		return
	}
	user, err := h.service.CreateUser(request, principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusCreated, user)
}

func (h *Handler) user(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	user, err := h.service.UserByID(r.PathValue("userID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, user)
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) || !h.requireCSRF(w, r) {
		return
	}
	var request domain.UpdateUserInput
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "用户更新请求格式无效")
		return
	}
	user, err := h.service.UpdateUser(r.PathValue("userID"), request, principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, user)
}

func (h *Handler) issuePasswordResetToken(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) || !h.requireCSRF(w, r) {
		return
	}
	issued, err := h.service.IssuePasswordResetToken(r.PathValue("userID"), principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusCreated, issued)
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	h.writeData(w, http.StatusOK, h.service.Overview())
}

func (h *Handler) servers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	if len([]rune(query)) > 100 {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "搜索内容不能超过 100 个字符", false))
		return
	}
	limit := 25
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 100 {
			h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "limit 必须在 1 到 100 之间", false))
			return
		}
		limit = parsed
	}
	items, nextCursor, err := paginateServers(h.service.VisibleServers(principalFrom(r).Session.User.ID, query), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"data": items, "page": map[string]any{"nextCursor": nextCursor}})
}

func paginateServers(items []domain.Server, cursor string, limit int) ([]domain.Server, any, error) {
	start := 0
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, nil, domain.NewProblem("VALIDATION_FAILED", "cursor 无效", false)
		}
		found := false
		for index, item := range items {
			if item.ID == string(decoded) {
				start = index + 1
				found = true
				break
			}
		}
		if !found {
			return nil, nil, domain.NewProblem("VALIDATION_FAILED", "cursor 已过期或不属于当前结果集", false)
		}
	}
	end := min(start+limit, len(items))
	page := items[start:end]
	var nextCursor any
	if end < len(items) {
		nextCursor = base64.RawURLEncoding.EncodeToString([]byte(items[end-1].ID))
	}
	return page, nextCursor, nil
}

func (h *Handler) server(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.read") {
		return
	}
	server, err := h.service.Server(r.PathValue("serverID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, server)
}

func (h *Handler) serverPermissions(w http.ResponseWriter, r *http.Request) {
	permissions, err := h.service.EffectiveServerPermissions(principalFrom(r).Session.User.ID, r.PathValue("serverID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, permissions)
}

func (h *Handler) createServer(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) || !h.requireCSRF(w, r) {
		return
	}
	var request domain.CreateServerInput
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "服务器配置格式无效")
		return
	}
	operation, err := h.service.CreateServer(request, r.Header.Get("Idempotency-Key"), principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) power(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.power") || !h.requireCSRF(w, r) {
		return
	}
	var request struct {
		Action domain.PowerAction `json:"action"`
	}
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "电源请求格式无效")
		return
	}
	operation, err := h.service.RequestPower(r.PathValue("serverID"), request.Action, r.Header.Get("Idempotency-Key"), principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) allocations(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.network.read") {
		return
	}
	allocations, err := h.service.Allocations(r.PathValue("serverID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, allocations)
}

func (h *Handler) createAllocation(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.network.write") || !h.requireCSRF(w, r) {
		return
	}
	generation, ok := h.ifMatchGeneration(w, r)
	if !ok {
		return
	}
	var request struct {
		BindIP   string `json:"bindIp"`
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
		Primary  *bool  `json:"primary"`
	}
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "网络分配请求格式无效")
		return
	}
	if request.Primary == nil {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "网络分配请求格式无效", false))
		return
	}
	operation, err := h.service.CreateAllocation(
		r.PathValue("serverID"),
		domain.CreateAllocationInput{BindIP: request.BindIP, Port: request.Port, Protocol: request.Protocol, Primary: *request.Primary},
		generation,
		r.Header.Get("Idempotency-Key"),
		principalFrom(r).Session.User,
	)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) setPrimaryAllocation(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.network.write") || !h.requireCSRF(w, r) {
		return
	}
	generation, ok := h.ifMatchGeneration(w, r)
	if !ok {
		return
	}
	var request struct {
		Primary *bool `json:"primary"`
	}
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "PATCH 仅支持将网络分配提升为主分配")
		return
	}
	if request.Primary == nil || !*request.Primary {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "PATCH 仅支持将网络分配提升为主分配", false))
		return
	}
	operation, err := h.service.SetPrimaryAllocation(
		r.PathValue("serverID"),
		r.PathValue("allocationID"),
		generation,
		r.Header.Get("Idempotency-Key"),
		principalFrom(r).Session.User,
	)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) deleteAllocation(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.network.write") || !h.requireCSRF(w, r) {
		return
	}
	generation, ok := h.ifMatchGeneration(w, r)
	if !ok {
		return
	}
	operation, err := h.service.DeleteAllocation(
		r.PathValue("serverID"),
		r.PathValue("allocationID"),
		generation,
		r.Header.Get("Idempotency-Key"),
		principalFrom(r).Session.User,
	)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) startup(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.startup.read") {
		return
	}
	startup, err := h.service.Startup(r.PathValue("serverID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	publicStartup := startup
	publicStartup.Variables = append([]domain.StartupVariable(nil), startup.Variables...)
	for index := range publicStartup.Variables {
		if !publicStartup.Variables[index].Secret {
			continue
		}
		publicStartup.Variables[index].Default = nil
		publicStartup.Variables[index].Value = nil
		publicStartup.Variables[index].EnumValues = nil
		publicStartup.Variables[index].ConstValue = nil
	}
	h.writeData(w, http.StatusOK, publicStartup)
}

type updateStartupRequest struct {
	Variables map[string]any
}

func (request *updateStartupRequest) UnmarshalJSON(data []byte) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	rawVariables, ok := members["variables"]
	if !ok || len(members) != 1 {
		return errors.New("startup update request must contain the exact variables member")
	}

	decoder := json.NewDecoder(bytes.NewReader(rawVariables))
	decoder.UseNumber()
	if err := decoder.Decode(&request.Variables); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("startup variables contain multiple JSON values")
	}
	return nil
}

func (h *Handler) updateStartup(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.startup.write") || !h.requireCSRF(w, r) {
		return
	}
	generation, ok := h.ifMatchGeneration(w, r)
	if !ok {
		return
	}
	var request updateStartupRequest
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "variables 至少需要包含一个启动变量")
		return
	}
	if len(request.Variables) == 0 {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "variables 至少需要包含一个启动变量", false))
		return
	}
	operation, err := h.service.UpdateStartup(
		r.PathValue("serverID"),
		request.Variables,
		generation,
		r.Header.Get("Idempotency-Key"),
		principalFrom(r).Session.User,
	)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) serverMembership(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	membership, err := h.service.ServerMembership(r.PathValue("serverID"), r.PathValue("userID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, membership)
}

func (h *Handler) putServerMembership(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) || !h.requireCSRF(w, r) {
		return
	}
	var request struct {
		Permissions []string `json:"permissions"`
	}
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "服务器成员请求格式无效")
		return
	}
	membership, err := h.service.PutServerMembership(
		r.PathValue("serverID"), r.PathValue("userID"), request.Permissions, principalFrom(r).Session.User,
	)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, membership)
}

func (h *Handler) deleteServerMembership(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) || !h.requireCSRF(w, r) {
		return
	}
	if err := h.service.DeleteServerMembership(
		r.PathValue("serverID"), r.PathValue("userID"), principalFrom(r).Session.User,
	); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ifMatchGeneration(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	generation, err := strconv.ParseInt(raw, 10, 64)
	if raw == "" || err != nil || generation < 1 {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "If-Match 必须是正整数 generation", false))
		return 0, false
	}
	return generation, true
}

func (h *Handler) operation(w http.ResponseWriter, r *http.Request) {
	operation, err := h.service.Operation(r.PathValue("operationID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	if err := h.service.AuthorizeServer(principalFrom(r).Session.User.ID, operation.ServerID, "servers.read"); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, operation)
}

func (h *Handler) operations(w http.ResponseWriter, r *http.Request) {
	limit := 25
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 100 {
			h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "limit 必须在 1 到 100 之间", false))
			return
		}
		limit = parsed
	}
	items, nextCursor, err := paginateOperations(
		h.service.VisibleOperations(principalFrom(r).Session.User.ID),
		r.URL.Query().Get("cursor"),
		limit,
	)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"data": items, "page": map[string]any{"nextCursor": nextCursor}})
}

func paginateOperations(items []domain.Operation, cursor string, limit int) ([]domain.Operation, any, error) {
	start := 0
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, nil, domain.NewProblem("VALIDATION_FAILED", "cursor 无效", false)
		}
		found := false
		for index, item := range items {
			if item.ID == string(decoded) {
				start = index + 1
				found = true
				break
			}
		}
		if !found {
			return nil, nil, domain.NewProblem("VALIDATION_FAILED", "cursor 已过期或不属于当前结果集", false)
		}
	}
	end := min(start+limit, len(items))
	page := items[start:end]
	var nextCursor any
	if end < len(items) {
		nextCursor = base64.RawURLEncoding.EncodeToString([]byte(items[end-1].ID))
	}
	return page, nextCursor, nil
}

func (h *Handler) nodes(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	h.writeData(w, http.StatusOK, h.service.Nodes())
}

func (h *Handler) games(w http.ResponseWriter, _ *http.Request) {
	h.writeData(w, http.StatusOK, h.service.GameDefinitions())
}

func (h *Handler) audit(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	h.writeData(w, http.StatusOK, h.service.AuditEvents())
}

func (h *Handler) console(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.console") {
		return
	}
	lines, err := h.service.Console(r.PathValue("serverID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, lines)
}

func (h *Handler) consoleCommand(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.console") || !h.requireCSRF(w, r) {
		return
	}
	var request struct {
		Command string `json:"command"`
	}
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "控制台命令格式无效")
		return
	}
	serverID := r.PathValue("serverID")
	// store 层负责校验、权限与审计；校验通过后真实帧由 dispatcher 下发。
	if err := h.service.SendConsoleCommand(serverID, request.Command, principalFrom(r).Session.User); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	if h.dispatcher != nil {
		server, err := h.service.Server(serverID)
		if err != nil {
			h.writeError(w, traceID(r), err)
			return
		}
		if server.NodeID == "" {
			h.writeError(w, traceID(r), domain.NewProblem("OPERATION_CONFLICT", "服务器未绑定节点", false))
			return
		}
		if err := h.dispatcher.SendConsoleCommand(server.NodeID, serverID, request.Command); err != nil {
			h.logger.Warn("dispatch console command", "server", serverID, "node", server.NodeID, "error", err)
			h.writeError(w, traceID(r), domain.NewProblem("NODE_OFFLINE", "节点离线，命令未下发", false))
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) files(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.files.read") {
		return
	}
	entries, err := h.service.Files(r.PathValue("serverID"), r.URL.Query().Get("path"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, entries)
}

func (h *Handler) fileContent(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.files.read") {
		return
	}
	requestedPath := r.URL.Query().Get("path")
	if strings.TrimSpace(requestedPath) == "" {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "path 不能为空", false))
		return
	}
	content, err := h.service.ReadFile(r.PathValue("serverID"), requestedPath)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, content)
}

func (h *Handler) writeFileContent(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.files.write") || !h.requireCSRF(w, r) {
		return
	}
	var request struct {
		Path     string  `json:"path"`
		Content  *string `json:"content"`
		Encoding string  `json:"encoding"`
	}
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "文件写入请求格式无效")
		return
	}
	if strings.TrimSpace(request.Path) == "" || request.Content == nil || (request.Encoding != "" && request.Encoding != "utf-8" && request.Encoding != "base64") {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "文件写入请求格式无效", false))
		return
	}
	content := []byte(*request.Content)
	if request.Encoding == "base64" {
		decoded, err := base64.RawStdEncoding.Strict().DecodeString(*request.Content)
		if err != nil {
			h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "base64 文件内容无效", false))
			return
		}
		content = decoded
	}
	if err := h.service.WriteFile(r.PathValue("serverID"), request.Path, content, principalFrom(r).Session.User); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) createDirectory(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.files.write") || !h.requireCSRF(w, r) {
		return
	}
	var request struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "目录路径无效")
		return
	}
	if strings.TrimSpace(request.Path) == "" {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "目录路径无效", false))
		return
	}
	if err := h.service.CreateDirectory(r.PathValue("serverID"), request.Path, principalFrom(r).Session.User); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) moveFile(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.files.write") || !h.requireCSRF(w, r) {
		return
	}
	var request struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		Replace     *bool  `json:"replace"`
	}
	if err := decodeJSON(r, &request); err != nil {
		h.writeJSONDecodeError(w, r, err, "移动文件请求格式无效")
		return
	}
	if strings.TrimSpace(request.Source) == "" || strings.TrimSpace(request.Destination) == "" || request.Replace == nil {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "移动文件请求格式无效", false))
		return
	}
	if err := h.service.MoveFile(r.PathValue("serverID"), request.Source, request.Destination, *request.Replace, principalFrom(r).Session.User); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteFile(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.files.write") || !h.requireCSRF(w, r) {
		return
	}
	requestedPath := r.URL.Query().Get("path")
	if strings.TrimSpace(requestedPath) == "" {
		h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "path 不能为空", false))
		return
	}
	recursive := false
	if raw := r.URL.Query().Get("recursive"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", "recursive 必须是布尔值", false))
			return
		}
		recursive = parsed
	}
	if err := h.service.DeleteFile(r.PathValue("serverID"), requestedPath, recursive, principalFrom(r).Session.User); err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) backups(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.backups.read") {
		return
	}
	backups, err := h.service.Backups(r.PathValue("serverID"))
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusOK, backups)
}

func (h *Handler) createBackup(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.backups.create") || !h.requireCSRF(w, r) {
		return
	}
	operation, err := h.service.CreateBackup(r.PathValue("serverID"), r.Header.Get("Idempotency-Key"), principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) restoreBackup(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.backups.restore") || !h.requireCSRF(w, r) {
		return
	}
	operation, err := h.service.RestoreBackup(r.PathValue("serverID"), r.PathValue("backupID"), r.Header.Get("Idempotency-Key"), principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.backups.delete") || !h.requireCSRF(w, r) {
		return
	}
	operation, err := h.service.DeleteBackup(r.PathValue("serverID"), r.PathValue("backupID"), r.Header.Get("Idempotency-Key"), principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	h.writeData(w, http.StatusAccepted, operation)
}

func (h *Handler) downloadBackup(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeServer(w, r, "servers.backups.read") {
		return
	}
	content, err := h.service.DownloadBackup(r.PathValue("serverID"), r.PathValue("backupID"), principalFrom(r).Session.User)
	if err != nil {
		h.writeError(w, traceID(r), err)
		return
	}
	payload := content.Content
	if content.Base64 {
		decoded, decodeErr := base64.StdEncoding.DecodeString(string(payload))
		if decodeErr != nil {
			h.writeError(w, traceID(r), domain.NewProblem("INTERNAL_ERROR", "备份内容解码失败", true))
			return
		}
		payload = decoded
	}
	filename := content.Filename
	if filename == "" {
		filename = r.PathValue("backupID") + ".tar.gz"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		h.logger.Warn("backup download: write response", "traceId", traceID(r), "error", err)
	}
}

func (h *Handler) writeData(w http.ResponseWriter, status int, data any) {
	h.writeJSON(w, status, map[string]any{"data": data})
}

func (h *Handler) writeJSONDecodeError(w http.ResponseWriter, r *http.Request, err error, validationMessage string) {
	if errors.Is(err, errUnsupportedMediaType) {
		h.writeError(w, traceID(r), domain.NewProblem("UNSUPPORTED_MEDIA_TYPE", "请求 Content-Type 必须为 application/json", false))
		return
	}
	h.writeError(w, traceID(r), domain.NewProblem("VALIDATION_FAILED", validationMessage, false))
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			h.logger.Error("encode response", "error", err)
		}
	}
}

func (h *Handler) writeError(w http.ResponseWriter, requestTraceID string, err error) {
	problem := domain.NewProblem("INTERNAL_ERROR", "服务器内部错误", true)
	var typed *domain.Problem
	if errors.As(err, &typed) {
		problem = typed
	}
	status := statusFor(problem.Code)
	h.writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": problem.Code, "message": problem.Message, "retryable": problem.Retryable,
		"operationId": nil, "traceId": requestTraceID, "details": problem.Details,
	}})
}

func decodeJSON(r *http.Request, target any) error {
	return decodeJSONWithLimit(r, target, maxJSONBodyBytes)
}

func decodeAnonymousJSON(r *http.Request, target any) error {
	return decodeJSONWithLimit(r, target, maxAnonymousJSONBodyBytes)
}

func decodeJSONWithLimit(r *http.Request, target any, maximumBodyBytes int64) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errUnsupportedMediaType
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maximumBodyBytes+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maximumBodyBytes {
		return fmt.Errorf("request body exceeds %d bytes", maximumBodyBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request contains multiple JSON values")
	}
	return nil
}

func statusFor(code string) int {
	switch code {
	case "AUTH_REQUIRED", "AUTH_INVALID_CREDENTIALS", "AUTH_INVALID_RESET_TOKEN", "BOOTSTRAP_TOKEN_INVALID":
		return http.StatusUnauthorized
	case "CSRF_FAILED", "FORBIDDEN":
		return http.StatusForbidden
	case "UNSUPPORTED_MEDIA_TYPE":
		return http.StatusUnsupportedMediaType
	case "NOT_FOUND":
		return http.StatusNotFound
	case "OPERATION_CONFLICT", "OPERATION_IN_PROGRESS", "IDEMPOTENCY_KEY_REUSED", "PORT_CONFLICT", "RESTORE_LOCKED", "EMAIL_CONFLICT", "SETUP_ALREADY_COMPLETE":
		return http.StatusConflict
	case "PRECONDITION_FAILED":
		return http.StatusPreconditionFailed
	case "VALIDATION_FAILED", "PATH_ESCAPE_BLOCKED", "INSUFFICIENT_RESOURCE", "GAME_DEFINITION_NOT_APPROVED", "PACKAGE_INCOMPATIBLE":
		return http.StatusUnprocessableEntity
	case "BACKUP_INTEGRITY_FAILED":
		return http.StatusUnprocessableEntity
	case "RATE_LIMITED":
		return http.StatusTooManyRequests
	case "NODE_OFFLINE":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func requestSourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if value := strings.TrimSpace(r.RemoteAddr); value != "" {
		return value
	}
	return "unknown"
}

func principalFrom(r *http.Request) principal {
	actor, _ := r.Context().Value(principalKey).(principal)
	return actor
}

func traceID(r *http.Request) string {
	if value := r.Header.Get("X-Trace-Id"); value != "" {
		return value
	}
	return id.New()
}

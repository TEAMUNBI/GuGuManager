package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gugumanager/gugumanager/internal/config"
	"github.com/gugumanager/gugumanager/internal/httpapi"
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
	var development *store.Memory
	if cfg.DevBootstrapToken != "" {
		development, err = store.NewMemoryForSetupAt(
			cfg.Environment, cfg.DevBootstrapToken, time.Now().UTC().Add(15*time.Minute),
			cfg.AgentToken, cfg.OperationLatency, cfg.DevDataRoot,
		)
	} else {
		development, err = store.NewMemoryAt(cfg.Environment, cfg.AdminEmail, cfg.AdminPassword, cfg.AgentToken, cfg.OperationLatency, cfg.DevDataRoot)
	}
	if err != nil {
		logger.Error("initialize development adapter", "error", err)
		os.Exit(1)
	}
	api := httpapi.New(development, logger)
	handler := spa(api, cfg.WebRoot, logger)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	livenessCtx, stopLiveness := context.WithCancel(context.Background())
	defer stopLiveness()
	go reconcileNodeLiveness(livenessCtx, development)

	go func() {
		logger.Info("control plane listening", "addr", cfg.HTTPAddr, "environment", cfg.Environment, "adapter", "development-memory")
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", serveErr)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	stopLiveness()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown", "error", err)
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

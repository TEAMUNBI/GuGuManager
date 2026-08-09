package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gugumanager/gugumanager/internal/agent"
)

func main() {
	once := flag.Bool("once", false, "run a single enroll/connect session and exit (no auto-reconnect)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := agent.LoadConfig()
	if err != nil {
		logger.Error("load config failed", "error", err)
		os.Exit(1)
	}
	if cfg.PanelAddr == "" {
		logger.Error("GUGU_AGENT_PANEL_ADDR is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *once {
		err = agent.RunOnce(ctx, cfg, logger)
	} else {
		err = agent.Run(ctx, cfg, logger)
	}
	if err != nil {
		logger.Error("agent stopped with error", "error", err)
		os.Exit(1)
	}
	logger.Info("agent exiting")
}

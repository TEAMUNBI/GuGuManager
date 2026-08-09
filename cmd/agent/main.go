package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type heartbeat struct {
	NodeName     string `json:"nodeName"`
	AgentVersion string `json:"agentVersion"`
}

func main() {
	panel := flag.String("panel", envOr("GUGU_PANEL_URL", "http://127.0.0.1:8080"), "Control Plane URL")
	node := flag.String("node", envOr("GUGU_NODE_NAME", "nimbus-east-01"), "registered node name")
	token := flag.String("token", envOr("GUGU_AGENT_TOKEN", "gugu-agent-dev-token"), "development registration token")
	once := flag.Bool("once", false, "send one heartbeat and exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Warn("running development HTTP heartbeat adapter; production requires outbound mTLS gRPC", "node", *node)
	client := &http.Client{Timeout: 10 * time.Second}
	send := func() error {
		payload, err := json.Marshal(heartbeat{NodeName: *node, AgentVersion: "agent 0.1.0-dev"})
		if err != nil {
			return err
		}
		request, err := http.NewRequest(http.MethodPost, strings.TrimRight(*panel, "/")+"/api/v1/dev/agent/heartbeat", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+*token)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
			return fmt.Errorf("heartbeat returned %s: %s", response.Status, strings.TrimSpace(string(body)))
		}
		return nil
	}

	if *once {
		if err := send(); err != nil {
			logger.Error("heartbeat failed", "error", err)
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := send(); err != nil {
			logger.Error("heartbeat failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func envOr(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

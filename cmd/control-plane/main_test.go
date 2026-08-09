package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewLoggerHonorsConfiguredLevelAndFormat(t *testing.T) {
	t.Run("debug json", func(t *testing.T) {
		var output bytes.Buffer
		logger := newLogger(&output, "debug", "json")
		logger.Debug("diagnostic", "component", "control-plane")

		var event map[string]any
		if err := json.Unmarshal(output.Bytes(), &event); err != nil {
			t.Fatalf("logger output is not JSON: %v; output=%q", err, output.String())
		}
		if event["level"] != "DEBUG" || event["msg"] != "diagnostic" || event["component"] != "control-plane" {
			t.Fatalf("JSON log event = %+v", event)
		}
	})

	t.Run("warn text", func(t *testing.T) {
		var output bytes.Buffer
		logger := newLogger(&output, "warn", "text")
		logger.Info("filtered")
		logger.Warn("visible", "component", "control-plane")

		line := output.String()
		if strings.Contains(line, "filtered") {
			t.Fatalf("info event bypassed warn filter: %q", line)
		}
		if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "msg=visible") || !strings.Contains(line, "component=control-plane") {
			t.Fatalf("text log event = %q", line)
		}
	})
}

package runtime

import (
	"strings"
	"testing"
)

func TestParseContainerStats(t *testing.T) {
	raw := `{
	  "cpu_stats": {"cpu_usage": {"total_usage": 2000}, "system_cpu_usage": 100000, "online_cpus": 4},
	  "precpu_stats": {"cpu_usage": {"total_usage": 1000}, "system_cpu_usage": 80000},
	  "memory_stats": {"usage": 536870912, "limit": 1073741824},
	  "networks": {"eth0": {"rx_bytes": 100, "tx_bytes": 200}}
	}`
	stats, err := containerStatsFromJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parse stats: %v", err)
	}
	if stats.MemoryBytes != 536870912 {
		t.Errorf("memory bytes = %d, want 536870912", stats.MemoryBytes)
	}
	if stats.MemoryLimitBytes != 1073741824 {
		t.Errorf("memory limit = %d, want 1073741824", stats.MemoryLimitBytes)
	}
	if stats.NetworkRxBytes != 100 || stats.NetworkTxBytes != 200 {
		t.Errorf("network = %d/%d, want 100/200", stats.NetworkRxBytes, stats.NetworkTxBytes)
	}
	// CPU% = (Δtotal/Δsystem)*onlineCPUs*100 = (1000/20000)*4*100 = 20
	if stats.CPUPercent < 19.9 || stats.CPUPercent > 20.1 {
		t.Errorf("cpu percent = %v, want ~20", stats.CPUPercent)
	}
}

func TestParseContainerStatsZeroDelta(t *testing.T) {
	// precpu 为零（首次采样）时 CPU% 应为 0，不 panic。
	raw := `{"cpu_stats": {"cpu_usage": {"total_usage": 100}, "system_cpu_usage": 100, "online_cpus": 2}, "precpu_stats": {"cpu_usage": {"total_usage": 0}, "system_cpu_usage": 0}}`
	stats, err := containerStatsFromJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parse stats: %v", err)
	}
	if stats.CPUPercent != 0 {
		t.Errorf("cpu percent = %v, want 0 on first sample", stats.CPUPercent)
	}
}

func TestParseContainerStatsMalformed(t *testing.T) {
	if _, err := containerStatsFromJSON([]byte("{not json")); err == nil {
		t.Fatal("expected error for malformed stats json")
	}
	if _, err := containerStatsFromJSON([]byte(strings.Repeat(" ", 0))); err == nil {
		t.Fatal("expected error for empty stats json")
	}
}

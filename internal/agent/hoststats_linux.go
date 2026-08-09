//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func collectHostStats(dataRoot string) hostStats {
	var stats hostStats

	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					stats.MemoryTotalBytes = v * 1024
				}
			case "MemAvailable:":
				if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					stats.MemoryAvailableBytes = v * 1024
				}
			}
		}
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(dataRoot, &stat); err == nil {
		stats.DiskTotalBytes = int64(stat.Blocks) * int64(stat.Bsize)
		stats.DiskAvailableBytes = int64(stat.Bavail) * int64(stat.Bsize)
	}
	return stats
}

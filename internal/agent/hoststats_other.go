//go:build !windows && !linux

package agent

func collectHostStats(dataRoot string) hostStats {
	return hostStats{}
}

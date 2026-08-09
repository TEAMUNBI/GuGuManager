package agent

// hostStats 描述 Agent 所在主机的资源总量与可用量。
// 由按平台的实现填充；无法探测时返回零值。
type hostStats struct {
	MemoryTotalBytes     int64
	MemoryAvailableBytes int64
	DiskTotalBytes       int64
	DiskAvailableBytes   int64
}

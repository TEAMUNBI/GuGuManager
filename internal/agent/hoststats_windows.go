//go:build windows

package agent

import (
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx 对应 WIN32_MEMORY_STATUS_EX。Length 需在调用前填结构体大小。
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func collectHostStats(dataRoot string) hostStats {
	var stats hostStats

	var mem memoryStatusEx
	mem.Length = uint32(unsafe.Sizeof(mem))
	if result, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem))); result != 0 {
		stats.MemoryTotalBytes = int64(mem.TotalPhys)
		stats.MemoryAvailableBytes = int64(mem.AvailPhys)
	}

	root, err := filepath.Abs(dataRoot)
	if err != nil || root == "" {
		root = "."
	}
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err == nil {
		var free, total uint64
		var avail uint64
		if err := windows.GetDiskFreeSpaceEx(rootPtr, &free, &total, &avail); err == nil {
			stats.DiskTotalBytes = int64(total)
			stats.DiskAvailableBytes = int64(free)
		}
	}
	return stats
}

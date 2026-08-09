//go:build windows

package files

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func atomicRename(source string, destination string, replace bool) error {
	sourcePointer, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	flags := uintptr(moveFileWriteThrough)
	if replace {
		flags |= moveFileReplaceExisting
	}
	result, _, callErr := moveFileEx.Call(
		uintptr(unsafe.Pointer(sourcePointer)),
		uintptr(unsafe.Pointer(destinationPointer)),
		flags,
	)
	if result == 0 {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: callErr}
	}
	return nil
}

func syncDirectory(string) error {
	return nil
}

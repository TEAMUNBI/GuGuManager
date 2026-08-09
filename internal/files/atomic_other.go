//go:build !windows

package files

import (
	"io/fs"
	"os"
)

func atomicRename(source string, destination string, replace bool) error {
	if !replace {
		if _, err := os.Lstat(destination); err == nil {
			return fs.ErrExist
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(source, destination)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// fileChecksum 计算文件的 sha256 与字节大小。
func fileChecksum(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

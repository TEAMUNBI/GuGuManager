package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// randomHex 生成 n 字节的随机十六进制字符串；熵源失败时回退到纳秒时间戳。
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// parsePlayersFromRCON 从 rcon-cli list 的标准输出
// "There are X of a max of Y players online: ..." 提取在线人数与上限。
// 输出不符合预期时返回 (0, 0)，由调用方视为无玩家。
func parsePlayersFromRCON(output string) (online, max int) {
	const onlineMarker = "There are "
	idx := strings.Index(output, onlineMarker)
	if idx < 0 {
		return 0, 0
	}
	rest := output[idx+len(onlineMarker):]
	if end := strings.Index(rest, " of a max of "); end > 0 {
		online, _ = strconv.Atoi(strings.TrimSpace(rest[:end]))
		rest = rest[end+len(" of a max of "):]
	}
	if end := strings.Index(rest, " players online"); end > 0 {
		max, _ = strconv.Atoi(strings.TrimSpace(rest[:end]))
	}
	return online, max
}

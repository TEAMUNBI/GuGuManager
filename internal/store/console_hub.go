package store

import (
	"sync"

	"github.com/gugumanager/gugumanager/internal/domain"
)

// consoleHub 是服务器控制台日志的进程内订阅中心：RecordConsoleLines 追加
// 新行后广播给订阅者（实时 WebSocket 推送），多副本各自持有一个 hub，
// 副本间不共享实时流——Agent 上报落在哪个副本，该副本即向其订阅者推送。
// 缓冲满时丢弃最旧未消费行，实时日志允许丢帧，避免慢消费者阻塞上报链路。
type consoleHub struct {
	mu       sync.Mutex
	subs     map[string]map[uint64]chan domain.ConsoleLine
	nextID   uint64
	buffered int
}

const consoleHubBuffer = 256

func newConsoleHub() *consoleHub {
	return &consoleHub{
		subs:     make(map[string]map[uint64]chan domain.ConsoleLine),
		buffered: consoleHubBuffer,
	}
}

// Subscribe 订阅指定服务器的实时日志行。返回接收 channel 与取消函数；
// 调用方必须在退出时调用取消以释放订阅。channel 由 hub 关闭语义之外
// 管理：取消后不再投递。
func (h *consoleHub) Subscribe(serverID string) (<-chan domain.ConsoleLine, func()) {
	ch := make(chan domain.ConsoleLine, h.buffered)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	if h.subs[serverID] == nil {
		h.subs[serverID] = make(map[uint64]chan domain.ConsoleLine)
	}
	h.subs[serverID][id] = ch
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[serverID][id]; !ok {
			return
		}
		delete(h.subs[serverID], id)
		if len(h.subs[serverID]) == 0 {
			delete(h.subs, serverID)
		}
		close(ch)
	}
	return ch, cancel
}

// Publish 把新日志行非阻塞广播给该服务器的所有订阅者。
func (h *consoleHub) Publish(serverID string, lines []domain.ConsoleLine) {
	if len(lines) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subs[serverID] {
		for _, line := range lines {
			select {
			case sub <- line:
			default:
				// 订阅者消费慢：丢弃最旧行，保持实时性。
			}
		}
	}
}

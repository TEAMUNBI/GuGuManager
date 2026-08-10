package store

import (
	"testing"
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
)

func TestConsoleHubBroadcastAndCancel(t *testing.T) {
	hub := newConsoleHub()
	serverID := "server-1"
	ch, cancel := hub.Subscribe(serverID)
	defer cancel()

	line := domain.ConsoleLine{Sequence: 1, Message: "hello"}
	hub.Publish(serverID, []domain.ConsoleLine{line})
	select {
	case got := <-ch:
		if got.Message != "hello" || got.Sequence != 1 {
			t.Fatalf("broadcast line mismatch: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast")
	}

	// 取消后不再投递（取消幂等，defer 再调用安全）；channel 关闭，读取返回
	// ok=false 表示流结束。
	cancel()
	hub.Publish(serverID, []domain.ConsoleLine{{Sequence: 2, Message: "after cancel"}})
	select {
	case got, ok := <-ch:
		if ok {
			t.Fatalf("received line after cancel: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected channel to be closed after cancel")
	}
}

func TestConsoleHubIsolatesServers(t *testing.T) {
	hub := newConsoleHub()
	chA, cancelA := hub.Subscribe("server-a")
	defer cancelA()
	chB, cancelB := hub.Subscribe("server-b")
	defer cancelB()

	hub.Publish("server-a", []domain.ConsoleLine{{Sequence: 1, Message: "for-a"}})

	select {
	case got := <-chA:
		if got.Message != "for-a" {
			t.Fatalf("server-a line mismatch: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("server-a subscriber got nothing")
	}
	select {
	case got := <-chB:
		t.Fatalf("server-b received unrelated line: %+v", got)
	default:
	}
}

func TestConsoleHubSlowSubscriberDropsInsteadOfBlocking(t *testing.T) {
	hub := newConsoleHub()
	ch, cancel := hub.Subscribe("server-1")
	defer cancel()

	// 填满缓冲后再发布，慢消费者不应阻塞发布者。
	lines := make([]domain.ConsoleLine, 0, hub.buffered*2)
	for i := 0; i < hub.buffered*2; i++ {
		lines = append(lines, domain.ConsoleLine{Sequence: int64(i + 1), Message: "line"})
	}
	done := make(chan struct{})
	go func() {
		hub.Publish("server-1", lines)
		close(done)
	}()
	select {
	case <-done:
		// 发布完成（未阻塞）即通过；订阅者只收到缓冲容量内的行。
	case <-time.After(time.Second):
		t.Fatal("publish blocked on slow subscriber")
	}
	_ = ch
}

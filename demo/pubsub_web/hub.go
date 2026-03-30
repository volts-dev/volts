// Package pubsub_web — SSE 客户端广播中心
package pubsub_web

import (
	"sync"
)

// SseEvent 是推送给浏览器的事件结构
type SseEvent struct {
	ID    int    `json:"id"`
	Topic string `json:"topic"`
	Body  string `json:"body"`
	Time  string `json:"time"`
}

// Hub 维护所有 SSE 长连接客户端，并负责广播消息。
// broker subscriber → Hub.Broadcast() → 每个 client channel → SSE handler
type Hub struct {
	mu      sync.RWMutex
	clients map[uint64]chan SseEvent
	nextID  uint64
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[uint64]chan SseEvent),
	}
}

// Register 注册一个新的 SSE 客户端，返回消息 channel 和注销函数
func (h *Hub) Register() (uint64, <-chan SseEvent, func()) {
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	ch := make(chan SseEvent, 16)
	h.clients[id] = ch
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		delete(h.clients, id)
		close(ch)
		h.mu.Unlock()
	}
	return id, ch, cancel
}

// Broadcast 向所有在线客户端推送事件（跳过慢客户端）
func (h *Hub) Broadcast(evt SseEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.clients {
		select {
		case ch <- evt:
		default: // 客户端处理慢时跳过，防止阻塞 broker
		}
	}
}

// Count 返回当前连接数
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

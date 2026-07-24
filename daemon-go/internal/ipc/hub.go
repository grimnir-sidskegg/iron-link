package ipc

import (
	"sync"

	"ironlink/daemon/internal/api"
)

// Hub fans daemon-side events out to all active Subscribe connections.
//
// A subscriber gets a buffered channel; if it lags past the buffer the Hub
// DROPS events for that subscriber rather than blocking the daemon (the stream
// is best-effort and lossy under load by design). The
// snapshot func supplies the first `State` frame every subscriber receives.
type Hub struct {
	mu       sync.Mutex
	subs     map[int]chan api.Event
	next     int
	snapshot func() api.Event
}

// NewHub builds a Hub. snapshot returns the current session `State` event sent
// as the first frame on every new subscription.
func NewHub(snapshot func() api.Event) *Hub {
	return &Hub{subs: make(map[int]chan api.Event), snapshot: snapshot}
}

// subscribe registers a new subscriber and returns its channel plus a cancel
// func that unregisters and drains it. Internal: the server calls this.
func (h *Hub) subscribe() (<-chan api.Event, func()) {
	const buffer = 64
	ch := make(chan api.Event, buffer)
	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = ch
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
		h.mu.Unlock()
	}
}

// Snapshot returns the current `State` frame (the first frame a subscriber
// receives).
func (h *Hub) Snapshot() api.Event { return h.snapshot() }

// Broadcast delivers ev to every subscriber, dropping it for any subscriber
// whose buffer is full (best-effort).
func (h *Hub) Broadcast(ev api.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default: // subscriber is lagging — drop rather than stall the daemon
		}
	}
}

// SubscriberCount reports the number of active subscribers (for tests / metrics).
func (h *Hub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

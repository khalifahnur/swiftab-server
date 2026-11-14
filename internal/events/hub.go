// Package events fans live restaurant activity out to connected dashboards.
//
// The hub lives in process memory, so it only reaches clients connected to
// the same server instance. Dashboards also poll, which covers a deployment
// that runs more than one instance.
package events

import "sync"

// Event is one change a dashboard may want to refresh for.
type Event struct {
	// Type is the stream the change belongs to: "order" or "reservation".
	Type string `json:"type"`
	// Action says what happened, for example "created" or "status".
	Action string `json:"action"`
	ID     string `json:"id,omitempty"`
	// Table and Status let the dashboard describe the change without refetching.
	Table  string `json:"table,omitempty"`
	Status string `json:"status,omitempty"`
}

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan Event]struct{})}
}

// Subscribe registers a listener for one restaurant. Call the returned
// function exactly once to stop listening.
func (h *Hub) Subscribe(restaurantID string) (<-chan Event, func()) {
	ch := make(chan Event, 16)

	h.mu.Lock()
	if h.subs[restaurantID] == nil {
		h.subs[restaurantID] = make(map[chan Event]struct{})
	}
	h.subs[restaurantID][ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs[restaurantID], ch)
			if len(h.subs[restaurantID]) == 0 {
				delete(h.subs, restaurantID)
			}
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// Publish notifies every listener of a restaurant. A listener that is not
// keeping up misses the event rather than blocking the request that caused it;
// the dashboard's polling picks the change up instead.
func (h *Hub) Publish(restaurantID string, e Event) {
	if h == nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[restaurantID] {
		select {
		case ch <- e:
		default:
		}
	}
}

// Listeners reports how many dashboards are connected for a restaurant.
func (h *Hub) Listeners(restaurantID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[restaurantID])
}

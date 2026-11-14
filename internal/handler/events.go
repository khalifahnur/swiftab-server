package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"swiftab/server/internal/util"
)

// heartbeatInterval keeps the connection alive through proxies that close
// idle streams.
const heartbeatInterval = 25 * time.Second

// StreamEvents is a Server-Sent Events stream of the restaurant's live
// activity. The dashboard listens to it and refreshes the affected data when
// an order or reservation changes.
func (h *AdminHandler) StreamEvents(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Set up your restaurant first")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok || h.Hub == nil {
		util.JsonError(w, http.StatusNotImplemented, "Live updates are not available")
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	// Tell nginx-style proxies not to buffer the stream.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// The browser reconnects by itself after this delay if the stream drops.
	fmt.Fprint(w, "retry: 5000\n\n")
	fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	flusher.Flush()

	stream, cancel := h.Hub.Subscribe(restaurantID.Hex())
	defer cancel()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event := <-stream:
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

package events

import (
	"testing"
	"time"
)

func TestPublishReachesOnlyTheRestaurantsListeners(t *testing.T) {
	h := NewHub()
	a, cancelA := h.Subscribe("a")
	b, cancelB := h.Subscribe("b")
	defer cancelA()
	defer cancelB()

	h.Publish("a", Event{Type: "order", Action: "created", ID: "1"})

	select {
	case e := <-a:
		if e.ID != "1" {
			t.Fatalf("got event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("listener for restaurant a received nothing")
	}

	select {
	case e := <-b:
		t.Fatalf("listener for restaurant b received %+v", e)
	default:
	}
}

func TestCancelStopsDeliveryAndIsIdempotent(t *testing.T) {
	h := NewHub()
	_, cancel := h.Subscribe("a")
	if h.Listeners("a") != 1 {
		t.Fatalf("listeners = %d, want 1", h.Listeners("a"))
	}
	cancel()
	cancel()
	if h.Listeners("a") != 0 {
		t.Fatalf("listeners after cancel = %d, want 0", h.Listeners("a"))
	}
	h.Publish("a", Event{Type: "order"}) // must not panic with no listeners
}

func TestSlowListenerDoesNotBlockPublish(t *testing.T) {
	h := NewHub()
	_, cancel := h.Subscribe("a")
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ { // far more than the channel buffer
			h.Publish("a", Event{Type: "order"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a listener that was not reading")
	}
}

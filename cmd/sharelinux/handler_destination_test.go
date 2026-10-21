package main

import "testing"

func TestAbandonedDestinationIsNotKept(t *testing.T) {
	h := newHandler(nil, nil)

	h.abandonDestination(7)
	h.DropDestinationReceived(7, "/late")
	if len(h.destinations) != 0 {
		t.Fatalf("a destination that arrived after the cancel was kept: %+v", h.destinations)
	}
	if len(h.abandoned) != 0 {
		t.Fatalf("the abandoned mark stays after its destination came: %v", h.abandoned)
	}

	h.DropDestinationReceived(8, "/early")
	h.abandonDestination(8)
	if len(h.destinations) != 0 {
		t.Fatalf("a destination that was already here was kept: %+v", h.destinations)
	}
	h.DropDestinationReceived(8, "/again")
	if path, found := h.takeDestination(8); !found || path != "/again" {
		t.Fatalf("a later drop with the same id lost its destination: %q %v", path, found)
	}
}

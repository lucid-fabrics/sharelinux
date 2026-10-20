package main

import (
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestPointerGate(t *testing.T) {
	var gate pointerGate
	if gate.Blocked() {
		t.Fatal("open at start")
	}
	gate.Begin()
	if !gate.Blocked() {
		t.Fatal("live pointer events are held back during a drop")
	}
	gate.End()
	gate.End()
	if gate.Blocked() {
		t.Fatal("End is idempotent and reopens the gate")
	}
}

func TestDropGestureOrder(t *testing.T) {
	type press struct {
		x, y    int
		buttons uint8
	}
	var sent []press
	var waits []time.Duration
	dropGesture(func(x, y int, buttons uint8) error {
		sent = append(sent, press{x, y, buttons})
		return nil
	}, func(d time.Duration) { waits = append(waits, d) }, 10, 20)
	want := []press{{10, 20, rfb.ButtonLeft}, {11, 21, rfb.ButtonLeft}, {11, 21, 0}}
	if len(sent) != len(want) {
		t.Fatalf("%v", sent)
	}
	for i := range want {
		if sent[i] != want[i] {
			t.Fatalf("%d: %v", i, sent)
		}
	}
	if len(waits) != 2 {
		t.Fatalf("%v", waits)
	}
}

// The Mac picks the folder from where its pointer is, so a hover must reach it
// before the drop plays its presses, and never while a drop is playing them.
func TestHoverReachesTheMacBeforeTheDrop(t *testing.T) {
	type event struct {
		x, y    int
		buttons uint8
	}
	var sent []event
	send := func(x, y int, buttons uint8) error {
		sent = append(sent, event{x, y, buttons})
		return nil
	}
	var gate pointerGate
	hoverPointer(&gate, send, 5, 6)
	hoverPointer(&gate, send, 10, 20)
	gate.Begin()
	hoverPointer(&gate, send, 99, 99)
	dropGesture(send, func(time.Duration) {}, 10, 20)
	gate.End()
	want := []event{{5, 6, 0}, {10, 20, 0}, {10, 20, rfb.ButtonLeft}, {11, 21, rfb.ButtonLeft}, {11, 21, 0}}
	if len(sent) != len(want) {
		t.Fatalf("%v", sent)
	}
	for i := range want {
		if sent[i] != want[i] {
			t.Fatalf("%d: %v", i, sent)
		}
	}
}

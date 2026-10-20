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

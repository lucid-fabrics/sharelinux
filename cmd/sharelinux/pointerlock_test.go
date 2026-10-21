package main

import (
	"testing"
	"time"
)

func TestChooseLockBackend(t *testing.T) {
	for _, test := range []struct {
		name string
		caps lockCaps
		want lockBackend
	}{
		{"wayland with both protocols", lockCaps{wayland: true, constraints: true, relativeMgr: true}, lockWayland},
		{"wayland without constraints", lockCaps{wayland: true, relativeMgr: true}, lockNone},
		{"wayland without relative pointer", lockCaps{wayland: true, constraints: true}, lockNone},
		{"wayland with neither", lockCaps{wayland: true}, lockNone},
		{"wayland never falls back to x11 bits", lockCaps{wayland: true, x11: true, xfixes: true, xinput2: true}, lockNone},
		{"x11 with raw motion", lockCaps{x11: true, xfixes: true, xinput2: true}, lockX11},
		{"x11 raw motion without barriers", lockCaps{x11: true, xinput2: true}, lockX11},
		{"x11 barriers only", lockCaps{x11: true, xfixes: true}, lockConfine},
		{"x11 with nothing", lockCaps{x11: true}, lockNone},
		{"no display", lockCaps{}, lockNone},
	} {
		if got := chooseLockBackend(test.caps); got != test.want {
			t.Errorf("%s: got %d, want %d", test.name, got, test.want)
		}
	}
}

func TestLockBackendCapabilities(t *testing.T) {
	for _, test := range []struct {
		backend  lockBackend
		relative bool
		holds    bool
	}{
		{lockNone, false, false},
		{lockConfine, false, true},
		{lockX11, true, true},
		{lockWayland, true, true},
	} {
		if test.backend.relative() != test.relative || test.backend.holds() != test.holds {
			t.Errorf("backend %d: relative %v holds %v", test.backend, test.backend.relative(), test.backend.holds())
		}
	}
}

func TestRelativePointerAccumulatesFractions(t *testing.T) {
	v := fitViewport(1000, 1000, 500, 500) // 2 window pixels per framebuffer pixel
	var p relativePointer
	p.set(100, 100)
	x, y := p.move(1, 0, v, 500, 500)
	if x != 100 || y != 100 {
		t.Fatalf("half a pixel is kept, not sent: %d %d", x, y)
	}
	x, _ = p.move(1, 0, v, 500, 500)
	if x != 101 {
		t.Fatalf("two halves make a pixel: %d", x)
	}
	for range 10 {
		x, y = p.move(-0.5, 0.5, v, 500, 500)
	}
	if x != 98 || y != 102 {
		t.Fatalf("small steps add up: %d %d", x, y)
	}
}

func TestRelativePointerHasNoLocalEdgeStop(t *testing.T) {
	// The window is a small part of the Mac's screen, so a long way across the
	// desk is a long way across the Mac and not a stop at the window's edge.
	v := fitViewport(400, 300, 4000, 3000)
	var p relativePointer
	p.set(10, 10)
	x, y := p.move(300, 200, v, 4000, 3000)
	if x != 3010 || y != 2010 {
		t.Fatalf("%d %d", x, y)
	}
	x, y = p.move(5000, 5000, v, 4000, 3000)
	if x != 3999 || y != 2999 {
		t.Fatalf("the Mac's screen edge clamps: %d %d", x, y)
	}
	x, y = p.move(-1e6, -1e6, v, 4000, 3000)
	if x != 0 || y != 0 {
		t.Fatalf("%d %d", x, y)
	}
}

var lockEpoch = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func TestResyncIgnoresEchoOfOwnMotion(t *testing.T) {
	var p relativePointer
	p.set(100, 100)
	p.X, p.Y = 130, 100
	for i := range 4 {
		p.sent(110+i*10, 100, lockEpoch.Add(time.Duration(i)*8*time.Millisecond))
	}
	// The Mac reports an older position than the pointer already moved to.
	if p.resync(120, 100, lockEpoch.Add(40*time.Millisecond)) {
		t.Fatal("an echo is not a warp")
	}
	if p.X != 130 || p.Y != 100 {
		t.Fatalf("the total must not jump back: %v %v", p.X, p.Y)
	}
	// Echoes up to that one are used; the newer ones still wait.
	if len(p.flight) != 2 {
		t.Fatalf("flight %d", len(p.flight))
	}
	if p.resync(132, 101, lockEpoch.Add(50*time.Millisecond)) {
		t.Fatal("a rounded echo within the tolerance is not a warp")
	}
}

func TestResyncFollowsAWarp(t *testing.T) {
	var p relativePointer
	p.set(100, 100)
	p.sent(101, 100, lockEpoch)
	if !p.resync(900, 700, lockEpoch.Add(10*time.Millisecond)) {
		t.Fatal("a position nobody sent is a warp")
	}
	if p.X != 900 || p.Y != 700 || len(p.flight) != 0 {
		t.Fatalf("%v %v %d", p.X, p.Y, len(p.flight))
	}
	v := fitViewport(1000, 1000, 1000, 1000)
	if x, y := p.move(5, 5, v, 1000, 1000); x != 905 || y != 705 {
		t.Fatalf("the next motion starts from the warp: %d %d", x, y)
	}
}

func TestResyncOutsideToleranceIsAWarp(t *testing.T) {
	var p relativePointer
	p.set(100, 100)
	p.sent(200, 200, lockEpoch)
	if !p.resync(200+echoTolerance+1, 200, lockEpoch.Add(time.Millisecond)) {
		t.Fatal("beyond the tolerance it is not the echo")
	}
}

func TestResyncSameSpotWithNothingInFlightMovesNothing(t *testing.T) {
	var p relativePointer
	p.set(50, 60)
	if p.resync(50, 60, lockEpoch) {
		t.Fatal("the Mac agrees with the total")
	}
}

func TestResyncExpiresOldSentPositions(t *testing.T) {
	var p relativePointer
	p.set(10, 10)
	p.sent(500, 500, lockEpoch)
	if !p.resync(500, 500, lockEpoch.Add(echoLife+time.Millisecond)) {
		t.Fatal("an echo that never came is stale: the same spot later is a warp")
	}
	if p.X != 500 || p.Y != 500 {
		t.Fatalf("%v %v", p.X, p.Y)
	}
}

func TestSentIsBounded(t *testing.T) {
	var p relativePointer
	for i := range echoSlots * 3 {
		p.sent(i, 0, lockEpoch)
	}
	if len(p.flight) != echoSlots {
		t.Fatalf("flight %d", len(p.flight))
	}
	if p.flight[0].x != echoSlots*2 {
		t.Fatalf("the oldest go first: %d", p.flight[0].x)
	}
}

func TestLockStart(t *testing.T) {
	for _, test := range []struct {
		name         string
		reported     bool
		rx, ry       int
		newer, sent  bool
		sx, sy       int
		wantX, wantY int
	}{
		{"nothing known starts in the middle, not the corner", false, 0, 0, false, false, 0, 0, 1000, 500},
		{"a report with no send is used", true, 300, 200, true, false, 0, 0, 300, 200},
		{"our last send when the Mac never reported", false, 0, 0, false, true, 40, 50, 40, 50},
		{"a warp after our last send wins", true, 700, 600, true, true, 40, 50, 700, 600},
		{"our send after the last report wins", true, 700, 600, false, true, 40, 50, 40, 50},
		{"a reported origin is a position, not unknown", true, 0, 0, true, false, 0, 0, 0, 0},
	} {
		x, y := lockStart(test.reported, test.rx, test.ry, test.newer, test.sent, test.sx, test.sy, 2000, 1000)
		if x != test.wantX || y != test.wantY {
			t.Errorf("%s: got %d,%d want %d,%d", test.name, x, y, test.wantX, test.wantY)
		}
	}
	if x, y := lockStart(false, 0, 0, false, false, 0, 0, 0, 0); x != 0 || y != 0 {
		t.Errorf("an empty picture: %d,%d", x, y)
	}
}

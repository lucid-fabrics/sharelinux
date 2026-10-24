package main

import (
	"math"
	"testing"
	"time"
)

// cycle plays one leave and enter of Exclusive on X11: the unlock warps the
// pointer to the Mac's cursor on a whole device pixel (round, the way x11.c
// does), the display server reports that spot as a motion, and the session
// either sends it to the Mac (without the echo guard) or not. The decision is
// the one onMotion makes.
func cycle(port viewport, frameW, frameH int, scale float64, echo *warpEcho, x, y int, now time.Time) (int, int) {
	wx, wy := port.toWindow(float64(x)+0.5, float64(y)+0.5, frameW, frameH)
	echo.arm(float64(wx), float64(wy), now)
	px := math.Round(float64(wx)*scale) / scale
	py := math.Round(float64(wy)*scale) / scale
	if !motionSends(echo, px, py, now, true, false, true) {
		return x, y
	}
	fx, fy, _ := port.toFramebuffer(float32(px), float32(py), frameW, frameH)
	return fx, fy
}

func TestLockCycleKeepsMacPointer(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name         string
		winW, winH   int
		frameW       int
		frameH       int
		scale        float64
		startX, endY int
	}{
		{"retina mac into 1280x727", 1280, 727, 3456, 1962, 1, 3261, 2077},
		{"scale two", 1280, 727, 3456, 1962, 2, 3261, 2077},
		{"fractional fit", 1013, 641, 2560, 1440, 1, 1777, 913},
		{"fractional scale 1.5", 1013, 641, 2560, 1440, 1.5, 12, 1439},
		{"same size", 800, 600, 800, 600, 1, 400, 300},
	} {
		port := fitViewport(test.winW, test.winH, test.frameW, test.frameH)
		x, y := test.startX, test.endY
		var echo warpEcho
		for i := 0; i < 25; i++ {
			x, y = cycle(port, test.frameW, test.frameH, test.scale, &echo, x, y, now)
		}
		if x != test.startX || y != test.endY {
			t.Errorf("%s: Mac pointer drifted to %d,%d from %d,%d", test.name, x, y, test.startX, test.endY)
		}
	}
}

func TestWarpEchoOnlyCoversTheWarp(t *testing.T) {
	now := time.Now()
	var echo warpEcho
	if echo.swallows(10, 10, now) {
		t.Fatal("nothing armed")
	}
	echo.arm(100, 50.5, now)
	if !echo.swallows(100, 50, now) || !echo.swallows(100.4, 51, now) {
		t.Fatal("the warp's own spot is swallowed")
	}
	if echo.swallows(103, 50, now) {
		t.Fatal("a real move is not swallowed")
	}
	if echo.swallows(100, 50, now) {
		t.Fatal("a real move ends the echo")
	}
	echo.arm(100, 50, now)
	if echo.swallows(100, 50, now.Add(2*time.Second)) {
		t.Fatal("a stale warp is not swallowed")
	}
	echo.arm(1, 1, now)
	echo.disarm()
	if echo.swallows(1, 1, now) {
		t.Fatal("disarmed")
	}
}

// Without the guard the truncated or rounded pixel does move the Mac: the test
// above would fail on the old behaviour.
func TestLockCycleWithoutGuardDrifts(t *testing.T) {
	port := fitViewport(1280, 727, 3456, 1962)
	x, y := 3261, 2077
	for i := 0; i < 4; i++ {
		wx, wy := port.toWindow(float64(x), float64(y), 3456, 1962)
		x, y, _ = port.toFramebuffer(float32(int(wx)), float32(int(wy)), 3456, 1962)
	}
	if y == 2077 && x == 3261 {
		t.Fatal("expected the old mapping to drift")
	}
}

// Leaving Exclusive and clicking without moving: the pointer still sits on the
// rounded echo spot, and the click must go to the Mac's exact position.
func TestClickOnPendingEchoUsesExactMacPosition(t *testing.T) {
	now := time.Now()
	port := fitViewport(1280, 727, 3456, 1962)
	macX, macY := 3261, 2077
	wx, wy := port.toWindow(float64(macX)+0.5, float64(macY)+0.5, 3456, 1962)
	var echo warpEcho
	echo.arm(float64(wx), float64(wy), now)
	mx, my, inside := port.toFramebuffer(float32(wx), float32(wy), 3456, 1962)
	for _, exclusive := range []bool{true, false} {
		fx, fy, ok := clickTarget(&echo, float64(wx), float64(wy), now, exclusive, false, macX, macY, mx, my, inside)
		if !ok || fx != macX || fy != macY {
			t.Errorf("exclusive %v: click went to %d,%d (%v), want %d,%d", exclusive, fx, fy, ok, macX, macY)
		}
	}
	// A real move ends the echo and the click follows the pointer again.
	fx, fy, ok := clickTarget(&echo, float64(wx)+30, float64(wy), now, false, false, macX, macY, 5, 6, true)
	if !ok || fx != 5 || fy != 6 {
		t.Errorf("after a move the click went to %d,%d", fx, fy)
	}
	// Exclusive with a held lock always uses the Mac's position.
	fx, fy, _ = clickTarget(&echo, 0, 0, now, true, true, macX, macY, 5, 6, true)
	if fx != macX || fy != macY {
		t.Errorf("held lock click went to %d,%d", fx, fy)
	}
}

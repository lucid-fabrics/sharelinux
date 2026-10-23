package main

import (
	"math"
	"testing"
	"time"
)

// cycle plays one leave and enter of Exclusive on X11: the unlock warps the
// pointer to the Mac's cursor on a whole device pixel (round, the way x11.c
// does), the display server reports that spot as a motion, and the session
// either sends it to the Mac (without the echo guard) or not.
func cycle(port viewport, frameW, frameH int, scale float64, echo *warpEcho, x, y int, now time.Time) (int, int) {
	wx, wy := port.toWindow(float64(x)+0.5, float64(y)+0.5, frameW, frameH)
	echo.arm(float64(wx), float64(wy), now)
	px := math.Round(float64(wx)*scale) / scale
	py := math.Round(float64(wy)*scale) / scale
	if echo.swallows(px, py, now) {
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

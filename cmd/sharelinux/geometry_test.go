package main

import (
	"math"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 0.01 }

func TestFitViewportLetterboxes(t *testing.T) {
	v := fitViewport(1000, 500, 1000, 1000)
	if !near(v.W, 500) || !near(v.H, 500) || !near(v.X, 250) || !near(v.Y, 0) {
		t.Fatalf("%+v", v)
	}
	v = fitViewport(500, 1000, 1000, 500)
	if !near(v.W, 500) || !near(v.H, 250) || !near(v.X, 0) || !near(v.Y, 375) {
		t.Fatalf("%+v", v)
	}
	v = fitViewport(1600, 900, 800, 450)
	if !near(v.W, 1600) || !near(v.H, 900) || !near(v.X, 0) || !near(v.Y, 0) {
		t.Fatalf("%+v", v)
	}
}

func TestFitViewportEmpty(t *testing.T) {
	if !fitViewport(0, 10, 10, 10).empty() || !fitViewport(10, 10, 0, 10).empty() {
		t.Fatal("degenerate sizes give an empty viewport")
	}
}

func TestToFramebuffer(t *testing.T) {
	v := fitViewport(1000, 500, 1000, 1000) // 500x500 at x 250
	x, y, inside := v.toFramebuffer(250, 0, 1000, 1000)
	if x != 0 || y != 0 || !inside {
		t.Fatal(x, y, inside)
	}
	x, y, inside = v.toFramebuffer(500, 250, 1000, 1000)
	if x != 500 || y != 500 || !inside {
		t.Fatal(x, y, inside)
	}
	x, _, inside = v.toFramebuffer(100, 250, 1000, 1000)
	if x != 0 || inside {
		t.Fatal("left of the picture clamps to column 0 and is outside", x, inside)
	}
	x, y, inside = v.toFramebuffer(900, 900, 1000, 1000)
	if x != 999 || y != 999 || inside {
		t.Fatal(x, y, inside)
	}
}

func TestToWindowInverse(t *testing.T) {
	v := fitViewport(800, 600, 400, 300)
	wx, wy := v.toWindow(100, 50, 400, 300)
	x, y, inside := v.toFramebuffer(wx+0.1, wy+0.1, 400, 300)
	if x != 100 || y != 50 || !inside {
		t.Fatal(x, y, inside)
	}
	if !near(v.scale(400), 2) {
		t.Fatal(v.scale(400))
	}
}

func TestRelativePointerClamps(t *testing.T) {
	v := fitViewport(1000, 1000, 500, 500) // 2 window pixels per framebuffer pixel
	var p relativePointer
	p.set(10, 10)
	x, y := p.move(20, 40, v, 500, 500)
	if x != 20 || y != 30 {
		t.Fatal(x, y)
	}
	x, y = p.move(-1e6, 1e6, v, 500, 500)
	if x != 0 || y != 499 {
		t.Fatal(x, y)
	}
}

func TestScrollPointsPulsePerNotch(t *testing.T) {
	standard := rfb.ServerInfo{Dialect: rfb.DialectStandard}
	// SendScroll turns points into pulses by whole multiples of StandardPointsPerPulse.
	pulses := func(points float64) int { return int(points / rfb.StandardPointsPerPulse) }
	for _, notch := range []struct {
		x, y         float32
		wantX, wantY int
	}{
		{0, 1, 0, 1},   // up
		{0, -1, 0, -1}, // down
		{1, 0, -1, 0},  // right: SendScroll counts left as positive
		{-1, 0, 1, 0},  // left
	} {
		x, y := scrollPoints(standard, notch.x, notch.y)
		if pulses(x) != notch.wantX || pulses(y) != notch.wantY {
			t.Fatalf("notch (%v, %v) = pulses (%d, %d), want (%d, %d)", notch.x, notch.y, pulses(x), pulses(y), notch.wantX, notch.wantY)
		}
	}
	var total float64
	for range 10 {
		_, y := scrollPoints(standard, 0, 1)
		total += y
	}
	if pulses(total) != 10 {
		t.Fatalf("ten notches = %d pulses", pulses(total))
	}
}

func TestScrollPointsKeepsSmoothScrolling(t *testing.T) {
	extended := rfb.ServerInfo{Dialect: rfb.DialectStandard, Extended: true}
	extended.Commands[scrollWheelMessage/8] = 0x80 >> (scrollWheelMessage % 8)
	for name, info := range map[string]rfb.ServerInfo{
		"apple":             {Dialect: rfb.DialectApple},
		"scroll-wheel-seen": extended,
	} {
		x, y := scrollPoints(info, 0.5, -2)
		if x != 8 || y != -32 {
			t.Fatal(name, x, y)
		}
	}
}

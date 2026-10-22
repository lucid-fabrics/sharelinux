//go:build linux

package main

import (
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The picture's place in root pixels, from the window's origin, the widget's
// bounds, the surface transform and the scale.
func TestX11PictureRectScaleAndOffset(t *testing.T) {
	cases := []struct {
		name                      string
		originX, originY          int
		bx, by, bw, bh, ox, oy, k float64
		want                      pixelRect
	}{
		{"plain window", 100, 50, 0, 0, 800, 600, 0, 0, 1, pixelRect{100, 50, 800, 600}},
		{"window offset moves it", 300, 200, 0, 0, 800, 600, 0, 0, 1, pixelRect{300, 200, 800, 600}},
		{"HiDPI scale 2 doubles the size and the inset", 100, 50, 10, 20, 400, 300, 0, 0, 2, pixelRect{120, 90, 800, 600}},
		{"surface transform counts, scaled", 100, 50, 0, 0, 400, 300, 5, 7, 2, pixelRect{110, 64, 800, 600}},
		{"letterbox picture inside a wider window", 0, 0, 160, 0, 960, 540, 0, 0, 1, pixelRect{160, 0, 960, 540}},
		{"letterbox at scale 2", 40, 30, 80, 45, 480, 270, 0, 0, 2, pixelRect{200, 120, 960, 540}},
		{"fractions truncate", 10, 10, 0.9, 0.9, 99.9, 49.9, 0, 0, 1, pixelRect{10, 10, 99, 49}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := x11PictureRect(c.originX, c.originY, c.bx, c.by, c.bw, c.bh, c.ox, c.oy, c.k)
			if got != c.want {
				t.Fatalf("picture = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A picture partly off the screen is warped to the middle of what shows of it,
// on the monitor that holds the window; one that does not show has no target.
func TestX11VisibleMiddleStaysOnTheMonitor(t *testing.T) {
	monitor := pixelRect{0, 0, 1920, 1080}
	cases := []struct {
		name    string
		picture pixelRect
		monitor pixelRect
		x, y    int
		ok      bool
	}{
		{"fully visible", pixelRect{100, 100, 800, 600}, monitor, 500, 400, true},
		{"hangs off the right edge", pixelRect{1520, 100, 800, 600}, monitor, 1720, 400, true},
		{"hangs off the left edge", pixelRect{-400, 100, 800, 600}, monitor, 200, 400, true},
		{"hangs off the top and bottom", pixelRect{100, -200, 800, 1600}, monitor, 500, 540, true},
		{"second monitor holds it", pixelRect{2000, 100, 800, 600}, pixelRect{1920, 0, 1920, 1080}, 2400, 400, true},
		{"cut by the second monitor's left edge", pixelRect{1700, 100, 800, 600}, pixelRect{1920, 0, 1920, 1080}, 2210, 400, true},
		{"no part shows", pixelRect{-900, 100, 800, 600}, monitor, 0, 0, false},
		{"only an edge touches", pixelRect{1920, 100, 800, 600}, monitor, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, y, ok := x11VisibleMiddle(c.picture, c.monitor)
			if ok != c.ok || (ok && (x != c.x || y != c.y)) {
				t.Fatalf("middle = %d,%d,%v, want %d,%d,%v", x, y, ok, c.x, c.y, c.ok)
			}
		})
	}
}

// A lock whose barriers could not be rebuilt falls back as one that failed to
// start does: not held, not on, one notice per stretch of Exclusive mode.
func TestPointerLockLoseFallsBack(t *testing.T) {
	lock := pointerLock{}
	lock.request(true)
	if !lock.lose() {
		t.Fatal("the first loss must tell the user")
	}
	if lock.on || lock.held || lock.idle {
		t.Fatalf("lost lock left on=%v held=%v idle=%v", lock.on, lock.held, lock.idle)
	}
	lock.request(true)
	if lock.lose() {
		t.Fatal("a second loss in the same stretch must stay quiet")
	}
	lock.leaveExclusive()
	lock.request(true)
	if !lock.lose() {
		t.Fatal("leaving Exclusive mode must make the next loss worth a notice")
	}
}

// x11Window opens a window with a picture on the X11 display, or skips.
func x11Window(t *testing.T) (*gtk.Window, *screenView) {
	t.Helper()
	clip := builtinClip(t)
	requireDisplay(t)
	if backend := detectLockBackend(); backend != lockX11 {
		t.Skip("needs X11 with XFixes and XInput 2")
	}
	view := newScreenView()
	window := gtk.NewWindow()
	window.SetDefaultSize(400, 300)
	window.SetChild(view)
	view.resize(clip.Width, clip.Height)
	window.Present()
	pump(func() bool { return false }, 500*time.Millisecond)
	return window, view
}

// The geometry is read every frame, 125 times a second. It must ask the server
// only when the window changed or the fallback interval passed, not on every
// frame: it used to make 4 blocking requests per frame.
func TestX11SettleDoesNotAskTheServerEveryFrame(t *testing.T) {
	window, view := x11Window(t)
	t.Cleanup(window.Destroy)
	if held, requested := view.lockPointer(lockConfine); held || !requested {
		t.Skipf("no barriers (held %v requested %v)", held, requested)
	}
	defer view.unlockPointer(lockConfine, false, 0, 0)
	view.settleLock(lockConfine)
	trips, ticks := x11Cost()
	const frames = 500
	start := time.Now()
	for range frames {
		if !view.settleLock(lockConfine) {
			t.Fatal("the lock was lost on a window that is still there")
		}
	}
	trips2, ticks2 := x11Cost()
	spent := time.Since(start)
	// At most one refresh (3 requests) per fallback interval, plus one for the first.
	budget := uint64(3 * (spent/(250*time.Millisecond) + 2))
	t.Logf("%d frames in %v: %d blocking requests (4 per frame was %d)", ticks2-ticks, spent, trips2-trips, 4*frames)
	if ticks2-ticks != frames {
		t.Fatalf("%d geometry reads, want %d", ticks2-ticks, frames)
	}
	if trips2-trips > budget {
		t.Fatalf("%d blocking requests for %d frames, want at most %d", trips2-trips, frames, budget)
	}
}

// A window destroyed while the lock stands makes the next X request fail with
// BadWindow, which ends the whole program unless it is caught. The lock must
// report itself lost instead.
func TestX11SettleSurvivesADestroyedWindow(t *testing.T) {
	window, view := x11Window(t)
	if held, requested := view.lockPointer(lockConfine); held || !requested {
		window.Destroy()
		t.Skipf("no barriers (held %v requested %v)", held, requested)
	}
	if !view.settleLock(lockConfine) {
		t.Fatal("the lock was lost on a window that is still there")
	}
	vanishX11WindowForTest()
	if view.settleLock(lockConfine) {
		t.Fatal("the lock did not notice its window was gone")
	}
	view.unlockPointer(lockConfine, false, 0, 0)
	if oss := x11Confined(); oss {
		t.Fatal("barriers still stand after the unlock")
	}
}

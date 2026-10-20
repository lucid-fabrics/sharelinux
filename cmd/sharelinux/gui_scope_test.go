//go:build linux

package main

import (
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// A handler a session leaves on the window fires for every later change, and
// keeps the dead session reachable.
func TestSignalScopeReleaseStopsHandlers(t *testing.T) {
	if !gtk.InitCheck() {
		t.Skip("GTK needs a display (run under Xvfb)")
	}
	window := gtk.NewWindow()
	calls := 0
	var scope signalScope
	scope.add(window, window.NotifyProperty("title", func() { calls++ }))
	other := 0
	window.NotifyProperty("title", func() { other++ })

	window.SetTitle("first")
	if calls != 1 {
		t.Fatalf("the handler fires while the session lives: %d", calls)
	}
	scope.release()
	window.SetTitle("first")
	if calls != 1 {
		t.Fatalf("a released handler must not fire again: %d", calls)
	}
	if other != 2 {
		t.Fatalf("handlers the scope does not own stay: %d", other)
	}
	scope.release() // twice is fine
}

type heavy struct{ pixels [1 << 20]byte }

// A handler holds a Go closure and the closure holds what it captured, here a
// stand-in for the session and its framebuffer. Destroying the tree is what lets
// that be collected once the widgets are not used any more.
func TestDestroyTreeLetsCapturedStateGo(t *testing.T) {
	if !gtk.InitCheck() {
		t.Skip("GTK needs a display (run under Xvfb)")
	}
	state := &heavy{}
	watch := weak.Make(state)
	build := func() gtk.Widgetter {
		box := gtk.NewBox(gtk.OrientationVertical, 0)
		inner := gtk.NewBox(gtk.OrientationHorizontal, 0)
		button := gtk.NewButton()
		button.ConnectClicked(func() { _ = state.pixels[0]; _ = button })
		click := gtk.NewGestureClick()
		click.ConnectPressed(func(int, float64, float64) { _ = state.pixels[1]; _ = click })
		button.AddController(click)
		inner.Append(button)
		box.Append(inner)
		return box
	}
	root := build()
	destroyTree(root)
	root, state = nil, nil
	for range 5 {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	if watch.Value() != nil {
		t.Fatal("the state a handler captured is still held after the tree was destroyed")
	}
	runtime.KeepAlive(root)
}

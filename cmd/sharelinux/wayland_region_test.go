//go:build linux

package main

import (
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// A refresh of the lock region on a window without a surface gives the region back. It needs a seat with
// a pointer: on a headless compositor a VNC client that sends a pointer event gives it one.
func TestWaylandRefreshWithoutSurfaceLeaksNoRegion(t *testing.T) {
	clip := builtinClip(t)
	requireDisplay(t)
	if detectLockBackend() != lockWayland {
		t.Skip("needs Wayland with pointer constraints")
	}
	view := newScreenView()
	window := gtk.NewWindow()
	window.SetDefaultSize(400, 300)
	window.SetChild(view)
	view.resize(clip.Width, clip.Height)
	window.Present()
	t.Cleanup(window.Destroy)
	pump(func() bool { return false }, 500*time.Millisecond)
	if held, requested := view.lockPointer(lockWayland); held || !requested {
		t.Skipf("the compositor gave no lock (held %v requested %v)", held, requested)
	}
	defer view.unlockPointer(lockWayland, false, 0, 0)
	before := waylandLiveRegions()
	// The picture moves inside the window, so the region no longer fits, and then the surface goes.
	view.SetMarginStart(40)
	pump(func() bool { return false }, 300*time.Millisecond)
	window.Unrealize()
	for range 5 {
		view.refreshLock(lockWayland)
	}
	if after := waylandLiveRegions(); after != before {
		t.Fatalf("%d regions alive, %d before", after, before)
	}
}

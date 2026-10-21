//go:build linux

package main

import (
	"testing"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// pump runs the main loop until done reports true or the time is up.
func pump(done func() bool, limit time.Duration) bool {
	context := glib.MainContextDefault()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		for context.Pending() {
			context.Iteration(false)
		}
		if done() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return done()
}

// The popped signal fires when the pop starts. The page is torn down only after
// the transition, from the main loop, and only once.
func TestReleaseWaitsForThePageToLeave(t *testing.T) {
	requireDisplay(t)
	adw.Init()
	view := adw.NewNavigationView()
	window := gtk.NewWindow()
	window.SetChild(view)
	root := adw.NewNavigationPage(gtk.NewBox(gtk.OrientationVertical, 0), "root")
	page := adw.NewNavigationPage(gtk.NewBox(gtk.OrientationVertical, 0), "session")
	view.Add(root)
	window.Present()
	view.Push(page)
	released := 0
	view.ConnectPopped(func(*adw.NavigationPage) {
		releaseAfterTransition(page, func() { released++ })
		if released != 0 {
			t.Error("the page was released inside the popped signal")
		}
	})
	view.Pop()
	if released != 0 {
		t.Fatal("the page was released before the transition could end")
	}
	if !pump(func() bool { return released > 0 }, 5*time.Second) {
		t.Fatal("the page was never released")
	}
	pump(func() bool { return false }, 300*time.Millisecond)
	if released != 1 {
		t.Fatalf("released %d times, want once", released)
	}
}

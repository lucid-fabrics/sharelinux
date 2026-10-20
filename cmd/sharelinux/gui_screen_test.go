//go:build linux

package main

import (
	"image"
	"testing"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
)

func newTestScreen(t *testing.T) *screenView {
	t.Helper()
	if !gtk.InitCheck() {
		t.Skip("GTK needs a display (run under Xvfb)")
	}
	return newScreenView()
}

// The Mac shrinks its screen while a copy of the old size is still queued. The
// widget must refuse a buffer that does not fit instead of reading past it.
func TestScreenUpdateRefusesSmallerBuffer(t *testing.T) {
	view := newTestScreen(t)
	view.resize(1200, 900)
	if !view.update(bgra.New(1200, 900), image.Rect(0, 0, 1200, 900)) {
		t.Fatal("a buffer of the picture's size is accepted")
	}
	tiles := view.tileCount()
	if tiles == 0 {
		t.Fatal("the full copy made no tiles")
	}

	smaller := bgra.New(300, 200)
	// The area is from before the shrink and reaches past the new buffer.
	if view.update(smaller, image.Rect(0, 0, 1200, 900)) {
		t.Fatal("a buffer smaller than the picture must be refused")
	}
	if view.tileCount() != tiles {
		t.Fatal("a refused update must change nothing")
	}
}

func TestScreenUpdateRefusesShortBuffer(t *testing.T) {
	view := newTestScreen(t)
	view.resize(600, 600)
	short := bgra.New(600, 600)
	short.Pix = short.Pix[:len(short.Pix)/2]
	if view.update(short, image.Rect(0, 0, 600, 600)) {
		t.Fatal("a buffer without the pixels its size claims must be refused")
	}
	if view.tileCount() != 0 {
		t.Fatal("nothing may be copied from a short buffer")
	}
	padded := &bgra.Image{Pix: make([]byte, 600*4*600), Stride: 600 * 3, Width: 600, Height: 600}
	if view.update(padded, image.Rect(0, 0, 600, 600)) {
		t.Fatal("a stride below the row size must be refused")
	}
}

// The whole path on a resize to smaller: plan, resize, copy, with the dirty area
// of the old size still pending.
func TestScreenResizeSmallerThenFullCopy(t *testing.T) {
	view := newTestScreen(t)
	view.resize(1200, 900)
	view.update(bgra.New(1200, 900), image.Rect(0, 0, 1200, 900))

	next := bgra.New(300, 200)
	resize, area := planPicture(next.Bounds(), image.Pt(view.width, view.height), image.Rect(900, 600, 1200, 900), false)
	if !resize {
		t.Fatal("a new size means resize")
	}
	view.resize(next.Width, next.Height)
	if !view.update(next, area) {
		t.Fatal("after the resize the buffer fits")
	}
	if view.tileCount() != 1 {
		t.Fatalf("300x200 is one tile, got %d", view.tileCount())
	}
}

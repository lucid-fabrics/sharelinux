//go:build linux

package main

/*
#cgo pkg-config: gtk4 gtk4-x11 gtk4-wayland wayland-client x11 xfixes xi
#cgo LDFLAGS: -lm
#include "screen.h"
#include "x11.h"
*/
import "C"

import (
	"image"
	"os"
	"strconv"
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// defaultTileSize is the side of a picture tile in pixels. SHARELINUX_TILE
// overrides it for measurements; 0 draws the whole picture as one texture.
const defaultTileSize = 512

// screenView is the widget that draws the Mac's framebuffer.
type screenView struct {
	*gtk.Widget
	c *C.GtkWidget

	width, height int
}

func newScreenView() *screenView {
	tile := defaultTileSize
	if value := os.Getenv("SHARELINUX_TILE"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			tile = parsed
		}
	}
	c := C.oss_screen_new(C.int(tile))
	object := coreglib.Take(unsafe.Pointer(c))
	// gotk4 has no constructor for a widget type it does not know, so the
	// wrapper is filled the way its own code does.
	widget := &gtk.Widget{
		InitiallyUnowned: coreglib.InitiallyUnowned{Object: object},
		Object:           object,
		Accessible:       gtk.Accessible{Object: object},
		Buildable:        gtk.Buildable{Object: object},
		ConstraintTarget: gtk.ConstraintTarget{Object: object},
	}
	return &screenView{Widget: widget, c: c}
}

// resize starts a new picture of the given size.
func (s *screenView) resize(width, height int) {
	s.width, s.height = width, height
	C.oss_screen_resize(s.c, C.int(width), C.int(height))
}

// update copies a rectangle of the framebuffer into the tiles it touches. It
// returns false when the framebuffer does not fit the picture the widget holds.
func (s *screenView) update(framebuffer *bgra.Image, area image.Rectangle) bool {
	if framebuffer == nil || len(framebuffer.Pix) == 0 {
		return true
	}
	area = area.Intersect(framebuffer.Bounds())
	if area.Empty() {
		return true
	}
	return C.oss_screen_update(s.c, (*C.guint8)(unsafe.Pointer(&framebuffer.Pix[0])), C.gsize(len(framebuffer.Pix)), C.gsize(framebuffer.Stride),
		C.int(framebuffer.Width), C.int(framebuffer.Height),
		C.int(area.Min.X), C.int(area.Min.Y), C.int(area.Dx()), C.int(area.Dy())) != 0
}

func (s *screenView) setViewport(port viewport) {
	C.oss_screen_set_viewport(s.c, C.float(port.X), C.float(port.Y), C.float(port.W), C.float(port.H))
}

func (s *screenView) setRealSize(real bool) {
	value := C.gboolean(0)
	if real {
		value = 1
	}
	C.oss_screen_set_real_size(s.c, value)
}

// setCursor shows the Mac's cursor image over the picture, or none with nil.
func (s *screenView) setCursor(cursor *rfb.Cursor) {
	if cursor == nil || cursor.Width <= 0 || cursor.Height <= 0 || len(cursor.Pixels) < cursor.Width*cursor.Height*4 {
		C.oss_screen_set_cursor(s.c, nil, 0, 0, 0, 0)
		return
	}
	C.oss_screen_set_cursor(s.c, (*C.guint8)(unsafe.Pointer(&cursor.Pixels[0])), C.int(cursor.Width), C.int(cursor.Height),
		C.int(cursor.HotspotX), C.int(cursor.HotspotY))
}

func (s *screenView) setCursorPosition(x, y int, visible bool) {
	value := C.gboolean(0)
	if visible {
		value = 1
	}
	C.oss_screen_set_cursor_position(s.c, C.int(x), C.int(y), value)
}

// frames counts the frames the widget has drawn.
func (s *screenView) frames() uint64 {
	return uint64(C.oss_screen_frames(s.c))
}

// clear frees the picture's tiles at once.
func (s *screenView) clear() {
	s.resize(0, 0)
}

// tileCount is the number of tiles that hold a picture, for tests.
func (s *screenView) tileCount() int {
	return int(C.oss_screen_tile_count(s.c))
}

// inhibitShortcuts hands the system's shortcuts (Super, Alt+Tab) to this window, or takes them back.
func (s *screenView) inhibitShortcuts(on bool) bool {
	value := C.gboolean(0)
	if on {
		value = 1
	}
	return C.oss_inhibit_shortcuts(s.c, value) != 0
}

// shortcutsInhibited reports whether the window currently owns the system's shortcuts.
func (s *screenView) shortcutsInhibited() bool {
	return C.oss_shortcuts_inhibited(s.c) != 0
}

func gdkRect(x, y int) *gdk.Rectangle {
	rect := gdk.NewRectangle(x, y, 1, 1)
	return &rect
}

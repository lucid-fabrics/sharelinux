//go:build linux

package main

/*
#cgo pkg-config: gtk4 gtk4-x11 gtk4-wayland wayland-client x11 xfixes xi epoxy
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include "screen.h"
#include "x11.h"
*/
import "C"

import (
	"image"
	"os"
	"strconv"
	"time"
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

	// dmabuf says GPU pictures may go to GTK as DMA-BUFs.
	dmabuf bool
	// dmabufFailures counts the DMA-BUF imports that failed in a row; at three this screen reads pictures
	// back instead, until the next run of the video.
	dmabufFailures C.gint
	// watch follows the widget's drawing for savePNG.
	watch *C.GdkPaintable
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
	return &screenView{Widget: widget, c: c, dmabuf: false}
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

// clear frees the picture's tiles, and the video pictures, at once.
func (s *screenView) clear() {
	s.resize(0, 0)
	C.oss_screen_release_video(s.c)
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

// setVideo shows the Mac's video (true) or the picture tiles. Turning it off
// keeps the last video picture until the tiles change.
func (s *screenView) setVideo(on bool) {
	if on {
		// A new run of the video may be luckier with DMA-BUFs than the one that gave up on them.
		C.oss_dmabuf_failures_reset(&s.dmabufFailures)
	}
	C.oss_screen_set_video(s.c, boolToC(on))
}

// renderer describes what draws this widget.
type rendererInfo struct {
	Name, GL    string
	Accelerated bool
}

func (s *screenView) renderer() rendererInfo {
	var info C.OssRenderer
	C.oss_screen_renderer(s.c, &info)
	return rendererInfo{Name: C.GoString(&info.renderer[0]), GL: C.GoString(&info.gl[0]), Accelerated: info.accelerated != 0}
}

// chooseDMABuf decides whether GPU pictures go to GTK as DMA-BUFs: only when a GPU draws the
// widget and the display imports NV12 at all. SHARELINUX_DMABUF=1 or 0 forces the answer. It
// returns the renderer it looked at.
func (s *screenView) chooseDMABuf() rendererInfo {
	info := s.renderer()
	switch os.Getenv("SHARELINUX_DMABUF") {
	case "1":
		s.dmabuf = true
	case "0":
		s.dmabuf = false
	default:
		s.dmabuf = info.Accelerated && dmabufNV12Supported()
	}
	return info
}

// setDMABuf says whether GPU pictures may go to GTK as DMA-BUFs; it returns the old setting.
func (s *screenView) setDMABuf(on bool) bool {
	old := s.dmabuf
	s.dmabuf = on
	return old
}

// setOffload lets the compositor put the video on a plane of its own.
func (s *screenView) setOffload(on bool) {
	C.oss_screen_set_offload(s.c, boolToC(on))
}

// present makes a texture from a decoded picture and hands it to the widget; it
// can run on any thread. The frame is freed.
func (s *screenView) present(frame *videoFrame) outputKind {
	defer frame.free()
	var output C.OssOutput
	texture := C.oss_frame_texture(frame.c, C.gdk_display_get_default(), boolToC(s.dmabuf), &s.dmabufFailures, &output)
	if texture == nil {
		return outputNone
	}
	present := C.OssPresent{
		src_x: 0, src_y: 0,
		src_width: C.int(frame.Width), src_height: C.int(frame.Height),
		received_us: frame.info.received_us,
		decoded_us:  frame.info.decoded_us,
		decode_us:   frame.info.decode_us,
		output:      output,
	}
	C.oss_screen_present_video(s.c, texture, &present)
	switch output {
	case C.OSS_OUTPUT_DMABUF:
		return outputDMABuf
	case C.OSS_OUTPUT_COPY:
		return outputCopy
	}
	return outputMemory
}

// screenVideo is what the widget knows about the video it draws.
type screenVideo struct {
	Active                      bool
	Queued, Replaced, Presented uint64
	AgeLast, AgeAverage         time.Duration
}

// videoCounters reads the widget's video counters.
func (s *screenView) videoCounters() screenVideo {
	var stats C.OssScreenVideo
	C.oss_screen_video_stats(s.c, &stats)
	result := screenVideo{
		Active:    stats.active != 0,
		Queued:    uint64(stats.queued),
		Replaced:  uint64(stats.replaced),
		Presented: uint64(stats.presented),
		AgeLast:   time.Duration(stats.age_last_us) * time.Microsecond,
	}
	if stats.presented > 0 {
		result.AgeAverage = time.Duration(stats.age_total_us/C.gint64(stats.presented)) * time.Microsecond
	}
	return result
}

// watchDrawing starts following how the widget is drawn. Without it the last picture
// cannot be copied: a widget paintable only knows what was drawn after it was made.
func (s *screenView) watchDrawing() {
	if s.watch == nil {
		s.watch = C.oss_screen_watch(s.c)
	}
}

// stopWatching lets go of the follower.
func (s *screenView) stopWatching() {
	if s.watch != nil {
		C.g_object_unref(C.gpointer(unsafe.Pointer(s.watch)))
		s.watch = nil
	}
}

// savePNG writes the picture as it is drawn now to a PNG file, scaled down to at most
// maxWidth pixels wide. It reports false when nothing could be copied.
func (s *screenView) savePNG(path string, maxWidth int) bool {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	return C.oss_screen_save_png(s.watch, s.c, cpath, C.int(maxWidth)) != 0
}

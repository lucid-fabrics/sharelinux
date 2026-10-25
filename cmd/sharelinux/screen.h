//go:build linux

#ifndef OSS_SCREEN_H
#define OSS_SCREEN_H

#include <gtk/gtk.h>
#include <math.h>

#include "video.h"

// OssScreen draws the Mac's framebuffer. The picture is cut into tiles, each a
// GdkTexture, so a small change replaces one small texture and GSK repaints only
// that part of the window.

GtkWidget *oss_screen_new(int tile_size);

// oss_screen_resize starts a new framebuffer of the given size and drops the old picture.
void oss_screen_resize(GtkWidget *screen, int width, int height);

// oss_screen_update copies the rectangle from the BGRA framebuffer at pixels
// into the tiles it touches. The buffer is length bytes, fb_width x fb_height
// pixels with stride bytes per row, and the call never reads outside it. It
// returns FALSE and copies nothing when the buffer does not fit the picture the
// widget was resized to (the Mac's screen changed size meanwhile), or does not
// hold the pixels its own size claims; the caller then resizes and sends the
// whole picture. The pixels are only read during the call.
gboolean oss_screen_update(GtkWidget *screen, const guint8 *pixels, gsize length, gsize stride, int fb_width, int fb_height, int x, int y, int width, int height);

// oss_screen_set_viewport says where the picture sits, in widget units.
void oss_screen_set_viewport(GtkWidget *screen, float x, float y, float width, float height);

// oss_screen_set_real_size makes the widget ask for the picture's size (one
// framebuffer pixel per device pixel) instead of any size.
void oss_screen_set_real_size(GtkWidget *screen, gboolean real);

// oss_screen_set_cursor sets the Mac's cursor image (premultiplied BGRA), or
// clears it with NULL.
void oss_screen_set_cursor(GtkWidget *screen, const guint8 *pixels, int width, int height, int hot_x, int hot_y);

// oss_screen_set_cursor_position moves the Mac's cursor, in framebuffer pixels.
void oss_screen_set_cursor_position(GtkWidget *screen, int x, int y, gboolean visible);

// oss_screen_frames counts the frames the widget has drawn.
guint64 oss_screen_frames(GtkWidget *screen);

// oss_screen_tile_count is the number of tiles with a texture, for tests and measurements.
int oss_screen_tile_count(GtkWidget *screen);

// oss_screen_watch starts following how the widget is drawn, so oss_screen_save_png
// can copy the last picture. The result is owned by the caller (g_object_unref).
GdkPaintable *oss_screen_watch(GtkWidget *screen);

// oss_screen_snapshot renders what the watched widget shows now, scaled down to at most
// max_width pixels wide. Call it on the main thread. It returns NULL when nothing was
// drawn; the texture is owned by the caller (g_object_unref).
GdkTexture *oss_screen_snapshot(GdkPaintable *watch, GtkWidget *screen, int max_width);

// oss_texture_png encodes a texture as PNG. A texture is immutable, so any thread may
// call it. The result is freed with g_free; NULL when encoding failed.
guchar *oss_texture_png(GdkTexture *texture, gsize *length);

// OssPresent says what a video texture shows and where it came from.
typedef struct {
	int src_x, src_y, src_width, src_height; // the visible part of the texture
	gint64 received_us;                      // g_get_monotonic_time of the unit's first packet
	gint64 decoded_us;
	int decode_us;
	OssOutput output;
} OssPresent;

// oss_screen_set_video chooses what the widget shows: the Mac's video (TRUE) or the
// framebuffer tiles. Turning it off keeps the last video picture on screen until the
// tiles change, so the picture does not jump back to an old one. Main thread.
void oss_screen_set_video(GtkWidget *screen, gboolean on);

// oss_screen_present_video hands over the next video picture. It may be called on any
// thread, takes the caller's reference to texture, and shows only the latest picture:
// one that was not drawn before a newer one arrived is dropped.
void oss_screen_present_video(GtkWidget *screen, GdkTexture *texture, const OssPresent *present);

// OssRenderer says what draws the widget: the GSK renderer, the GL renderer's name when it is
// GL, and whether it is a GPU. A software renderer (llvmpipe, Cairo) cannot take a DMA-BUF
// from the GPU decoder, and the display may still list the formats of a GPU it does not use.
typedef struct {
	char renderer[48];
	char gl[128];
	gboolean accelerated;
} OssRenderer;

// oss_screen_renderer looks at the renderer of the realized widget. Main thread.
void oss_screen_renderer(GtkWidget *screen, OssRenderer *out);

// oss_screen_release_video drops the video pictures the widget holds, drawn or waiting, and so the
// decoder surfaces behind them. Main thread.
void oss_screen_release_video(GtkWidget *screen);

// oss_screen_set_offload turns on GtkGraphicsOffload for the video.
void oss_screen_set_offload(GtkWidget *screen, gboolean on);

typedef struct {
	gboolean active;
	guint64 queued;    // pictures handed over
	guint64 replaced;  // pictures dropped before they were drawn
	guint64 presented; // pictures drawn
	gint64 age_last_us, age_total_us; // time from the first packet to the picture being drawn
	OssOutput output;
	int width, height; // the last picture's size
} OssScreenVideo;

void oss_screen_video_stats(GtkWidget *screen, OssScreenVideo *stats);

// oss_inhibit_shortcuts asks the system to deliver its shortcuts to the window
// (on) or takes the request back (off). It returns whether a toplevel was found.
gboolean oss_inhibit_shortcuts(GtkWidget *widget, gboolean on);

// oss_shortcuts_inhibited reports whether the window currently has the shortcuts.
gboolean oss_shortcuts_inhibited(GtkWidget *widget);

#endif

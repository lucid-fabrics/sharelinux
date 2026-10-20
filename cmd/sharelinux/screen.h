//go:build linux

#ifndef OSS_SCREEN_H
#define OSS_SCREEN_H

#include <gtk/gtk.h>
#include <math.h>

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

// oss_inhibit_shortcuts asks the system to deliver its shortcuts to the window
// (on) or takes the request back (off). It returns whether a toplevel was found.
gboolean oss_inhibit_shortcuts(GtkWidget *widget, gboolean on);

// oss_shortcuts_inhibited reports whether the window currently has the shortcuts.
gboolean oss_shortcuts_inhibited(GtkWidget *widget);

#endif

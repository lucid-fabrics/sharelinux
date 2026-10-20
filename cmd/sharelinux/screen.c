//go:build linux

#include "screen.h"

#include <math.h>
#include <string.h>

#define OSS_TYPE_SCREEN (oss_screen_get_type())
G_DECLARE_FINAL_TYPE(OssScreen, oss_screen, OSS, SCREEN, GtkWidget)

struct _OssScreen {
	GtkWidget parent_instance;

	int tile_size; // 0: one texture for the whole picture
	int width, height;
	int columns, rows;
	GdkTexture **tiles;

	graphene_rect_t viewport;
	gboolean real_size;

	GdkTexture *cursor;
	int cursor_hot_x, cursor_hot_y;
	int cursor_x, cursor_y;
	gboolean cursor_visible;

	guint64 frames;
	GskScalingFilter downscale_filter;
};

G_DEFINE_FINAL_TYPE(OssScreen, oss_screen, GTK_TYPE_WIDGET)

static void clear_tiles(OssScreen *self) {
	if (self->tiles != NULL) {
		for (int i = 0; i < self->columns * self->rows; i++) {
			g_clear_object(&self->tiles[i]);
		}
		g_free(self->tiles);
		self->tiles = NULL;
	}
	self->columns = self->rows = 0;
}

static void oss_screen_dispose(GObject *object) {
	OssScreen *self = OSS_SCREEN(object);
	clear_tiles(self);
	g_clear_object(&self->cursor);
	G_OBJECT_CLASS(oss_screen_parent_class)->dispose(object);
}

static void oss_screen_measure(GtkWidget *widget, GtkOrientation orientation, int for_size, int *minimum, int *natural, int *minimum_baseline, int *natural_baseline) {
	OssScreen *self = OSS_SCREEN(widget);
	int size = 1;
	if (self->real_size) {
		int extent = orientation == GTK_ORIENTATION_HORIZONTAL ? self->width : self->height;
		int scale = gtk_widget_get_scale_factor(widget);
		size = MAX(1, extent / MAX(scale, 1));
	}
	*minimum = self->real_size ? size : 1;
	*natural = size;
	*minimum_baseline = *natural_baseline = -1;
}

static float snap(float value, float scale) {
	return roundf(value * scale) / scale;
}

static void oss_screen_snapshot(GtkWidget *widget, GtkSnapshot *snapshot) {
	OssScreen *self = OSS_SCREEN(widget);
	float w = gtk_widget_get_width(widget), h = gtk_widget_get_height(widget);
	self->frames++;
	gtk_snapshot_append_color(snapshot, &(GdkRGBA){0, 0, 0, 1}, &GRAPHENE_RECT_INIT(0, 0, w, h));
	if (self->tiles == NULL || self->width <= 0 || self->height <= 0 || self->viewport.size.width <= 0) {
		return;
	}
	float device = (float)gtk_widget_get_scale_factor(widget);
	float scale = self->viewport.size.width / (float)self->width;
	GskScalingFilter filter = scale > 1.0f ? GSK_SCALING_FILTER_LINEAR : self->downscale_filter;
	int tile_w = self->tile_size > 0 ? self->tile_size : self->width;
	int tile_h = self->tile_size > 0 ? self->tile_size : self->height;
	for (int row = 0; row < self->rows; row++) {
		float top = snap(self->viewport.origin.y + row * tile_h * scale, device);
		float bottom = row == self->rows - 1 ? snap(self->viewport.origin.y + self->viewport.size.height, device)
		                                     : snap(self->viewport.origin.y + (row + 1) * tile_h * scale, device);
		for (int column = 0; column < self->columns; column++) {
			GdkTexture *tile = self->tiles[row * self->columns + column];
			if (tile == NULL) {
				continue;
			}
			float left = snap(self->viewport.origin.x + column * tile_w * scale, device);
			float right = column == self->columns - 1 ? snap(self->viewport.origin.x + self->viewport.size.width, device)
			                                          : snap(self->viewport.origin.x + (column + 1) * tile_w * scale, device);
			graphene_rect_t bounds = GRAPHENE_RECT_INIT(left, top, right - left, bottom - top);
			gtk_snapshot_append_scaled_texture(snapshot, tile, filter, &bounds);
		}
	}
	if (self->cursor != NULL && self->cursor_visible) {
		float cx = self->viewport.origin.x + (self->cursor_x - self->cursor_hot_x) * scale;
		float cy = self->viewport.origin.y + (self->cursor_y - self->cursor_hot_y) * scale;
		graphene_rect_t bounds = GRAPHENE_RECT_INIT(cx, cy, gdk_texture_get_width(self->cursor) * scale, gdk_texture_get_height(self->cursor) * scale);
		gtk_snapshot_append_scaled_texture(snapshot, self->cursor, GSK_SCALING_FILTER_LINEAR, &bounds);
	}
}

static void oss_screen_class_init(OssScreenClass *class) {
	GObjectClass *object_class = G_OBJECT_CLASS(class);
	GtkWidgetClass *widget_class = GTK_WIDGET_CLASS(class);
	object_class->dispose = oss_screen_dispose;
	widget_class->measure = oss_screen_measure;
	widget_class->snapshot = oss_screen_snapshot;
	gtk_widget_class_set_css_name(widget_class, "ossscreen");
}

static void oss_screen_init(OssScreen *self) {
	gtk_widget_set_focusable(GTK_WIDGET(self), TRUE);
	gtk_widget_set_can_focus(GTK_WIDGET(self), TRUE);
	gtk_widget_set_hexpand(GTK_WIDGET(self), TRUE);
	gtk_widget_set_vexpand(GTK_WIDGET(self), TRUE);
	self->tile_size = 512;
	// A picture bigger than the window is shrunk with linear filtering. Trilinear
	// (SHARELINUX_FILTER=trilinear) smooths more but on a software renderer it is
	// 2x slower at 2560x1600 and 10x slower at 6522x4154, so it is opt-in.
	self->downscale_filter = GSK_SCALING_FILTER_LINEAR;
	const char *filter = g_getenv("SHARELINUX_FILTER");
	if (g_strcmp0(filter, "trilinear") == 0) {
		self->downscale_filter = GSK_SCALING_FILTER_TRILINEAR;
	} else if (g_strcmp0(filter, "nearest") == 0) {
		self->downscale_filter = GSK_SCALING_FILTER_NEAREST;
	}
}

GtkWidget *oss_screen_new(int tile_size) {
	OssScreen *self = g_object_new(OSS_TYPE_SCREEN, NULL);
	self->tile_size = tile_size < 0 ? 0 : tile_size;
	return GTK_WIDGET(self);
}

void oss_screen_resize(GtkWidget *screen, int width, int height) {
	OssScreen *self = OSS_SCREEN(screen);
	clear_tiles(self);
	self->width = MAX(width, 0);
	self->height = MAX(height, 0);
	if (self->width > 0 && self->height > 0) {
		int tile_w = self->tile_size > 0 ? self->tile_size : self->width;
		int tile_h = self->tile_size > 0 ? self->tile_size : self->height;
		self->columns = (self->width + tile_w - 1) / tile_w;
		self->rows = (self->height + tile_h - 1) / tile_h;
		self->tiles = g_new0(GdkTexture *, self->columns * self->rows);
	}
	gtk_widget_queue_resize(screen);
	gtk_widget_queue_draw(screen);
}

static void rebuild_tile(OssScreen *self, const guint8 *pixels, gsize stride, int column, int row) {
	int tile_w = self->tile_size > 0 ? self->tile_size : self->width;
	int tile_h = self->tile_size > 0 ? self->tile_size : self->height;
	int x = column * tile_w, y = row * tile_h;
	int w = MIN(tile_w, self->width - x), h = MIN(tile_h, self->height - y);
	gsize row_bytes = (gsize)w * 4;
	guint8 *copy = g_malloc(row_bytes * h);
	for (int line = 0; line < h; line++) {
		memcpy(copy + row_bytes * line, pixels + stride * (gsize)(y + line) + (gsize)x * 4, row_bytes);
	}
	GBytes *bytes = g_bytes_new_take(copy, row_bytes * h);
	GdkTexture *texture = gdk_memory_texture_new(w, h, GDK_MEMORY_B8G8R8X8, bytes, row_bytes);
	g_bytes_unref(bytes);
	int index = row * self->columns + column;
	g_clear_object(&self->tiles[index]);
	self->tiles[index] = texture;
}

void oss_screen_update(GtkWidget *screen, const guint8 *pixels, gsize stride, int x, int y, int width, int height) {
	OssScreen *self = OSS_SCREEN(screen);
	if (self->tiles == NULL || width <= 0 || height <= 0) {
		return;
	}
	int tile_w = self->tile_size > 0 ? self->tile_size : self->width;
	int tile_h = self->tile_size > 0 ? self->tile_size : self->height;
	int x1 = MIN(x + width, self->width), y1 = MIN(y + height, self->height);
	x = MAX(x, 0);
	y = MAX(y, 0);
	if (x >= x1 || y >= y1) {
		return;
	}
	for (int row = y / tile_h; row <= (y1 - 1) / tile_h; row++) {
		for (int column = x / tile_w; column <= (x1 - 1) / tile_w; column++) {
			rebuild_tile(self, pixels, stride, column, row);
		}
	}
	gtk_widget_queue_draw(screen);
}

void oss_screen_set_viewport(GtkWidget *screen, float x, float y, float width, float height) {
	OssScreen *self = OSS_SCREEN(screen);
	graphene_rect_t next = GRAPHENE_RECT_INIT(x, y, width, height);
	if (graphene_rect_equal(&next, &self->viewport)) {
		return;
	}
	self->viewport = next;
	gtk_widget_queue_draw(screen);
}

void oss_screen_set_real_size(GtkWidget *screen, gboolean real) {
	OssScreen *self = OSS_SCREEN(screen);
	if (self->real_size == real) {
		return;
	}
	self->real_size = real;
	gtk_widget_queue_resize(screen);
}

void oss_screen_set_cursor(GtkWidget *screen, const guint8 *pixels, int width, int height, int hot_x, int hot_y) {
	OssScreen *self = OSS_SCREEN(screen);
	g_clear_object(&self->cursor);
	if (pixels != NULL && width > 0 && height > 0) {
		GBytes *bytes = g_bytes_new(pixels, (gsize)width * height * 4);
		self->cursor = gdk_memory_texture_new(width, height, GDK_MEMORY_B8G8R8A8_PREMULTIPLIED, bytes, (gsize)width * 4);
		g_bytes_unref(bytes);
		self->cursor_hot_x = hot_x;
		self->cursor_hot_y = hot_y;
	}
	gtk_widget_queue_draw(screen);
}

void oss_screen_set_cursor_position(GtkWidget *screen, int x, int y, gboolean visible) {
	OssScreen *self = OSS_SCREEN(screen);
	if (self->cursor_x == x && self->cursor_y == y && self->cursor_visible == visible) {
		return;
	}
	self->cursor_x = x;
	self->cursor_y = y;
	self->cursor_visible = visible;
	if (self->cursor != NULL) {
		gtk_widget_queue_draw(screen);
	}
}

guint64 oss_screen_frames(GtkWidget *screen) {
	return OSS_SCREEN(screen)->frames;
}

int oss_screen_tile_count(GtkWidget *screen) {
	OssScreen *self = OSS_SCREEN(screen);
	int count = 0;
	for (int i = 0; self->tiles != NULL && i < self->columns * self->rows; i++) {
		count += self->tiles[i] != NULL;
	}
	return count;
}

static GdkToplevel *toplevel_of(GtkWidget *widget) {
	GtkNative *native = gtk_widget_get_native(widget);
	if (native == NULL) {
		return NULL;
	}
	GdkSurface *surface = gtk_native_get_surface(native);
	if (surface == NULL || !GDK_IS_TOPLEVEL(surface)) {
		return NULL;
	}
	return GDK_TOPLEVEL(surface);
}

gboolean oss_inhibit_shortcuts(GtkWidget *widget, gboolean on) {
	GdkToplevel *toplevel = toplevel_of(widget);
	if (toplevel == NULL) {
		return FALSE;
	}
	if (on) {
		gdk_toplevel_inhibit_system_shortcuts(toplevel, NULL);
	} else {
		gdk_toplevel_restore_system_shortcuts(toplevel);
	}
	return TRUE;
}

gboolean oss_shortcuts_inhibited(GtkWidget *widget) {
	GdkToplevel *toplevel = toplevel_of(widget);
	if (toplevel == NULL) {
		return FALSE;
	}
	gboolean inhibited = FALSE;
	g_object_get(toplevel, "shortcuts-inhibited", &inhibited, NULL);
	return inhibited;
}

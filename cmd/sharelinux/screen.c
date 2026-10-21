//go:build linux

#include "screen.h"

#include <epoxy/gl.h>
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

	// The video picture lives in a child inside a GtkGraphicsOffload, so a compositor
	// can put it on a plane of its own.
	GtkWidget *offload;
	GtkWidget *view;
	gboolean video_on, video_hold;
};

G_DEFINE_FINAL_TYPE(OssScreen, oss_screen, GTK_TYPE_WIDGET)

// ---- the video picture ---------------------------------------------------

#define OSS_TYPE_VIDEO_VIEW (oss_video_view_get_type())
G_DECLARE_FINAL_TYPE(OssVideoView, oss_video_view, OSS, VIDEO_VIEW, GtkWidget)

struct _OssVideoView {
	GtkWidget parent_instance;

	GMutex lock;
	GdkTexture *pending;
	OssPresent pending_present;
	gboolean idle_queued;
	guint64 queued, replaced;

	GdkTexture *texture;
	OssPresent present;
	gboolean fresh; // texture was not drawn yet
	guint64 presented;
	gint64 age_last_us, age_total_us;
	graphene_rect_t viewport;
	int tex_width, tex_height;
	GskScalingFilter filter;
};

G_DEFINE_FINAL_TYPE(OssVideoView, oss_video_view, GTK_TYPE_WIDGET)

static void oss_video_view_dispose(GObject *object) {
	OssVideoView *self = OSS_VIDEO_VIEW(object);
	g_mutex_lock(&self->lock);
	g_clear_object(&self->pending);
	g_mutex_unlock(&self->lock);
	g_clear_object(&self->texture);
	G_OBJECT_CLASS(oss_video_view_parent_class)->dispose(object);
}

static void oss_video_view_finalize(GObject *object) {
	g_mutex_clear(&OSS_VIDEO_VIEW(object)->lock);
	G_OBJECT_CLASS(oss_video_view_parent_class)->finalize(object);
}

static void oss_video_view_measure(GtkWidget *widget, GtkOrientation orientation, int for_size, int *minimum, int *natural,
                                   int *minimum_baseline, int *natural_baseline) {
	*minimum = *natural = 1;
	*minimum_baseline = *natural_baseline = -1;
}

static void oss_video_view_snapshot(GtkWidget *widget, GtkSnapshot *snapshot) {
	OssVideoView *self = OSS_VIDEO_VIEW(widget);
	if (self->texture == NULL || self->viewport.size.width <= 0 || self->viewport.size.height <= 0) {
		return;
	}
	if (self->fresh) {
		self->fresh = FALSE;
		self->presented++;
		self->age_last_us = MAX(g_get_monotonic_time() - self->present.received_us, 0);
		self->age_total_us += self->age_last_us;
	}
	const graphene_rect_t *port = &self->viewport;
	int tex_w = gdk_texture_get_width(self->texture), tex_h = gdk_texture_get_height(self->texture);
	int sx = self->present.src_x, sy = self->present.src_y;
	int sw = self->present.src_width > 0 ? self->present.src_width : tex_w;
	int sh = self->present.src_height > 0 ? self->present.src_height : tex_h;
	float scale_x = port->size.width / (float)sw, scale_y = port->size.height / (float)sh;
	GskScalingFilter filter = scale_x > 1.0f ? GSK_SCALING_FILTER_LINEAR : self->filter;
	if (sx == 0 && sy == 0 && sw == tex_w && sh == tex_h) {
		graphene_rect_t bounds = GRAPHENE_RECT_INIT(port->origin.x, port->origin.y, port->size.width, port->size.height);
		if (filter == GSK_SCALING_FILTER_LINEAR) {
			gtk_snapshot_append_texture(snapshot, self->texture, &bounds);
		} else {
			gtk_snapshot_append_scaled_texture(snapshot, self->texture, filter, &bounds);
		}
		return;
	}
	// Only part of the texture is picture (the decoder's surface is bigger): clip to it.
	gtk_snapshot_push_clip(snapshot, &GRAPHENE_RECT_INIT(port->origin.x, port->origin.y, port->size.width, port->size.height));
	graphene_rect_t bounds = GRAPHENE_RECT_INIT(port->origin.x - sx * scale_x, port->origin.y - sy * scale_y, tex_w * scale_x, tex_h * scale_y);
	gtk_snapshot_append_scaled_texture(snapshot, self->texture, filter, &bounds);
	gtk_snapshot_pop(snapshot);
}

static void oss_video_view_class_init(OssVideoViewClass *class) {
	GObjectClass *object_class = G_OBJECT_CLASS(class);
	GtkWidgetClass *widget_class = GTK_WIDGET_CLASS(class);
	object_class->dispose = oss_video_view_dispose;
	object_class->finalize = oss_video_view_finalize;
	widget_class->measure = oss_video_view_measure;
	widget_class->snapshot = oss_video_view_snapshot;
	gtk_widget_class_set_css_name(widget_class, "ossvideo");
}

static void oss_video_view_init(OssVideoView *self) {
	g_mutex_init(&self->lock);
	self->filter = GSK_SCALING_FILTER_LINEAR;
	gtk_widget_set_hexpand(GTK_WIDGET(self), TRUE);
	gtk_widget_set_vexpand(GTK_WIDGET(self), TRUE);
	gtk_widget_set_can_target(GTK_WIDGET(self), FALSE);
}

// Runs on the main loop: the latest picture becomes the one to draw.
static gboolean video_view_apply(gpointer data) {
	OssVideoView *self = OSS_VIDEO_VIEW(data);
	g_mutex_lock(&self->lock);
	GdkTexture *texture = self->pending;
	OssPresent present = self->pending_present;
	self->pending = NULL;
	self->idle_queued = FALSE;
	g_mutex_unlock(&self->lock);
	if (texture != NULL) {
		g_clear_object(&self->texture);
		self->texture = texture;
		self->present = present;
		self->fresh = TRUE;
		gtk_widget_queue_draw(GTK_WIDGET(self));
	}
	return G_SOURCE_REMOVE;
}

static void video_view_present(OssVideoView *self, GdkTexture *texture, const OssPresent *present) {
	g_mutex_lock(&self->lock);
	if (self->pending != NULL) {
		g_object_unref(self->pending);
		self->replaced++;
	}
	self->pending = texture;
	self->pending_present = *present;
	self->queued++;
	gboolean queue = !self->idle_queued;
	self->idle_queued = TRUE;
	g_mutex_unlock(&self->lock);
	if (queue) {
		g_idle_add_full(G_PRIORITY_DEFAULT, video_view_apply, g_object_ref(self), g_object_unref);
	}
}

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
	if (self->offload != NULL) {
		gtk_widget_unparent(self->offload);
		self->offload = NULL;
		self->view = NULL;
	}
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

static void draw_cursor(OssScreen *self, GtkSnapshot *snapshot, float scale) {
	if (self->cursor != NULL && self->cursor_visible) {
		float cx = self->viewport.origin.x + (self->cursor_x - self->cursor_hot_x) * scale;
		float cy = self->viewport.origin.y + (self->cursor_y - self->cursor_hot_y) * scale;
		graphene_rect_t bounds = GRAPHENE_RECT_INIT(cx, cy, gdk_texture_get_width(self->cursor) * scale, gdk_texture_get_height(self->cursor) * scale);
		gtk_snapshot_append_scaled_texture(snapshot, self->cursor, GSK_SCALING_FILTER_LINEAR, &bounds);
	}
}

static void oss_screen_snapshot(GtkWidget *widget, GtkSnapshot *snapshot) {
	OssScreen *self = OSS_SCREEN(widget);
	float w = gtk_widget_get_width(widget), h = gtk_widget_get_height(widget);
	self->frames++;
	if (self->video_on || self->video_hold) {
		// The offload child paints its own black behind the picture.
		gtk_widget_snapshot_child(widget, self->offload, snapshot);
		if (self->width > 0 && self->viewport.size.width > 0) {
			draw_cursor(self, snapshot, self->viewport.size.width / (float)self->width);
		}
		return;
	}
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
	draw_cursor(self, snapshot, scale);
}

static void oss_screen_size_allocate(GtkWidget *widget, int width, int height, int baseline) {
	OssScreen *self = OSS_SCREEN(widget);
	if (self->offload != NULL && gtk_widget_get_visible(self->offload)) {
		gtk_widget_measure(self->offload, GTK_ORIENTATION_HORIZONTAL, -1, NULL, NULL, NULL, NULL);
		gtk_widget_measure(self->offload, GTK_ORIENTATION_VERTICAL, -1, NULL, NULL, NULL, NULL);
		gtk_widget_allocate(self->offload, width, height, baseline, NULL);
	}
}

static void oss_screen_class_init(OssScreenClass *class) {
	GObjectClass *object_class = G_OBJECT_CLASS(class);
	GtkWidgetClass *widget_class = GTK_WIDGET_CLASS(class);
	object_class->dispose = oss_screen_dispose;
	widget_class->measure = oss_screen_measure;
	widget_class->snapshot = oss_screen_snapshot;
	widget_class->size_allocate = oss_screen_size_allocate;
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
	self->view = g_object_new(OSS_TYPE_VIDEO_VIEW, NULL);
	OSS_VIDEO_VIEW(self->view)->filter = self->downscale_filter;
	self->offload = gtk_graphics_offload_new(self->view);
	gtk_graphics_offload_set_black_background(GTK_GRAPHICS_OFFLOAD(self->offload), TRUE);
	gtk_widget_set_can_target(self->offload, FALSE);
	gtk_widget_set_visible(self->offload, FALSE);
	gtk_widget_set_parent(self->offload, GTK_WIDGET(self));
	if (g_strcmp0(g_getenv("SHARELINUX_OFFLOAD"), "0") == 0) {
		gtk_graphics_offload_set_enabled(GTK_GRAPHICS_OFFLOAD(self->offload), GTK_GRAPHICS_OFFLOAD_DISABLED);
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

gboolean oss_screen_update(GtkWidget *screen, const guint8 *pixels, gsize length, gsize stride, int fb_width, int fb_height, int x, int y, int width, int height) {
	OssScreen *self = OSS_SCREEN(screen);
	if (self->tiles == NULL || pixels == NULL || fb_width != self->width || fb_height != self->height) {
		return FALSE;
	}
	// The last row needs only its own pixels, not a whole stride.
	if (fb_width <= 0 || fb_height <= 0 || stride < (gsize)fb_width * 4 || length / stride < (gsize)(fb_height - 1) ||
	    length - stride * (gsize)(fb_height - 1) < (gsize)fb_width * 4) {
		return FALSE;
	}
	if (width <= 0 || height <= 0) {
		return TRUE;
	}
	int tile_w = self->tile_size > 0 ? self->tile_size : self->width;
	int tile_h = self->tile_size > 0 ? self->tile_size : self->height;
	int x1 = MIN(x + width, self->width), y1 = MIN(y + height, self->height);
	x = MAX(x, 0);
	y = MAX(y, 0);
	if (x >= x1 || y >= y1) {
		return TRUE;
	}
	for (int row = y / tile_h; row <= (y1 - 1) / tile_h; row++) {
		for (int column = x / tile_w; column <= (x1 - 1) / tile_w; column++) {
			rebuild_tile(self, pixels, stride, column, row);
		}
	}
	if (self->video_hold) {
		self->video_hold = FALSE;
		gtk_widget_set_visible(self->offload, FALSE);
		gtk_widget_queue_resize(screen);
	}
	gtk_widget_queue_draw(screen);
	return TRUE;
}

void oss_screen_set_viewport(GtkWidget *screen, float x, float y, float width, float height) {
	OssScreen *self = OSS_SCREEN(screen);
	graphene_rect_t next = GRAPHENE_RECT_INIT(x, y, width, height);
	if (graphene_rect_equal(&next, &self->viewport)) {
		return;
	}
	self->viewport = next;
	OSS_VIDEO_VIEW(self->view)->viewport = next;
	gtk_widget_queue_draw(screen);
	gtk_widget_queue_draw(self->view);
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

void oss_screen_set_video(GtkWidget *screen, gboolean on) {
	OssScreen *self = OSS_SCREEN(screen);
	if (on) {
		self->video_hold = FALSE;
	} else if (self->video_on) {
		self->video_hold = TRUE;
	}
	self->video_on = on;
	gtk_widget_set_visible(self->offload, on || self->video_hold);
	gtk_widget_queue_resize(screen);
	gtk_widget_queue_draw(screen);
}

void oss_screen_present_video(GtkWidget *screen, GdkTexture *texture, const OssPresent *present) {
	OssScreen *self = OSS_SCREEN(screen);
	video_view_present(OSS_VIDEO_VIEW(self->view), texture, present);
}

// The GL renderer string of a context on the default display, found once.
static const char *gl_renderer_string(GdkDisplay *display) {
	static char found[128];
	static gboolean done;
	if (done) {
		return found;
	}
	done = TRUE;
	GError *error = NULL;
	GdkGLContext *context = gdk_display_create_gl_context(display, &error);
	if (context == NULL) {
		g_clear_error(&error);
		return found;
	}
	if (gdk_gl_context_realize(context, &error)) {
		gdk_gl_context_make_current(context);
		const char *text = (const char *)glGetString(GL_RENDERER);
		if (text != NULL) {
			g_strlcpy(found, text, sizeof found);
		}
		gdk_gl_context_clear_current();
	}
	g_clear_error(&error);
	g_object_unref(context);
	return found;
}

void oss_screen_renderer(GtkWidget *screen, OssRenderer *out) {
	memset(out, 0, sizeof *out);
	GtkNative *native = gtk_widget_get_native(screen);
	GskRenderer *renderer = native != NULL ? gtk_native_get_renderer(native) : NULL;
	if (renderer == NULL) {
		return;
	}
	g_strlcpy(out->renderer, G_OBJECT_TYPE_NAME(renderer), sizeof out->renderer);
	if (strstr(out->renderer, "Cairo") != NULL) {
		return;
	}
	if (strstr(out->renderer, "Vulkan") != NULL) {
		out->accelerated = TRUE;
		return;
	}
	g_strlcpy(out->gl, gl_renderer_string(gtk_widget_get_display(screen)), sizeof out->gl);
	char *lower = g_ascii_strdown(out->gl, -1);
	out->accelerated = out->gl[0] != '\0' && strstr(lower, "llvmpipe") == NULL && strstr(lower, "softpipe") == NULL &&
	                   strstr(lower, "swrast") == NULL && strstr(lower, "software") == NULL;
	g_free(lower);
}

void oss_screen_release_video(GtkWidget *screen) {
	OssVideoView *view = OSS_VIDEO_VIEW(OSS_SCREEN(screen)->view);
	g_mutex_lock(&view->lock);
	g_clear_object(&view->pending);
	g_mutex_unlock(&view->lock);
	g_clear_object(&view->texture);
	gtk_widget_queue_draw(screen);
}

void oss_screen_set_offload(GtkWidget *screen, gboolean on) {
	OssScreen *self = OSS_SCREEN(screen);
	gtk_graphics_offload_set_enabled(GTK_GRAPHICS_OFFLOAD(self->offload), on ? GTK_GRAPHICS_OFFLOAD_ENABLED : GTK_GRAPHICS_OFFLOAD_DISABLED);
}

void oss_screen_video_stats(GtkWidget *screen, OssScreenVideo *stats) {
	OssScreen *self = OSS_SCREEN(screen);
	OssVideoView *view = OSS_VIDEO_VIEW(self->view);
	memset(stats, 0, sizeof *stats);
	g_mutex_lock(&view->lock);
	stats->queued = view->queued;
	stats->replaced = view->replaced;
	g_mutex_unlock(&view->lock);
	stats->active = self->video_on;
	stats->presented = view->presented;
	stats->age_last_us = view->age_last_us;
	stats->age_total_us = view->age_total_us;
	stats->output = view->present.output;
	if (view->texture != NULL) {
		stats->width = gdk_texture_get_width(view->texture);
		stats->height = gdk_texture_get_height(view->texture);
	}
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

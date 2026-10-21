//go:build linux

#include "wayland.h"

// GDK 4.18 deprecates the X11 handles; GDK_IS_X11_DISPLAY is only used to tell the backends apart.
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"

#include <string.h>
#include <wayland-client.h>

#include <gdk/wayland/gdkwayland.h>
#include <gdk/x11/gdkx.h>

#include "_cgo_export.h"
#include "pointer-constraints-unstable-v1-client-protocol.h"
#include "relative-pointer-unstable-v1-client-protocol.h"
#include "x11.h"

static struct zwp_pointer_constraints_v1 *constraints = NULL;
static struct zwp_relative_pointer_manager_v1 *relative_manager = NULL;
static gboolean probed = FALSE;

static struct zwp_locked_pointer_v1 *locked = NULL;
static struct zwp_relative_pointer_v1 *relative = NULL;

static void registry_global(void *data, struct wl_registry *registry, uint32_t name, const char *interface, uint32_t version) {
	if (strcmp(interface, zwp_pointer_constraints_v1_interface.name) == 0) {
		constraints = wl_registry_bind(registry, name, &zwp_pointer_constraints_v1_interface, 1);
	} else if (strcmp(interface, zwp_relative_pointer_manager_v1_interface.name) == 0) {
		relative_manager = wl_registry_bind(registry, name, &zwp_relative_pointer_manager_v1_interface, 1);
	}
}

static void registry_global_remove(void *data, struct wl_registry *registry, uint32_t name) {
}

static const struct wl_registry_listener registry_listener = {
	.global = registry_global,
	.global_remove = registry_global_remove,
};

// bind_globals looks the two protocols up once. The registry is read on a queue
// of its own so that GDK's handlers never run inside this call, and the two
// objects move to the default queue afterwards, where GDK's main loop source
// dispatches their events on the GTK thread.
static void bind_globals(GdkDisplay *display) {
	if (probed) {
		return;
	}
	probed = TRUE;
	struct wl_display *wl_display = gdk_wayland_display_get_wl_display(display);
	if (wl_display == NULL) {
		return;
	}
	struct wl_event_queue *queue = wl_display_create_queue(wl_display);
	struct wl_display *wrapper = wl_proxy_create_wrapper(wl_display);
	wl_proxy_set_queue((struct wl_proxy *)wrapper, queue);
	struct wl_registry *registry = wl_display_get_registry(wrapper);
	wl_proxy_wrapper_destroy(wrapper);
	wl_registry_add_listener(registry, &registry_listener, NULL);
	wl_display_roundtrip_queue(wl_display, queue);
	if (constraints != NULL) {
		wl_proxy_set_queue((struct wl_proxy *)constraints, NULL);
	}
	if (relative_manager != NULL) {
		wl_proxy_set_queue((struct wl_proxy *)relative_manager, NULL);
	}
	wl_registry_destroy(registry);
	wl_event_queue_destroy(queue);
}

int oss_lock_caps(void) {
	GdkDisplay *display = gdk_display_get_default();
	if (display == NULL) {
		return 0;
	}
	if (GDK_IS_WAYLAND_DISPLAY(display)) {
		bind_globals(display);
		return OSS_CAP_WAYLAND | (constraints != NULL ? OSS_CAP_CONSTRAINTS : 0) | (relative_manager != NULL ? OSS_CAP_RELATIVE : 0);
	}
	if (GDK_IS_X11_DISPLAY(display)) {
		return oss_x11_caps();
	}
	return 0;
}

static void on_locked(void *data, struct zwp_locked_pointer_v1 *pointer) {
	ossPointerLockState(1);
}

static void on_unlocked(void *data, struct zwp_locked_pointer_v1 *pointer) {
	ossPointerLockState(0);
}

static const struct zwp_locked_pointer_v1_listener locked_listener = {
	.locked = on_locked,
	.unlocked = on_unlocked,
};

static void on_relative_motion(void *data, struct zwp_relative_pointer_v1 *pointer, uint32_t utime_hi, uint32_t utime_lo,
	wl_fixed_t dx, wl_fixed_t dy, wl_fixed_t dx_unaccel, wl_fixed_t dy_unaccel) {
	ossPointerMotion(wl_fixed_to_double(dx), wl_fixed_to_double(dy));
}

static const struct zwp_relative_pointer_v1_listener relative_listener = {
	.relative_motion = on_relative_motion,
};

static void drop_objects(void) {
	if (relative != NULL) {
		zwp_relative_pointer_v1_destroy(relative);
		relative = NULL;
	}
	if (locked != NULL) {
		zwp_locked_pointer_v1_destroy(locked);
		locked = NULL;
	}
}

gboolean oss_wl_lock(GtkWidget *widget) {
	GdkDisplay *display = gdk_display_get_default();
	if (display == NULL || !GDK_IS_WAYLAND_DISPLAY(display) || constraints == NULL || relative_manager == NULL) {
		return FALSE;
	}
	GtkNative *native = gtk_widget_get_native(widget);
	GdkSurface *surface = native != NULL ? gtk_native_get_surface(native) : NULL;
	if (surface == NULL || !GDK_IS_WAYLAND_SURFACE(surface)) {
		return FALSE;
	}
	GdkSeat *seat = gdk_display_get_default_seat(display);
	GdkDevice *device = seat != NULL ? gdk_seat_get_pointer(seat) : NULL;
	struct wl_pointer *pointer = device != NULL ? gdk_wayland_device_get_wl_pointer(device) : NULL;
	struct wl_surface *wl_surface = gdk_wayland_surface_get_wl_surface(surface);
	if (pointer == NULL || wl_surface == NULL) {
		return FALSE;
	}
	// Two locks on one surface are a protocol error that ends the connection.
	drop_objects();
	// Oneshot: when the compositor ends the lock (focus lost) it is gone, and
	// a later request starts a fresh one. Nothing re-locks behind our back.
	locked = zwp_pointer_constraints_v1_lock_pointer(constraints, wl_surface, pointer, NULL, ZWP_POINTER_CONSTRAINTS_V1_LIFETIME_ONESHOT);
	zwp_locked_pointer_v1_add_listener(locked, &locked_listener, NULL);
	relative = zwp_relative_pointer_manager_v1_get_relative_pointer(relative_manager, pointer);
	zwp_relative_pointer_v1_add_listener(relative, &relative_listener, NULL);
	wl_display_flush(gdk_wayland_display_get_wl_display(display));
	return TRUE;
}

void oss_wl_unlock(GtkWidget *widget, gboolean has_hint, double x, double y) {
	if (locked == NULL && relative == NULL) {
		return;
	}
	GdkDisplay *display = gdk_display_get_default();
	if (has_hint && locked != NULL) {
		GtkNative *native = gtk_widget_get_native(widget);
		GdkSurface *surface = native != NULL ? gtk_native_get_surface(native) : NULL;
		struct wl_surface *wl_surface = surface != NULL && GDK_IS_WAYLAND_SURFACE(surface) ? gdk_wayland_surface_get_wl_surface(surface) : NULL;
		// The hint is in surface units; the point given is in the widget's own.
		graphene_point_t point = GRAPHENE_POINT_INIT((float)x, (float)y), in_surface;
		if (native != NULL && gtk_widget_compute_point(widget, GTK_WIDGET(native), &point, &in_surface)) {
			double offset_x = 0, offset_y = 0;
			gtk_native_get_surface_transform(native, &offset_x, &offset_y);
			x = in_surface.x + offset_x;
			y = in_surface.y + offset_y;
		}
		zwp_locked_pointer_v1_set_cursor_position_hint(locked, wl_fixed_from_double(x), wl_fixed_from_double(y));
		// The hint is double buffered: it counts from the next commit.
		if (wl_surface != NULL) {
			wl_surface_commit(wl_surface);
		}
	}
	drop_objects();
	if (display != NULL && GDK_IS_WAYLAND_DISPLAY(display)) {
		wl_display_flush(gdk_wayland_display_get_wl_display(display));
	}
}

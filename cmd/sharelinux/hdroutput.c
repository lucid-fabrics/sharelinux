//go:build linux

#include "hdroutput.h"

#include <string.h>
#include <unistd.h>
#include <wayland-client.h>

#include <gdk/wayland/gdkwayland.h>

#include "color-management-v1-client-protocol.h"

#define MAX_OUTPUTS 16
// Describing an output takes one round trip to become ready and one to be told; a few more are slack.
#define ROUNDTRIPS 4

typedef struct {
	OssOutputColor color;
	gboolean ready, failed, done;
} Describe;

static void description_failed(void *data, struct wp_image_description_v1 *description, uint32_t cause, const char *message) {
	((Describe *)data)->failed = TRUE;
}

static void description_ready(void *data, struct wp_image_description_v1 *description, uint32_t identity) {
	((Describe *)data)->ready = TRUE;
}

static void description_ready2(void *data, struct wp_image_description_v1 *description, uint32_t identity_hi, uint32_t identity_lo) {
	((Describe *)data)->ready = TRUE;
}

static const struct wp_image_description_v1_listener description_listener = {
	.failed = description_failed,
	.ready = description_ready,
	.ready2 = description_ready2,
};

static void info_icc_file(void *data, struct wp_image_description_info_v1 *info, int32_t icc, uint32_t size) {
	close(icc);
}

static void info_primaries(void *data, struct wp_image_description_info_v1 *info, int32_t r_x, int32_t r_y, int32_t g_x, int32_t g_y,
                           int32_t b_x, int32_t b_y, int32_t w_x, int32_t w_y) {
}

static void info_primaries_named(void *data, struct wp_image_description_info_v1 *info, uint32_t primaries) {
}

static void info_tf_power(void *data, struct wp_image_description_info_v1 *info, uint32_t eexp) {
}

static void info_tf_named(void *data, struct wp_image_description_info_v1 *info, uint32_t tf) {
	((Describe *)data)->color.transfer = tf;
}

static void info_luminances(void *data, struct wp_image_description_info_v1 *info, uint32_t min_lum, uint32_t max_lum,
                            uint32_t reference_lum) {
	OssOutputColor *color = &((Describe *)data)->color;
	color->min_lum = min_lum;
	color->max_lum = max_lum;
	color->reference_lum = reference_lum;
}

static void info_target_primaries(void *data, struct wp_image_description_info_v1 *info, int32_t r_x, int32_t r_y, int32_t g_x,
                                  int32_t g_y, int32_t b_x, int32_t b_y, int32_t w_x, int32_t w_y) {
}

static void info_target_luminance(void *data, struct wp_image_description_info_v1 *info, uint32_t min_lum, uint32_t max_lum) {
	((Describe *)data)->color.target_max_lum = max_lum;
}

static void info_target_max_cll(void *data, struct wp_image_description_info_v1 *info, uint32_t max_cll) {
}

static void info_target_max_fall(void *data, struct wp_image_description_info_v1 *info, uint32_t max_fall) {
}

static void info_done(void *data, struct wp_image_description_info_v1 *info) {
	((Describe *)data)->done = TRUE;
}

static const struct wp_image_description_info_v1_listener info_listener = {
	.done = info_done,
	.icc_file = info_icc_file,
	.primaries = info_primaries,
	.primaries_named = info_primaries_named,
	.tf_power = info_tf_power,
	.tf_named = info_tf_named,
	.luminances = info_luminances,
	.target_primaries = info_target_primaries,
	.target_luminance = info_target_luminance,
	.target_max_cll = info_target_max_cll,
	.target_max_fall = info_target_max_fall,
};

static void roundtrip(struct wl_display *display, struct wl_event_queue *queue) {
	if (queue != NULL) {
		wl_display_roundtrip_queue(display, queue);
	} else {
		wl_display_roundtrip(display);
	}
}

// describe_output asks the compositor for the image description of an output and reads its
// transfer function and luminances. manager lives on queue (NULL for the default queue). The
// result is known only when the whole description arrived.
static void describe_output(struct wl_display *display, struct wl_event_queue *queue, struct wp_color_manager_v1 *manager,
                            struct wl_output *output, OssOutputColor *result) {
	Describe state;
	memset(&state, 0, sizeof state);
	struct wp_color_management_output_v1 *color_output = wp_color_manager_v1_get_output(manager, output);
	struct wp_image_description_v1 *description = wp_color_management_output_v1_get_image_description(color_output);
	wp_image_description_v1_add_listener(description, &description_listener, &state);
	for (int i = 0; i < ROUNDTRIPS && !state.ready && !state.failed; i++) {
		roundtrip(display, queue);
	}
	if (state.ready) {
		struct wp_image_description_info_v1 *info = wp_image_description_v1_get_information(description);
		wp_image_description_info_v1_add_listener(info, &info_listener, &state);
		for (int i = 0; i < ROUNDTRIPS && !state.done; i++) {
			roundtrip(display, queue);
		}
		if (!state.done) {
			// The listener data lives on this stack: no event may reach it after the return.
			wl_proxy_destroy((struct wl_proxy *)info);
		}
	}
	wp_image_description_v1_destroy(description);
	wp_color_management_output_v1_destroy(color_output);
	state.color.known = state.ready && state.done;
	*result = state.color;
}

typedef struct {
	struct wp_color_manager_v1 *manager;
	struct wl_output *outputs[MAX_OUTPUTS];
	int count;
} Globals;

static void registry_global(void *data, struct wl_registry *registry, uint32_t name, const char *interface, uint32_t version) {
	Globals *globals = data;
	if (strcmp(interface, "wp_color_manager_v1") == 0) {
		// Version 1 has everything the description of an output needs.
		globals->manager = wl_registry_bind(registry, name, &wp_color_manager_v1_interface, 1);
	} else if (strcmp(interface, "wl_output") == 0 && globals->count < MAX_OUTPUTS) {
		globals->outputs[globals->count++] = wl_registry_bind(registry, name, &wl_output_interface, 1);
	}
}

static void registry_global_remove(void *data, struct wl_registry *registry, uint32_t name) {
}

static const struct wl_registry_listener registry_listener = {
	.global = registry_global,
	.global_remove = registry_global_remove,
};

int oss_wl_probe_outputs(OssOutputColor *out, int max) {
	struct wl_display *display = wl_display_connect(NULL);
	if (display == NULL) {
		return -1;
	}
	Globals globals;
	memset(&globals, 0, sizeof globals);
	struct wl_registry *registry = wl_display_get_registry(display);
	wl_registry_add_listener(registry, &registry_listener, &globals);
	wl_display_roundtrip(display);
	int written = -1;
	if (globals.manager != NULL) {
		wl_display_roundtrip(display);
		written = 0;
		for (int i = 0; i < globals.count && written < max; i++) {
			describe_output(display, NULL, globals.manager, globals.outputs[i], &out[written++]);
		}
	}
	// Disconnecting frees every object bound here.
	wl_display_disconnect(display);
	return written;
}

// The color manager of GDK's connection lives on a queue of its own, so that GDK's handlers never
// run inside a call here, and is kept for the next ask.
static struct wl_event_queue *window_queue;
static struct wp_color_manager_v1 *window_manager;
static gboolean window_probed;

static void bind_window_manager(GdkDisplay *display) {
	if (window_probed) {
		return;
	}
	window_probed = TRUE;
	struct wl_display *wl_display = gdk_wayland_display_get_wl_display(display);
	if (wl_display == NULL) {
		return;
	}
	window_queue = wl_display_create_queue(wl_display);
	struct wl_display *wrapper = wl_proxy_create_wrapper(wl_display);
	wl_proxy_set_queue((struct wl_proxy *)wrapper, window_queue);
	struct wl_registry *registry = wl_display_get_registry(wrapper);
	wl_proxy_wrapper_destroy(wrapper);
	Globals globals;
	memset(&globals, 0, sizeof globals);
	globals.count = MAX_OUTPUTS; // outputs are GDK's, not bound here
	wl_registry_add_listener(registry, &registry_listener, &globals);
	wl_display_roundtrip_queue(wl_display, window_queue);
	window_manager = globals.manager;
	wl_registry_destroy(registry);
	if (window_manager != NULL) {
		wl_display_roundtrip_queue(wl_display, window_queue);
	}
}

// active_monitor is the monitor of the window the person is using: the active one, else the first
// that is on screen, else the first monitor there is.
static GdkMonitor *active_monitor(GdkDisplay *display) {
	GdkMonitor *found = NULL;
	GListModel *windows = gtk_window_get_toplevels();
	for (guint i = 0; i < g_list_model_get_n_items(windows); i++) {
		GtkWindow *window = g_list_model_get_item(windows, i);
		GdkSurface *surface = gtk_native_get_surface(GTK_NATIVE(window));
		if (surface != NULL && (found == NULL || gtk_window_is_active(window))) {
			found = gdk_display_get_monitor_at_surface(display, surface);
		}
		gboolean active = gtk_window_is_active(window);
		g_object_unref(window);
		if (found != NULL && active) {
			break;
		}
	}
	if (found == NULL) {
		GListModel *monitors = gdk_display_get_monitors(display);
		if (g_list_model_get_n_items(monitors) > 0) {
			found = g_list_model_get_item(monitors, 0);
			g_object_unref(found);
		}
	}
	return found;
}

int oss_wl_window_output(OssOutputColor *out) {
	GdkDisplay *display = gdk_display_get_default();
	if (display == NULL || !GDK_IS_WAYLAND_DISPLAY(display)) {
		return 0;
	}
	bind_window_manager(display);
	if (window_manager == NULL) {
		return 0;
	}
	GdkMonitor *monitor = active_monitor(display);
	struct wl_output *output = monitor != NULL ? gdk_wayland_monitor_get_wl_output(monitor) : NULL;
	if (output == NULL) {
		return 0;
	}
	describe_output(gdk_wayland_display_get_wl_display(display), window_queue, window_manager, output, out);
	return out->known;
}

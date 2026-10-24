//go:build linux

#include "x11.h"

#include <glib-unix.h>
#include <math.h>
#include <string.h>

// GDK 4.18 deprecates the X11 handles. They are still the only way to reach the
// XID, which XFixes needs, so the warnings are silenced here.
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"

#include <gdk/x11/gdkx.h>
#include <X11/extensions/Xfixes.h>
#include <X11/extensions/XInput2.h>

#include "_cgo_export.h"
#include "wayland.h"
#include "x11math.h"

static PointerBarrier barriers[4];
static void warp_to_middle(GtkWidget *widget);
static int barrier_count = 0;
// barrier_box is the window box (left, top, right, bottom) the barriers stand on.
static int barrier_box[4];
static Display *barrier_display = NULL;
static GdkDisplay *barrier_gdk_display = NULL;

// Blocking X requests (each waits for the server's answer) are counted so a
// test and the debug log can show what the lock costs per tick.
static unsigned long round_trips = 0, geometry_ticks = 0;
#define ROUND_TRIP() (round_trips++)

unsigned long oss_x11_round_trips(void) {
	return round_trips;
}

unsigned long oss_x11_geometry_ticks(void) {
	return geometry_ticks;
}

// ---- window geometry cache ----
//
// The lock needs the window's place and the pointer every frame, but asking the
// server costs a round trip each. A second connection watches the window for
// ConfigureNotify (moved, resized), Reparent, Map, Unmap and the pointer
// crossing it, and only then is the server asked again. A slow refresh stays
// as a fallback in case an event is missed or the watch could not be made.

#define REFRESH_US (250 * 1000)

static struct {
	gboolean have_window, window_dirty, have_pointer, pointer_dirty, lost;
	int width, height, origin_x, origin_y, pointer_x, pointer_y;
	gint64 window_at, pointer_at;
} cache;

static Display *watch_display = NULL;
static Window watch_window = None;
static guint watch_source = 0;
static gboolean watch_failed = FALSE;
static Window vanished_window = None;

static int watch_error(Display *display, XErrorEvent *event) {
	watch_failed = TRUE;
	return 0;
}

static void watch_drain(void) {
	while (watch_display != NULL && XPending(watch_display)) {
		XEvent event;
		XNextEvent(watch_display, &event);
		switch (event.type) {
		case DestroyNotify:
			if (event.xdestroywindow.window == watch_window) {
				cache.lost = TRUE;
			}
			break;
		case ConfigureNotify:
		case ReparentNotify:
		case MapNotify:
		case UnmapNotify:
			cache.window_dirty = TRUE;
			cache.pointer_dirty = TRUE;
			break;
		case EnterNotify:
		case LeaveNotify:
			cache.pointer_dirty = TRUE;
			break;
		}
	}
}

static gboolean watch_ready(gint fd, GIOCondition condition, gpointer data) {
	watch_drain();
	return G_SOURCE_CONTINUE;
}

static void watch_close(void) {
	if (watch_source != 0) {
		g_source_remove(watch_source);
		watch_source = 0;
	}
	if (watch_display != NULL) {
		XCloseDisplay(watch_display);
		watch_display = NULL;
	}
	watch_window = None;
}

// watch_open watches the window on a connection of its own, which leaves
// GDK's event selection alone. Failing is fine: the slow refresh covers it.
static void watch_open(Display *main_display, Window window) {
	if (watch_display != NULL && watch_window == window) {
		return;
	}
	watch_close();
	watch_display = XOpenDisplay(XDisplayString(main_display));
	if (watch_display == NULL) {
		return;
	}
	// A destroyed window is an X error on this connection, which would end the
	// program by default, so it is caught for the one request that names it.
	watch_failed = FALSE;
	XErrorHandler previous = XSetErrorHandler(watch_error);
	XSelectInput(watch_display, window, StructureNotifyMask | EnterWindowMask | LeaveWindowMask);
	XSync(watch_display, False);
	ROUND_TRIP();
	XSetErrorHandler(previous);
	if (watch_failed) {
		watch_close();
		return;
	}
	watch_window = window;
	watch_source = g_unix_fd_add(ConnectionNumber(watch_display), G_IO_IN, watch_ready, NULL);
}

// read_window asks the server for the window's size and place. A window that is
// gone is an X error, which would end the program, so it runs in a trap and a
// failure marks the lock lost.
static gboolean read_window(GdkSurface *surface) {
	GdkDisplay *gdk_display = gdk_surface_get_display(surface);
	Display *display = gdk_x11_display_get_xdisplay(gdk_display);
	Window window = vanished_window != None ? vanished_window : gdk_x11_surface_get_xid(surface), root = DefaultRootWindow(display), child;
	XWindowAttributes attributes;
	int x, y;
	gdk_x11_display_error_trap_push(gdk_display);
	ROUND_TRIP();
	Status found = XGetWindowAttributes(display, window, &attributes);
	if (found) {
		ROUND_TRIP();
		found = XTranslateCoordinates(display, window, root, 0, 0, &x, &y, &child);
	}
	gdk_x11_display_error_trap_pop_ignored(gdk_display);
	if (!found) {
		cache.lost = TRUE;
		return FALSE;
	}
	cache.have_window = TRUE;
	cache.window_dirty = FALSE;
	cache.window_at = g_get_monotonic_time();
	cache.width = attributes.width;
	cache.height = attributes.height;
	cache.origin_x = x;
	cache.origin_y = y;
	return TRUE;
}

static gboolean read_pointer(GdkSurface *surface) {
	GdkDisplay *gdk_display = gdk_surface_get_display(surface);
	Display *display = gdk_x11_display_get_xdisplay(gdk_display);
	Window root_return, child_return;
	int win_x, win_y, x, y;
	unsigned int mask;
	gdk_x11_display_error_trap_push(gdk_display);
	ROUND_TRIP();
	Bool found = XQueryPointer(display, DefaultRootWindow(display), &root_return, &child_return, &x, &y, &win_x, &win_y, &mask);
	gdk_x11_display_error_trap_pop_ignored(gdk_display);
	if (!found) {
		cache.lost = TRUE;
		return FALSE;
	}
	cache.have_pointer = TRUE;
	cache.pointer_dirty = FALSE;
	cache.pointer_at = g_get_monotonic_time();
	cache.pointer_x = x;
	cache.pointer_y = y;
	return TRUE;
}

// fresh_window and fresh_pointer ask the server only when an event said the
// cache is out of date, or the fallback interval has passed.
static gboolean fresh_window(GdkSurface *surface) {
	watch_drain();
	if (cache.lost) {
		return FALSE;
	}
	if (cache.have_window && !cache.window_dirty && g_get_monotonic_time() - cache.window_at < REFRESH_US) {
		return TRUE;
	}
	return read_window(surface);
}

static gboolean fresh_pointer(GdkSurface *surface) {
	if (cache.lost) {
		return FALSE;
	}
	if (cache.have_pointer && !cache.pointer_dirty && g_get_monotonic_time() - cache.pointer_at < REFRESH_US) {
		return TRUE;
	}
	return read_pointer(surface);
}

static void drop_barriers(void) {
	if (barrier_count == 0) {
		return;
	}
	gdk_x11_display_error_trap_push(barrier_gdk_display);
	for (int i = 0; i < barrier_count; i++) {
		XFixesDestroyPointerBarrier(barrier_display, barriers[i]);
	}
	XSync(barrier_display, False);
	ROUND_TRIP();
	gdk_x11_display_error_trap_pop_ignored(barrier_gdk_display);
	barrier_count = 0;
}

gboolean oss_x11_confined(void) {
	return barrier_count > 0;
}

gboolean oss_x11_confine(GtkWidget *widget, gboolean on) {
	if (!on) {
		drop_barriers();
		watch_close();
		vanished_window = None;
		memset(&cache, 0, sizeof(cache));
		return TRUE;
	}
	drop_barriers();
	GtkNative *native = gtk_widget_get_native(widget);
	if (native == NULL) {
		return FALSE;
	}
	GdkSurface *surface = gtk_native_get_surface(native);
	if (surface == NULL || !GDK_IS_X11_SURFACE(surface)) {
		return FALSE;
	}
	Display *display = gdk_x11_display_get_xdisplay(gdk_surface_get_display(surface));
	Window window = gdk_x11_surface_get_xid(surface);
	int event_base, error_base, major = 5, minor = 0;
	if (!XFixesQueryExtension(display, &event_base, &error_base) || !XFixesQueryVersion(display, &major, &minor) || major < 5) {
		return FALSE;
	}
	if (watch_window != window) {
		memset(&cache, 0, sizeof(cache));
	}
	cache.lost = FALSE;
	watch_open(display, window);
	if (!read_window(surface)) {
		return FALSE;
	}
	cache.pointer_dirty = TRUE;
	// Barrier coordinates are unsigned, and a window with its shadow can reach past the screen.
	int x = MAX(cache.origin_x, 1), y = MAX(cache.origin_y, 1);
	int right = MAX(cache.origin_x + cache.width, 1), bottom = MAX(cache.origin_y + cache.height, 1);
	// The directions of a barrier are the ones it lets through, so each side
	// passes only motion into the window and blocks motion out of it. Each side
	// runs one pixel past its corners so a diagonal move cannot slip through.
	// An X error would end the program, so the requests run in a trap.
	Window root = DefaultRootWindow(display);
	GdkDisplay *gdk_display = gdk_surface_get_display(surface);
	gdk_x11_display_error_trap_push(gdk_display);
	barriers[0] = XFixesCreatePointerBarrier(display, root, x - 1, y, right + 1, y, BarrierPositiveY, 0, NULL);
	barriers[1] = XFixesCreatePointerBarrier(display, root, x - 1, bottom, right + 1, bottom, BarrierNegativeY, 0, NULL);
	barriers[2] = XFixesCreatePointerBarrier(display, root, x, y - 1, x, bottom + 1, BarrierPositiveX, 0, NULL);
	barriers[3] = XFixesCreatePointerBarrier(display, root, right, y - 1, right, bottom + 1, BarrierNegativeX, 0, NULL);
	XSync(display, False);
	ROUND_TRIP();
	if (gdk_x11_display_error_trap_pop(gdk_display) != 0) {
		return FALSE;
	}
	barrier_display = display;
	barrier_gdk_display = gdk_display;
	barrier_count = 4;
	barrier_box[0] = x;
	barrier_box[1] = y;
	barrier_box[2] = right;
	barrier_box[3] = bottom;
	return TRUE;
}

// picture_rect is the picture widget in root pixels, from the cached window place.
static gboolean picture_rect(GtkWidget *widget, GtkNative *native, GdkSurface *surface, oss_rect *rect) {
	graphene_rect_t bounds;
	if (!gtk_widget_compute_bounds(widget, GTK_WIDGET(native), &bounds)) {
		return FALSE;
	}
	double offset_x = 0, offset_y = 0;
	gtk_native_get_surface_transform(native, &offset_x, &offset_y);
	*rect = oss_picture_rect(cache.origin_x, cache.origin_y, bounds.origin.x, bounds.origin.y, bounds.size.width, bounds.size.height, offset_x, offset_y, gdk_surface_get_scale_factor(surface));
	return TRUE;
}

int oss_x11_geometry(GtkWidget *widget, int geometry[OSS_GEOMETRY_LEN]) {
	GtkNative *native = gtk_widget_get_native(widget);
	GdkSurface *surface = native != NULL ? gtk_native_get_surface(native) : NULL;
	if (barrier_count == 0 || surface == NULL || !GDK_IS_X11_SURFACE(surface)) {
		return OSS_GEOMETRY_SKIP;
	}
	geometry_ticks++;
	if (!fresh_window(surface) || !fresh_pointer(surface)) {
		return OSS_GEOMETRY_LOST;
	}
	oss_rect picture;
	if (!picture_rect(widget, native, surface, &picture)) {
		return OSS_GEOMETRY_SKIP;
	}
	geometry[0] = MAX(cache.origin_x, 1);
	geometry[1] = MAX(cache.origin_y, 1);
	geometry[2] = MAX(cache.origin_x + cache.width, 1);
	geometry[3] = MAX(cache.origin_y + cache.height, 1);
	geometry[4] = picture.x;
	geometry[5] = picture.y;
	geometry[6] = picture.width;
	geometry[7] = picture.height;
	geometry[8] = cache.pointer_x;
	geometry[9] = cache.pointer_y;
	memcpy(geometry + 10, barrier_box, sizeof(barrier_box));
	return OSS_GEOMETRY_OK;
}

// oss_x11_test_vanish makes the lock read a window that does not exist, as one
// destroyed mid-unmap is, and stops the watch so only the request itself finds
// out. The X error is the real one; the window GDK holds stays untouched. It
// exists for the test of a lock that outlives its window.
void oss_x11_test_vanish(void) {
	watch_close();
	vanished_window = 0x7ffffff0;
	cache.window_dirty = TRUE;
}

void oss_x11_center(GtkWidget *widget) {
	warp_to_middle(widget);
}

int oss_x11_caps(void) {
	GdkDisplay *gdk_display = gdk_display_get_default();
	if (gdk_display == NULL || !GDK_IS_X11_DISPLAY(gdk_display)) {
		return 0;
	}
	Display *display = gdk_x11_display_get_xdisplay(gdk_display);
	int caps = OSS_CAP_X11;
	int event_base, error_base, major = 5, minor = 0;
	if (XFixesQueryExtension(display, &event_base, &error_base) && XFixesQueryVersion(display, &major, &minor) && major >= 5) {
		caps |= OSS_CAP_XFIXES;
	}
	// GDK has already negotiated XInput 2 on this connection (asking again for a
	// lower version is an X error), so only the extension's presence is checked here;
	// oss_x11_lock negotiates on its own connection and falls back when that fails.
	int opcode;
	if (XQueryExtension(display, "XInputExtension", &opcode, &event_base, &error_base)) {
		caps |= OSS_CAP_XINPUT2;
	}
	return caps;
}

// ---- raw motion ----

// A device that reports absolute positions (a tablet, a virtual machine's
// pointer) is turned into deltas here, scaled to the screen.
typedef struct {
	int id;
	// absolute is set when both axes report positions; a device with only one
	// such axis is ignored (skip), as its motion cannot be told apart.
	gboolean absolute, skip;
	double min[2], max[2];
	double last[2];
	gboolean have_last[2];
} RawDevice;

#define RAW_DEVICES 32

static Display *raw_display = NULL;
static int raw_opcode = 0;
static guint raw_source = 0;
static RawDevice raw_devices[RAW_DEVICES];
static int raw_device_count = 0;
static GtkWidget *raw_widget = NULL;

static RawDevice *raw_device(int id) {
	for (int i = 0; i < raw_device_count; i++) {
		if (raw_devices[i].id == id) {
			return &raw_devices[i];
		}
	}
	if (raw_device_count == RAW_DEVICES) {
		raw_device_count = 0;
	}
	RawDevice *device = &raw_devices[raw_device_count++];
	memset(device, 0, sizeof(*device));
	device->id = id;
	int count = 0;
	XIDeviceInfo *info = XIQueryDevice(raw_display, id, &count);
	int absolute_axes = 0;
	for (int i = 0; info != NULL && i < count; i++) {
		for (int j = 0; j < info[i].num_classes; j++) {
			if (info[i].classes[j]->type != XIValuatorClass) {
				continue;
			}
			XIValuatorClassInfo *axis = (XIValuatorClassInfo *)info[i].classes[j];
			if (axis->number < 2 && axis->mode == XIModeAbsolute && axis->max > axis->min) {
				absolute_axes |= 1 << axis->number;
				device->min[axis->number] = axis->min;
				device->max[axis->number] = axis->max;
			}
		}
	}
	if (info != NULL) {
		XIFreeDeviceInfo(info);
	}
	device->absolute = absolute_axes == 3;
	device->skip = absolute_axes == 1 || absolute_axes == 2;
	return device;
}

// raw_delta adds the motion of one raw event to dx, dy, in device pixels.
static void raw_delta(XIRawEvent *raw, double *dx, double *dy) {
	RawDevice *device = raw_device(raw->sourceid);
	if (device->skip) {
		return;
	}
	double *accelerated = raw->valuators.values;
	double *plain = raw->raw_values;
	int screen_size[2] = {DisplayWidth(raw_display, DefaultScreen(raw_display)), DisplayHeight(raw_display, DefaultScreen(raw_display))};
	for (int i = 0; i < raw->valuators.mask_len * 8; i++) {
		if (!XIMaskIsSet(raw->valuators.mask, i)) {
			continue;
		}
		double value = *accelerated++, base = *plain++;
		if (i > 1) {
			continue;
		}
		double delta = value;
		if (device->absolute) {
			delta = 0;
			if (device->have_last[i]) {
				double range = device->max[i] - device->min[i];
				delta = range > 0 ? (base - device->last[i]) * screen_size[i] / range : 0;
			}
			device->last[i] = base;
			device->have_last[i] = TRUE;
		}
		if (i == 0) {
			*dx += delta;
		} else {
			*dy += delta;
		}
	}
}

// warp_to puts the pointer at (x, y), in window units, on GDK's connection.
static gboolean warp_to(GtkWidget *widget, double x, double y) {
	GtkNative *native = gtk_widget_get_native(widget);
	GdkSurface *surface = native != NULL ? gtk_native_get_surface(native) : NULL;
	if (surface == NULL || !GDK_IS_X11_SURFACE(surface)) {
		return FALSE;
	}
	GdkDisplay *gdk_display = gdk_surface_get_display(surface);
	Display *display = gdk_x11_display_get_xdisplay(gdk_display);
	double offset_x = 0, offset_y = 0;
	gtk_native_get_surface_transform(native, &offset_x, &offset_y);
	double scale = gdk_surface_get_scale_factor(surface);
	gdk_x11_display_error_trap_push(gdk_display);
	XWarpPointer(display, None, gdk_x11_surface_get_xid(surface), 0, 0, 0, 0, (int)lround((x + offset_x) * scale), (int)lround((y + offset_y) * scale));
	XFlush(display);
	gdk_x11_display_error_trap_pop_ignored(gdk_display);
	return TRUE;
}

// warp_to_middle puts the pointer in the middle of the part of the picture that
// is on the screen: the picture of a window partly off the screen has a middle
// the pointer cannot reach, and a barrier would hold it at the edge instead.
static void warp_to_middle(GtkWidget *widget) {
	GtkNative *native = gtk_widget_get_native(widget);
	GdkSurface *surface = native != NULL ? gtk_native_get_surface(native) : NULL;
	if (surface == NULL || !GDK_IS_X11_SURFACE(surface) || !fresh_window(surface)) {
		return;
	}
	oss_rect picture;
	// A widget not yet laid out has no middle; the lock's settle step centres it later.
	if (!picture_rect(widget, native, surface, &picture) || picture.width <= 0 || picture.height <= 0) {
		return;
	}
	GdkDisplay *gdk_display = gdk_surface_get_display(surface);
	oss_rect screen = picture;
	GdkMonitor *monitor = gdk_display_get_monitor_at_surface(gdk_display, surface);
	if (monitor != NULL) {
		GdkRectangle area;
		gdk_monitor_get_geometry(monitor, &area);
		int scale = MAX(gdk_monitor_get_scale_factor(monitor), 1);
		screen = (oss_rect){area.x * scale, area.y * scale, area.width * scale, area.height * scale};
	}
	int x, y;
	if (!oss_visible_middle(picture, screen, &x, &y)) {
		return;
	}
	Display *display = gdk_x11_display_get_xdisplay(gdk_display);
	gdk_x11_display_error_trap_push(gdk_display);
	XWarpPointer(display, None, DefaultRootWindow(display), 0, 0, 0, 0, x, y);
	XFlush(display);
	gdk_x11_display_error_trap_pop_ignored(gdk_display);
}

static gboolean raw_ready(gint fd, GIOCondition condition, gpointer data) {
	double dx = 0, dy = 0;
	gboolean moved = FALSE;
	while (raw_display != NULL && XPending(raw_display)) {
		XEvent event;
		XNextEvent(raw_display, &event);
		if (event.type != GenericEvent || event.xcookie.extension != raw_opcode || !XGetEventData(raw_display, &event.xcookie)) {
			continue;
		}
		if (event.xcookie.evtype == XI_RawMotion) {
			raw_delta((XIRawEvent *)event.xcookie.data, &dx, &dy);
			moved = TRUE;
		}
		XFreeEventData(raw_display, &event.xcookie);
	}
	if (moved && raw_widget != NULL) {
		int scale = MAX(gtk_widget_get_scale_factor(raw_widget), 1);
		warp_to_middle(raw_widget);
		if (isfinite(dx) && isfinite(dy)) {
			ossPointerMotion(dx / scale, dy / scale);
		}
	}
	return G_SOURCE_CONTINUE;
}

static void raw_close(void) {
	if (raw_source != 0) {
		g_source_remove(raw_source);
		raw_source = 0;
	}
	if (raw_display != NULL) {
		XCloseDisplay(raw_display);
		raw_display = NULL;
	}
	raw_widget = NULL;
	raw_device_count = 0;
}

int oss_x11_lock(GtkWidget *widget, int *major_out, int *minor_out) {
	raw_close();
	GdkDisplay *gdk_display = gdk_display_get_default();
	if (gdk_display == NULL || !GDK_IS_X11_DISPLAY(gdk_display)) {
		return OSS_LOCK_NO_XINPUT;
	}
	Display *main_display = gdk_x11_display_get_xdisplay(gdk_display);
	// A connection of its own keeps GDK's event stream untouched.
	raw_display = XOpenDisplay(XDisplayString(main_display));
	if (raw_display == NULL) {
		return OSS_LOCK_NO_XINPUT;
	}
	// The server answers with the highest version it has when that is lower.
	int event_base, error_base, major = 2, minor = 2;
	if (!XQueryExtension(raw_display, "XInputExtension", &raw_opcode, &event_base, &error_base) || XIQueryVersion(raw_display, &major, &minor) != Success) {
		raw_close();
		return OSS_LOCK_NO_XINPUT;
	}
	*major_out = major;
	*minor_out = minor;
	unsigned char bits[XIMaskLen(XI_LASTEVENT)] = {0};
	XISetMask(bits, XI_RawMotion);
	XIEventMask mask = {.deviceid = XIAllMasterDevices, .mask_len = sizeof(bits), .mask = bits};
	XISelectEvents(raw_display, DefaultRootWindow(raw_display), &mask, 1);
	XFlush(raw_display);
	// Without barriers nothing holds the pointer, so the lock is not made at all.
	if (!oss_x11_confine(widget, TRUE)) {
		raw_close();
		return OSS_LOCK_NO_BARRIERS;
	}
	raw_widget = widget;
	raw_source = g_unix_fd_add(ConnectionNumber(raw_display), G_IO_IN, raw_ready, NULL);
	warp_to_middle(widget);
	return OSS_LOCK_OK;
}

gboolean oss_x11_unlock(GtkWidget *widget, gboolean has_hint, double x, double y) {
	raw_close();
	oss_x11_confine(widget, FALSE);
	if (has_hint) {
		// The point given is in the widget's own units; warp_to takes the window's.
		GtkNative *native = gtk_widget_get_native(widget);
		graphene_point_t point = GRAPHENE_POINT_INIT((float)x, (float)y), in_window;
		if (native != NULL && gtk_widget_compute_point(widget, GTK_WIDGET(native), &point, &in_window)) {
			return warp_to(widget, in_window.x, in_window.y);
		}
	}
	return FALSE;
}

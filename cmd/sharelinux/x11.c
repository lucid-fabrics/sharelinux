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
	Window root = DefaultRootWindow(display), child;
	XWindowAttributes attributes;
	int x, y;
	ROUND_TRIP();
	if (!XGetWindowAttributes(display, window, &attributes)) {
		return FALSE;
	}
	ROUND_TRIP();
	if (!XTranslateCoordinates(display, window, root, 0, 0, &x, &y, &child)) {
		return FALSE;
	}
	// Barrier coordinates are unsigned, and a window with its shadow can reach past the screen.
	int right = MAX(x + attributes.width, 1), bottom = MAX(y + attributes.height, 1);
	x = MAX(x, 1);
	y = MAX(y, 1);
	// The directions of a barrier are the ones it lets through, so each side
	// passes only motion into the window and blocks motion out of it. Each side
	// runs one pixel past its corners so a diagonal move cannot slip through.
	// An X error would end the program, so the requests run in a trap.
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

// window_box is the box oss_x11_confine builds its barriers on, in root coordinates.
static gboolean window_box(GtkWidget *widget, GdkSurface *surface, Display *display, int box[4]) {
	Window window = gdk_x11_surface_get_xid(surface), root = DefaultRootWindow(display), child;
	XWindowAttributes attributes;
	int x, y;
	ROUND_TRIP();
	if (!XGetWindowAttributes(display, window, &attributes)) {
		return FALSE;
	}
	ROUND_TRIP();
	if (!XTranslateCoordinates(display, window, root, 0, 0, &x, &y, &child)) {
		return FALSE;
	}
	box[0] = MAX(x, 1);
	box[1] = MAX(y, 1);
	box[2] = MAX(x + attributes.width, 1);
	box[3] = MAX(y + attributes.height, 1);
	return TRUE;
}

gboolean oss_x11_geometry(GtkWidget *widget, int geometry[OSS_GEOMETRY_LEN]) {
	GtkNative *native = gtk_widget_get_native(widget);
	GdkSurface *surface = native != NULL ? gtk_native_get_surface(native) : NULL;
	graphene_rect_t bounds;
	if (barrier_count == 0 || surface == NULL || !GDK_IS_X11_SURFACE(surface) || !gtk_widget_compute_bounds(widget, GTK_WIDGET(native), &bounds)) {
		return FALSE;
	}
	Display *display = gdk_x11_display_get_xdisplay(gdk_surface_get_display(surface));
	Window root = DefaultRootWindow(display), window = gdk_x11_surface_get_xid(surface), child, root_return, child_return;
	int origin_x, origin_y, pointer_x, pointer_y, win_x, win_y;
	unsigned int mask;
	geometry_ticks++;
	if (!window_box(widget, surface, display, geometry)) {
		return FALSE;
	}
	ROUND_TRIP();
	if (!XTranslateCoordinates(display, window, root, 0, 0, &origin_x, &origin_y, &child)) {
		return FALSE;
	}
	ROUND_TRIP();
	if (!XQueryPointer(display, root, &root_return, &child_return, &pointer_x, &pointer_y, &win_x, &win_y, &mask)) {
		return FALSE;
	}
	double offset_x = 0, offset_y = 0;
	gtk_native_get_surface_transform(native, &offset_x, &offset_y);
	double scale = gdk_surface_get_scale_factor(surface);
	geometry[4] = origin_x + (int)((bounds.origin.x + offset_x) * scale);
	geometry[5] = origin_y + (int)((bounds.origin.y + offset_y) * scale);
	geometry[6] = (int)(bounds.size.width * scale);
	geometry[7] = (int)(bounds.size.height * scale);
	geometry[8] = pointer_x;
	geometry[9] = pointer_y;
	memcpy(geometry + 10, barrier_box, sizeof(barrier_box));
	return TRUE;
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
static void warp_to(GtkWidget *widget, double x, double y) {
	GtkNative *native = gtk_widget_get_native(widget);
	GdkSurface *surface = native != NULL ? gtk_native_get_surface(native) : NULL;
	if (surface == NULL || !GDK_IS_X11_SURFACE(surface)) {
		return;
	}
	GdkDisplay *gdk_display = gdk_surface_get_display(surface);
	Display *display = gdk_x11_display_get_xdisplay(gdk_display);
	double offset_x = 0, offset_y = 0;
	gtk_native_get_surface_transform(native, &offset_x, &offset_y);
	double scale = gdk_surface_get_scale_factor(surface);
	gdk_x11_display_error_trap_push(gdk_display);
	XWarpPointer(display, None, gdk_x11_surface_get_xid(surface), 0, 0, 0, 0, (int)((x + offset_x) * scale), (int)((y + offset_y) * scale));
	XFlush(display);
	gdk_x11_display_error_trap_pop_ignored(gdk_display);
}

static void warp_to_middle(GtkWidget *widget) {
	graphene_rect_t bounds;
	GtkNative *native = gtk_widget_get_native(widget);
	// A widget not yet laid out has no middle; the lock's settle step centres it later.
	if (native != NULL && gtk_widget_compute_bounds(widget, GTK_WIDGET(native), &bounds) && bounds.size.width > 0 && bounds.size.height > 0) {
		warp_to(widget, bounds.origin.x + bounds.size.width / 2, bounds.origin.y + bounds.size.height / 2);
	}
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

void oss_x11_unlock(GtkWidget *widget, gboolean has_hint, double x, double y) {
	raw_close();
	oss_x11_confine(widget, FALSE);
	if (has_hint) {
		// The point given is in the widget's own units; warp_to takes the window's.
		GtkNative *native = gtk_widget_get_native(widget);
		graphene_point_t point = GRAPHENE_POINT_INIT((float)x, (float)y), in_window;
		if (native != NULL && gtk_widget_compute_point(widget, GTK_WIDGET(native), &point, &in_window)) {
			warp_to(widget, in_window.x, in_window.y);
		}
	}
}

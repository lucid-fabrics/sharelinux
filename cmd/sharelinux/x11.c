//go:build linux

#include "x11.h"

// GDK 4.18 deprecates the X11 handles. They are still the only way to reach the
// XID, which XFixes needs, so the warnings are silenced here.
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"

#include <gdk/x11/gdkx.h>
#include <X11/extensions/Xfixes.h>

static PointerBarrier barriers[4];
static int barrier_count = 0;
static Display *barrier_display = NULL;
static GdkDisplay *barrier_gdk_display = NULL;

static void drop_barriers(void) {
	if (barrier_count == 0) {
		return;
	}
	gdk_x11_display_error_trap_push(barrier_gdk_display);
	for (int i = 0; i < barrier_count; i++) {
		XFixesDestroyPointerBarrier(barrier_display, barriers[i]);
	}
	XSync(barrier_display, False);
	gdk_x11_display_error_trap_pop_ignored(barrier_gdk_display);
	barrier_count = 0;
}

gboolean oss_pointer_confinable(void) {
	GdkDisplay *display = gdk_display_get_default();
	return display != NULL && GDK_IS_X11_DISPLAY(display);
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
	if (!XGetWindowAttributes(display, window, &attributes) || !XTranslateCoordinates(display, window, root, 0, 0, &x, &y, &child)) {
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
	if (gdk_x11_display_error_trap_pop(gdk_display) != 0) {
		return FALSE;
	}
	barrier_display = display;
	barrier_gdk_display = gdk_display;
	barrier_count = 4;
	return TRUE;
}

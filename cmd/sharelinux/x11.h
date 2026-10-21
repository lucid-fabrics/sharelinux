//go:build linux

#ifndef OSS_X11_H
#define OSS_X11_H

#include <gtk/gtk.h>

// oss_x11_confine keeps the pointer inside the window (on) or lets it go (off),
// with XFixes pointer barriers around the window. It returns whether the
// window is an X11 window and the barriers could be made; on Wayland it does
// nothing and returns FALSE.
gboolean oss_x11_confine(GtkWidget *widget, gboolean on);

// oss_x11_caps returns the OSS_CAP_* bits (see wayland.h) of an X11 display.
int oss_x11_caps(void);

// oss_x11_lock holds the pointer for Exclusive mode: pointer barriers around the
// window, XInput 2 raw motion read on a connection of its own (the deltas go to
// ossPointerMotion, in window units), and the pointer warped back to the middle
// of the widget after every burst so a click always lands on it. It returns
// FALSE and changes nothing when XInput 2 is missing.
gboolean oss_x11_lock(GtkWidget *widget);

// oss_x11_unlock ends it. With has_hint the pointer is put at (x, y), in the
// widget's units.
void oss_x11_unlock(GtkWidget *widget, gboolean has_hint, double x, double y);

// oss_x11_confined reports whether barriers are up.
gboolean oss_x11_confined(void);

#endif

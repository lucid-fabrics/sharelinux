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
// of the widget after every burst so a click always lands on it. It returns an
// OSS_LOCK_* status and stores the XInput version the server agreed to; with
// anything but OSS_LOCK_OK nothing is left running. The caller judges the version. XInput 2.2 is asked for,
// since a 2.0 client gets no raw events while a button is held.
#define OSS_LOCK_OK 0
#define OSS_LOCK_NO_XINPUT 1
#define OSS_LOCK_NO_BARRIERS 2
int oss_x11_lock(GtkWidget *widget, int *major, int *minor);

// oss_x11_unlock ends it. With has_hint the pointer is put at (x, y), in the
// widget's units.
void oss_x11_unlock(GtkWidget *widget, gboolean has_hint, double x, double y);

// oss_x11_geometry reads what the lock needs to judge itself, in root pixels:
// [0..3] the window box (left, top, right, bottom) as it is now, [4..7] the
// picture widget (x, y, width, height), [8..9] the pointer, [10..13] the box the
// barriers stand on. It returns FALSE when no barriers are up or anything
// could not be read.
#define OSS_GEOMETRY_LEN 14
gboolean oss_x11_geometry(GtkWidget *widget, int geometry[OSS_GEOMETRY_LEN]);

// oss_x11_center warps the pointer to the middle of the widget.
void oss_x11_center(GtkWidget *widget);

// oss_x11_confined reports whether barriers are up.
gboolean oss_x11_confined(void);

#endif

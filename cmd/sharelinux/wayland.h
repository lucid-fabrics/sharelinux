//go:build linux

#ifndef OSS_WAYLAND_H
#define OSS_WAYLAND_H

#include <gtk/gtk.h>

// Capability bits of oss_lock_caps.
#define OSS_CAP_WAYLAND 1
#define OSS_CAP_CONSTRAINTS 2
#define OSS_CAP_RELATIVE 4
#define OSS_CAP_X11 8
#define OSS_CAP_XFIXES 16
#define OSS_CAP_XINPUT2 32

// oss_lock_caps says which pointer-holding features the display server has. On
// Wayland the first call binds zwp_pointer_constraints_v1 and
// zwp_relative_pointer_manager_v1 from the registry of GDK's connection.
int oss_lock_caps(void);

// oss_wl_lock locks the pointer to the widget's rectangle of its surface; the
// compositor begins the lock once the pointer is inside it. The compositor decides
// when the lock begins: locked state changes go to ossPointerLockState, and the
// relative motion to ossPointerMotion. It returns whether the request was made.
gboolean oss_wl_lock(GtkWidget *widget);

// oss_wl_refresh fits the region of a lock to the widget's place again after it
// moved or changed size. It does nothing when the place is the same.
void oss_wl_refresh(GtkWidget *widget);

// oss_wl_live_regions counts the wl_region objects not yet destroyed, for the leak test.
int oss_wl_live_regions(void);

// oss_wl_unlock drops the lock, or the request for one. With has_hint the
// compositor is asked to put the pointer at (x, y) in the widget's units afterwards.
void oss_wl_unlock(GtkWidget *widget, gboolean has_hint, double x, double y);

#endif

//go:build linux

#ifndef OSS_X11_H
#define OSS_X11_H

#include <gtk/gtk.h>

// oss_x11_confine keeps the pointer inside the window (on) or lets it go (off),
// with XFixes pointer barriers around the window. It returns whether the
// window is an X11 window and the barriers could be made; on Wayland it does
// nothing and returns FALSE.
gboolean oss_x11_confine(GtkWidget *widget, gboolean on);

// oss_x11_confined reports whether barriers are up.
gboolean oss_x11_confined(void);

#endif

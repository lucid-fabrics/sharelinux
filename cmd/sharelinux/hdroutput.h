//go:build linux

#ifndef OSS_HDROUTPUT_H
#define OSS_HDROUTPUT_H

#include <gtk/gtk.h>

// OssOutputColor is what a Wayland compositor says about the colors of one output: the transfer
// function (a wp_color_manager_v1 code point, 0 when none is named) and the luminances in cd/m2.
typedef struct {
	// known is set when the compositor described the output in full.
	int known;
	unsigned transfer;
	unsigned min_lum, max_lum, reference_lum;
	// target_max_lum is 0 when the description names no target luminance.
	unsigned target_max_lum;
} OssOutputColor;

// oss_wl_probe_outputs describes every output on a connection of its own, so it works before GTK
// starts. It returns how many it wrote to out (at most max), or -1 when there is no Wayland
// display or the compositor has no wp_color_manager_v1.
int oss_wl_probe_outputs(OssOutputColor *out, int max);

// oss_wl_window_output describes the output the active window is on, on GDK's connection. It
// returns 0 when the compositor cannot say. GTK thread only.
int oss_wl_window_output(OssOutputColor *out);

#endif

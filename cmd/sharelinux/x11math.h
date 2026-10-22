//go:build linux

#ifndef OSS_X11_MATH_H
#define OSS_X11_MATH_H

#include <glib.h>

// A box in root pixels.
typedef struct {
	int x, y, width, height;
} oss_rect;

// oss_picture_rect is the picture widget in root pixels: the window's origin
// in the root, plus the widget's bounds inside the window and the surface
// transform (both in GTK units), times the window's scale factor.
static inline oss_rect oss_picture_rect(int origin_x, int origin_y, double bounds_x, double bounds_y, double bounds_width, double bounds_height, double offset_x, double offset_y, double scale) {
	oss_rect rect = {
		origin_x + (int)((bounds_x + offset_x) * scale),
		origin_y + (int)((bounds_y + offset_y) * scale),
		(int)(bounds_width * scale),
		(int)(bounds_height * scale),
	};
	return rect;
}

// oss_visible_middle finds the middle of the part of the picture that lies on
// the monitor, so a window partly off the screen is not warped to a point the
// pointer cannot reach. It returns FALSE when no part is visible.
static inline gboolean oss_visible_middle(oss_rect picture, oss_rect monitor, int *x, int *y) {
	int left = picture.x > monitor.x ? picture.x : monitor.x;
	int top = picture.y > monitor.y ? picture.y : monitor.y;
	int right = picture.x + picture.width < monitor.x + monitor.width ? picture.x + picture.width : monitor.x + monitor.width;
	int bottom = picture.y + picture.height < monitor.y + monitor.height ? picture.y + picture.height : monitor.y + monitor.height;
	if (right <= left || bottom <= top) {
		return FALSE;
	}
	*x = left + (right - left) / 2;
	*y = top + (bottom - top) / 2;
	return TRUE;
}

#endif

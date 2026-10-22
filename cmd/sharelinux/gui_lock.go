//go:build linux

package main

/*
#include "wayland.h"
#include "x11.h"
#include "x11math.h"
*/
import "C"

// lockSink receives what the display server's pointer lock reports. It is set
// while a session holds the lock and is only touched from the GTK thread.
var lockSink struct {
	motion func(dx, dy float64)
	state  func(locked bool)
}

//export ossPointerMotion
func ossPointerMotion(dx, dy C.double) {
	if lockSink.motion != nil {
		lockSink.motion(float64(dx), float64(dy))
	}
}

//export ossPointerLockState
func ossPointerLockState(locked C.int) {
	if lockSink.state != nil {
		lockSink.state(locked != 0)
	}
}

// detectLockBackend asks the display server how Exclusive mode can hold the pointer.
func detectLockBackend() lockBackend {
	bits := int(C.oss_lock_caps())
	return chooseLockBackend(lockCaps{
		wayland:     bits&C.OSS_CAP_WAYLAND != 0,
		constraints: bits&C.OSS_CAP_CONSTRAINTS != 0,
		relativeMgr: bits&C.OSS_CAP_RELATIVE != 0,
		x11:         bits&C.OSS_CAP_X11 != 0,
		xfixes:      bits&C.OSS_CAP_XFIXES != 0,
		xinput2:     bits&C.OSS_CAP_XINPUT2 != 0,
	})
}

// lockPointer holds the pointer in the window with the given backend. It
// returns whether the lock is held at once; on Wayland the compositor answers
// later through lockSink.state. False means the pointer is not held.
func (s *screenView) lockPointer(backend lockBackend) (held bool, requested bool) {
	switch backend {
	case lockWayland:
		requested = C.oss_wl_lock(s.c) != 0
		return false, requested
	case lockX11:
		var major, minor C.int
		status := lockStatus(C.oss_x11_lock(s.c, &major, &minor))
		if status == lockStatusOK && !xinputUsable(int(major), int(minor)) {
			// Raw motion would stop while a button is held: not good enough.
			C.oss_x11_unlock(s.c, 0, 0, 0)
			status = lockStatusNoXInput
		}
		// No raw motion: the barriers still keep the pointer inside.
		return x11LockResult(status, func() bool { return C.oss_x11_confine(s.c, 1) != 0 })
	case lockConfine:
		return false, C.oss_x11_confine(s.c, 1) != 0
	}
	return false, false
}

// refreshLock fits the X11 barriers to the window again after it changed size.
// It returns false when the X11 barriers could not be rebuilt, which leaves the
// pointer free: the caller falls back as a lock that failed to start does.
func (s *screenView) refreshLock(backend lockBackend) bool {
	switch backend {
	case lockConfine, lockX11:
		return C.oss_x11_confine(s.c, 1) != 0
	case lockWayland:
		C.oss_wl_refresh(s.c)
	}
	return true
}

// settleLock keeps an X11 lock true to the window after it was made. A lock
// begun before the window was placed and laid out stands on a stale box and
// leaves the pointer outside the picture, so the first click would land on
// whatever is under it, the window would lose the focus and the lock would
// end. It rebuilds the barriers when the window moved and brings the pointer
// back to the picture when raw motion holds it (otherwise it moves freely).
// It returns false when the lock cannot stand any more: the window is gone or
// the barriers could not be rebuilt.
func (s *screenView) settleLock(backend lockBackend) bool {
	if backend != lockX11 && backend != lockConfine {
		return true
	}
	var raw [C.OSS_GEOMETRY_LEN]C.int
	switch C.oss_x11_geometry(s.c, &raw[0]) {
	case C.OSS_GEOMETRY_LOST:
		return false
	case C.OSS_GEOMETRY_SKIP:
		return true
	}
	var geometry lockGeometry
	for index := range raw {
		geometry[index] = int(raw[index])
	}
	rebuild, recenter := settleActions(geometry, backend == lockX11)
	if rebuild && C.oss_x11_confine(s.c, 1) == 0 {
		return false
	}
	if recenter {
		C.oss_x11_center(s.c)
	}
	return true
}

// unlockPointer lets the pointer go and puts it at (x, y) in window units, where
// the Mac's cursor is drawn, when hint is set. It is safe to call without a lock.
func (s *screenView) unlockPointer(backend lockBackend, hint bool, x, y float64) {
	value := C.gboolean(0)
	if hint {
		value = 1
	}
	switch backend {
	case lockWayland:
		C.oss_wl_unlock(s.c, value, C.double(x), C.double(y))
	case lockX11:
		C.oss_x11_unlock(s.c, value, C.double(x), C.double(y))
	case lockConfine:
		C.oss_x11_confine(s.c, 0)
	}
}

// vanishX11WindowForTest makes the lock read a window that is gone, for the test of
// a lock that outlives its window.
func vanishX11WindowForTest() {
	C.oss_x11_test_vanish()
}

// pixelRect is a box in root pixels.
type pixelRect struct{ x, y, width, height int }

// x11PictureRect is the picture widget in root pixels: the window's origin in
// the root plus the widget's bounds and the surface transform (GTK units) times
// the scale factor.
func x11PictureRect(originX, originY int, boundsX, boundsY, boundsWidth, boundsHeight, offsetX, offsetY, scale float64) pixelRect {
	r := C.oss_picture_rect(C.int(originX), C.int(originY), C.double(boundsX), C.double(boundsY), C.double(boundsWidth), C.double(boundsHeight), C.double(offsetX), C.double(offsetY), C.double(scale))
	return pixelRect{int(r.x), int(r.y), int(r.width), int(r.height)}
}

// x11VisibleMiddle is the middle of the part of the picture on the monitor.
func x11VisibleMiddle(picture, monitor pixelRect) (x, y int, ok bool) {
	var cx, cy C.int
	ok = C.oss_visible_middle(C.oss_rect{C.int(picture.x), C.int(picture.y), C.int(picture.width), C.int(picture.height)}, C.oss_rect{C.int(monitor.x), C.int(monitor.y), C.int(monitor.width), C.int(monitor.height)}, &cx, &cy) != 0
	return int(cx), int(cy), ok
}

// x11Confined reports whether X11 barriers stand.
func x11Confined() bool {
	return C.oss_x11_confined() != 0
}

// x11Cost is how many blocking X requests the lock made and how often its
// geometry was read, since the program began.
func x11Cost() (roundTrips, ticks uint64) {
	return uint64(C.oss_x11_round_trips()), uint64(C.oss_x11_geometry_ticks())
}

// waylandLiveRegions counts the wl_region objects not yet destroyed, for the leak test.
func waylandLiveRegions() int {
	return int(C.oss_wl_live_regions())
}

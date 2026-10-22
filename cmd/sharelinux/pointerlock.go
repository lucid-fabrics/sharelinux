package main

import (
	"math"
	"time"
)

// lockBackend is how Exclusive mode holds the pointer on this display server.
type lockBackend int

const (
	// lockNone cannot hold the pointer: only the keyboard is captured.
	lockNone lockBackend = iota
	// lockConfine keeps the pointer inside the window with X11 pointer barriers
	// but has no relative motion, so the Mac's pointer stops at the window edge.
	lockConfine
	// lockX11 adds XInput 2 raw motion to the barriers.
	lockX11
	// lockWayland locks the pointer with zwp_pointer_constraints_v1 and reads
	// zwp_relative_pointer_v1.
	lockWayland
)

// relative reports whether the backend delivers motion without a window edge.
func (b lockBackend) relative() bool {
	return b == lockX11 || b == lockWayland
}

// holds reports whether the backend keeps the pointer in the window at all.
func (b lockBackend) holds() bool {
	return b != lockNone
}

// lockCaps is what the display server offers, as the C probes found it.
type lockCaps struct {
	wayland     bool
	constraints bool
	relativeMgr bool
	x11         bool
	xfixes      bool
	xinput2     bool
}

// chooseLockBackend picks the strongest way to hold the pointer. Wayland needs
// both protocols: a lock without relative motion would leave no way to move.
func chooseLockBackend(caps lockCaps) lockBackend {
	switch {
	case caps.wayland:
		if caps.constraints && caps.relativeMgr {
			return lockWayland
		}
	case caps.x11:
		if caps.xinput2 {
			return lockX11
		}
		if caps.xfixes {
			return lockConfine
		}
	}
	return lockNone
}

const (
	// echoTolerance is how far the Mac may round a position we sent, in
	// framebuffer pixels, and still be taken for the echo of it.
	echoTolerance = 3
	// echoLife is how long a sent position can wait for its echo.
	echoLife = 2 * time.Second
	// echoSlots bounds the positions waiting for their echo.
	echoSlots = 64
)

type sentPoint struct {
	x, y int
	at   time.Time
}

// sent records a position sent to the Mac, so that its echo is not taken for a warp.
func (p *relativePointer) sent(x, y int, at time.Time) {
	if len(p.flight) == echoSlots {
		p.flight = p.flight[1:]
	}
	p.flight = append(p.flight, sentPoint{x, y, at})
}

// resync takes the position the Mac reports for its cursor. The Mac echoes
// the positions we send, a little late; those are ignored, or the pointer would
// jump back while it moves. Any other position is a warp by a Mac app, and the
// running total starts from it. It reports whether the total moved.
func (p *relativePointer) resync(x, y int, at time.Time) bool {
	keep := p.flight[:0]
	echo := -1
	for _, point := range p.flight {
		if at.Sub(point.at) > echoLife {
			continue
		}
		keep = append(keep, point)
	}
	p.flight = keep
	for index := len(p.flight) - 1; index >= 0; index-- {
		point := p.flight[index]
		if absInt(point.x-x) <= echoTolerance && absInt(point.y-y) <= echoTolerance {
			echo = index
			break
		}
	}
	if echo >= 0 {
		p.flight = p.flight[echo+1:]
		return false
	}
	p.flight = p.flight[:0]
	if int(p.X) == x && int(p.Y) == y {
		return false
	}
	p.set(x, y)
	return true
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// lockStart is where the running total begins when the pointer is locked: the
// Mac's reported cursor when it reported one after our last pointer message,
// else the last position we sent, else the middle of the picture. The Mac
// reports its cursor only when something other than us moved it, so on a fresh
// connection neither is known, and the middle is a guess that the first motion
// then moves from. Starting at the origin instead would throw the cursor into
// a corner.
func lockStart(reported bool, reportedX, reportedY int, reportedNewer bool, sent bool, sentX, sentY int, frameWidth, frameHeight int) (int, int) {
	switch {
	case reported && (reportedNewer || !sent):
		return reportedX, reportedY
	case sent:
		return sentX, sentY
	}
	return max(frameWidth, 1) / 2, max(frameHeight, 1) / 2
}

// pointerLock follows one Exclusive-mode lock request.
type pointerLock struct {
	// on is set from the request until the release.
	on bool
	// held is set while the pointer really is held.
	held bool
	// idle is set while a lock is wanted but no request stands at the display
	// server: it ended the last one, or the pointer was not yet over the picture
	// when Exclusive began. The next pointer motion over the picture requests it.
	idle bool
	// warned is set once the "keyboard only" notice was shown for this stretch of
	// Exclusive mode, so a focus change does not repeat it.
	warned bool
}

// lockNotice is what the user is told about a change of the lock.
type lockNotice int

const (
	lockNoticeNone lockNotice = iota
	lockNoticeLocked
	lockNoticeEnded
)

// request records a lock request; held is whether the pointer is held at once.
func (l *pointerLock) request(held bool) {
	l.on, l.held, l.idle = true, held, false
}

// release records the end of the lock by us (focus lost, mode left, session over).
func (l *pointerLock) release() {
	l.on, l.held, l.idle = false, false, false
}

// lose records a lock that stood and can no longer, because the window is gone
// or the barriers could not be rebuilt. It ends the lock as a failure to start
// one does, so only the keyboard stays captured, and reports whether the user
// is told (once per stretch of Exclusive mode).
func (l *pointerLock) lose() (notify bool) {
	l.release()
	return l.failedOnce()
}

// state takes the display server's report that the lock began or ended.
func (l *pointerLock) state(locked bool) lockNotice {
	if !l.on || locked == l.held {
		return lockNoticeNone
	}
	l.held = locked
	l.idle = !locked
	if locked {
		return lockNoticeLocked
	}
	return lockNoticeEnded
}

// wait records a lock that is wanted but cannot be requested yet, because the
// pointer is not over the picture. Compositors differ on when they begin a lock
// whose region the pointer is outside of (some never do), so none is asked until
// the pointer is inside.
func (l *pointerLock) wait() {
	l.on, l.held, l.idle = true, false, true
}

// wantsRequest reports whether a wanted lock has no request standing.
func (l *pointerLock) wantsRequest() bool {
	return l.on && l.idle && !l.held
}

// failedOnce reports true the first time a lock could not be made in this stretch
// of Exclusive mode; leaving the mode makes the next failure worth a notice again.
func (l *pointerLock) failedOnce() bool {
	if l.warned {
		return false
	}
	l.warned = true
	return true
}

// leaveExclusive forgets the notice state when the mode is left.
func (l *pointerLock) leaveExclusive() {
	l.warned = false
}

// lockStatus is how the X11 lock with raw motion went.
type lockStatus int

const (
	// lockStatusOK: barriers and raw motion are up.
	lockStatusOK lockStatus = iota
	// lockStatusNoXInput: XInput 2.1 or later is missing, so raw motion is not
	// available while a button is held.
	lockStatusNoXInput
	// lockStatusNoBarriers: raw motion works but the barriers could not be made,
	// and without them the pointer is not held.
	lockStatusNoBarriers
)

// x11LockResult turns the status of the raw motion lock into held and requested.
// Without usable XInput the barriers alone still keep the pointer in the window;
// without barriers nothing holds it and only the keyboard is captured.
func x11LockResult(status lockStatus, confine func() bool) (held, requested bool) {
	switch status {
	case lockStatusOK:
		return true, true
	case lockStatusNoXInput:
		return false, confine()
	}
	return false, false
}

// xinputUsable reports whether a negotiated XInput version delivers raw motion
// during an implicit grab, which started with 2.1.
func xinputUsable(major, minor int) bool {
	return major > 2 || (major == 2 && minor >= 1)
}

// finiteMotion reports whether both parts of a motion are numbers a pointer can move by.
func finiteMotion(dx, dy float64) bool {
	return !math.IsNaN(dx) && !math.IsInf(dx, 0) && !math.IsNaN(dy) && !math.IsInf(dy, 0)
}

// lockGeometry is what the X11 lock reads of the screen, in root pixels: the
// window box now [0..3] (left, top, right, bottom), the picture [4..7] (x, y,
// width, height), the pointer [8..9] and the box the barriers stand on [10..13].
type lockGeometry [14]int

// settleActions judges a lock that is up. The barriers are rebuilt when the
// window is not where they stand, and, when raw motion holds the pointer
// (centre), the pointer is warped to the middle of the picture when it is not
// over it: a click outside the picture goes to another window, which takes the
// focus and ends the lock. A picture without size is not laid out yet and has
// no middle to go to.
func settleActions(g lockGeometry, centre bool) (rebuild, recenter bool) {
	rebuild = g[0] != g[10] || g[1] != g[11] || g[2] != g[12] || g[3] != g[13]
	if centre && g[6] > 0 && g[7] > 0 {
		recenter = g[8] < g[4] || g[8] >= g[4]+g[6] || g[9] < g[5] || g[9] >= g[5]+g[7]
	}
	return rebuild, recenter
}

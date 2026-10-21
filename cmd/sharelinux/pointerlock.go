package main

import "time"

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

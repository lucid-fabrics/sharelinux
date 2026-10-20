package main

import "time"

// duplicateClickWindow is how soon after a release a press of the same button
// counts as the release's echo. On X11 SDL listens to every input device, so a
// click injected through XTEST (xdotool, remote input tools) can arrive once from
// the XTEST device and once from the master pointer, and when the press and
// release are queued together the order is down, up, down, up. SDL cannot tell
// the copies apart (same mouse id, no device id) and a Mac reads the second pair
// as a double click. A hand cannot press the same button again within a few
// milliseconds, and the copies arrive within about one.
const duplicateClickWindow = 4 * time.Millisecond

// clickFilter drops the echoed press of a click.
type clickFilter struct {
	released [4]struct {
		at   uint64
		seen bool
	}
}

// allow reports whether the event is a real one. at is the SDL event timestamp in
// nanoseconds.
func (f *clickFilter) allow(button uint8, down bool, at uint64) bool {
	if int(button) >= len(f.released) {
		return true
	}
	last := &f.released[button]
	if !down {
		last.at, last.seen = at, true
		return true
	}
	return !last.seen || at < last.at || at-last.at >= uint64(duplicateClickWindow)
}

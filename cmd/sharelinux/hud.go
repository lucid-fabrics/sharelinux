package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// meter turns running counters into per-second rates over a window of about a second.
type meter struct {
	started time.Time
	frames  int
	updates uint64
	wire    uint64

	Frames  float64
	Updates float64
	BitsPS  float64
}

// Tick records the counters at now; it refreshes the rates once a second has passed.
func (m *meter) Tick(now time.Time, frames int, updates, wire uint64) {
	if m.started.IsZero() {
		m.started, m.frames, m.updates, m.wire = now, frames, updates, wire
		return
	}
	elapsed := now.Sub(m.started).Seconds()
	if elapsed < 1 {
		return
	}
	m.Frames = float64(frames-m.frames) / elapsed
	m.Updates = float64(updates-m.updates) / elapsed
	m.BitsPS = float64(wire-m.wire) * 8 / elapsed
	m.started, m.frames, m.updates, m.wire = now, frames, updates, wire
}

// latency smooths the time from sending input to the next screen update.
type latency struct {
	access sync.Mutex
	sent   time.Time
	value  time.Duration
}

func (l *latency) Value() time.Duration {
	l.access.Lock()
	defer l.access.Unlock()
	return l.value
}

func (l *latency) Sent(now time.Time) {
	l.access.Lock()
	defer l.access.Unlock()
	if l.sent.IsZero() {
		l.sent = now
	}
}

func (l *latency) Updated(now time.Time) {
	l.access.Lock()
	defer l.access.Unlock()
	if l.sent.IsZero() {
		return
	}
	sample := now.Sub(l.sent)
	l.sent = time.Time{}
	if l.value == 0 {
		l.value = sample
		return
	}
	l.value = (l.value*7 + sample) / 8
}

func formatBits(bitsPerSecond float64) string {
	switch {
	case bitsPerSecond >= 1e6:
		return fmt.Sprintf("%.1f Mbit/s", bitsPerSecond/1e6)
	case bitsPerSecond >= 1e3:
		return fmt.Sprintf("%.0f kbit/s", bitsPerSecond/1e3)
	default:
		return fmt.Sprintf("%.0f bit/s", bitsPerSecond)
	}
}

// bandwidthLine is the HUD's bandwidth row. Only Apple's record stream counts the
// bytes on the wire, so other servers have no figure and get no row.
func bandwidthLine(info rfb.ServerInfo, bitsPerSecond float64) string {
	if info.Dialect != rfb.DialectApple {
		return ""
	}
	return "bandwidth " + formatBits(bitsPerSecond)
}

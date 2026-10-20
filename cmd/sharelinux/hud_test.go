package main

import (
	"testing"
	"time"
)

func TestMeter(t *testing.T) {
	var m meter
	start := time.Unix(1000, 0)
	m.Tick(start, 0, 0, 0)
	m.Tick(start.Add(500*time.Millisecond), 30, 10, 1000)
	if m.Frames != 0 {
		t.Fatal("rates refresh after a second")
	}
	m.Tick(start.Add(2*time.Second), 120, 40, 250000)
	if m.Frames != 60 || m.Updates != 20 || m.BitsPS != 1e6 {
		t.Fatalf("%+v", m)
	}
}

func TestLatency(t *testing.T) {
	var l latency
	now := time.Unix(1000, 0)
	l.Updated(now)
	if l.Value() != 0 {
		t.Fatal("no input, no sample")
	}
	l.Sent(now)
	l.Sent(now.Add(time.Second))
	l.Updated(now.Add(80 * time.Millisecond))
	if l.Value() != 80*time.Millisecond {
		t.Fatal(l.Value())
	}
	l.Sent(now)
	l.Updated(now.Add(160 * time.Millisecond))
	if l.Value() != 90*time.Millisecond {
		t.Fatal(l.Value())
	}
}

func TestFormatBits(t *testing.T) {
	if formatBits(500) != "500 bit/s" || formatBits(2500) != "2 kbit/s" && formatBits(2500) != "3 kbit/s" || formatBits(2.5e6) != "2.5 Mbit/s" {
		t.Fatal(formatBits(500), formatBits(2500), formatBits(2.5e6))
	}
}

func TestStraightAlpha(t *testing.T) {
	got := straightAlpha([]byte{50, 100, 0, 100, 9, 9, 9, 0})
	if got[0] != 127 || got[1] != 255 || got[2] != 0 || got[3] != 100 || got[4] != 0 || got[7] != 0 {
		t.Fatal(got)
	}
}

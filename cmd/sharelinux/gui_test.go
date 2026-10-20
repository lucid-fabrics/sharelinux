//go:build linux

package main

import (
	"testing"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestModifiersFoldGDKState(t *testing.T) {
	cases := []struct {
		state gdk.ModifierType
		want  uint16
	}{
		{0, 0},
		{gdk.ShiftMask, modShift},
		{gdk.ControlMask | gdk.AltMask, modCtrl | modAlt},
		{gdk.SuperMask, modGUI},
		{gdk.MetaMask, modGUI},
		{gdk.ShiftMask | gdk.ControlMask | gdk.AltMask | gdk.SuperMask, modShift | modCtrl | modAlt | modGUI},
		{modAltGr, 0},
	}
	for _, c := range cases {
		if got := modifiers(c.state); got != c.want {
			t.Errorf("%#x: %#x, want %#x", uint(c.state), got, c.want)
		}
	}
}

func TestReleaseChordFromGDKState(t *testing.T) {
	// Ctrl+Alt+Esc as GDK reports it: Esc is evdev 1, hardware keycode 9.
	scancode := hidFromKeycode(9)
	mod := modifiers(gdk.ControlMask | gdk.AltMask)
	if chordCommand(scancode, mod) != commandReleaseExclusive {
		t.Fatal("the release chord must work from a raw GDK key event")
	}
}

func TestModeIDRoundTrip(t *testing.T) {
	for _, mode := range []rfb.Mode{rfb.ModeObserve, rfb.ModeControl, rfb.ModeExclusiveControl} {
		if got := modeFromID(modeID(mode)); got != mode {
			t.Errorf("%v became %v", mode, got)
		}
	}
	if modeFromID("anything else") != rfb.ModeControl {
		t.Fatal("an unknown id means control")
	}
}

func TestButtonBit(t *testing.T) {
	if buttonBit(1) != rfb.ButtonLeft || buttonBit(2) != rfb.ButtonMiddle || buttonBit(3) != rfb.ButtonRight {
		t.Fatal("GDK buttons 1, 2, 3 are left, middle, right")
	}
	if buttonBit(8) != 0 || buttonBit(0) != 0 {
		t.Fatal("side buttons are not forwarded")
	}
}

func TestLastElement(t *testing.T) {
	cases := map[string]string{
		"/home/a/Downloads":  "Downloads",
		"/home/a/Downloads/": "Downloads",
		"Downloads":          "Downloads",
		"/":                  "/",
	}
	for in, want := range cases {
		if got := lastElement(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestMatchesQuery(t *testing.T) {
	if !matchesQuery("", "anything") {
		t.Fatal("an empty query matches everything")
	}
	if !matchesQuery("mini", "mac mini") {
		t.Fatal("match is on lower-case text")
	}
	if matchesQuery("studio", "mac mini", "192.168.1.5") {
		t.Fatal("no field contains the query")
	}
}


package main

import (
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestChordNeedsFullLeader(t *testing.T) {
	const all = modCtrl | modAlt | modShift
	if chordCommand(11, all) != commandToggleStats {
		t.Fatal("leader+H is the stats overlay")
	}
	for _, mod := range []uint16{0, modCtrl, modCtrl | modAlt, modAlt | modShift, modGUI} {
		if chordCommand(11, mod) != commandNone {
			t.Errorf("modifiers %#x must not trigger a command", mod)
		}
	}
	if chordCommand(4, all) != commandNone {
		t.Fatal("leader+A is not a command")
	}
}

func TestReleaseChordIsCtrlAltEscape(t *testing.T) {
	if chordCommand(hidEscape, modCtrl|modAlt) != commandReleaseExclusive {
		t.Fatal("Ctrl+Alt+Esc releases exclusive mode")
	}
	for _, mod := range []uint16{0, modCtrl, modAlt, modCtrl | modAlt | modShift, modCtrl | modAlt | modGUI} {
		if chordCommand(hidEscape, mod) != commandNone {
			t.Errorf("modifiers %#x must not release", mod)
		}
	}
}

func TestChordKeysAreUnique(t *testing.T) {
	seen := map[command]uint32{}
	for scancode, command := range chordKeys {
		if other, found := seen[command]; found {
			t.Errorf("command %d on both %d and %d", command, scancode, other)
		}
		seen[command] = scancode
	}
}

func TestNextCycle(t *testing.T) {
	if nextCycle(0, 3) != 1 || nextCycle(2, 3) != 0 {
		t.Fatal("ring")
	}
}

func TestAllowedMode(t *testing.T) {
	for _, mode := range []rfb.Mode{rfb.ModeObserve, rfb.ModeControl, rfb.ModeExclusiveControl} {
		if got := allowedMode(mode, rfb.ServerFlagObserveOnly); got != rfb.ModeObserve {
			t.Errorf("view-only server allowed %v", got)
		}
		if got := allowedMode(mode, 0); got != mode {
			t.Errorf("%v changed to %v", mode, got)
		}
	}
}

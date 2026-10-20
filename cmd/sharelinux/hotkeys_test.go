package main

import "testing"

func TestChordNeedsFullLeader(t *testing.T) {
	const all = modLeftCtrl | modLeftAlt | modLeftShift
	if chordCommand(11, all) != commandToggleHUD {
		t.Fatal("leader+H is the HUD")
	}
	if chordCommand(41, modRightCtrl|modRightAlt|modRightShift) != commandReleaseExclusive {
		t.Fatal("either side of each modifier counts")
	}
	for _, mod := range []uint16{0, modLeftCtrl, modLeftCtrl | modLeftAlt, modLeftAlt | modLeftShift, modLeftGUI} {
		if chordCommand(11, mod) != commandNone {
			t.Errorf("modifiers %#x must not trigger a command", mod)
		}
	}
	if chordCommand(4, all) != commandNone {
		t.Fatal("leader+A is not a command")
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

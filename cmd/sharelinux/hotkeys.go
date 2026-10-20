package main

// The viewer's own commands. Every key goes to the Mac, so commands need a chord
// the Mac never uses: all three of Ctrl, Alt and Shift held, then a letter.
type command int

const (
	commandNone command = iota
	commandReleaseExclusive
	commandCycleMode
	commandToggleStats
	commandFullscreen
	commandCycleQuality
	commandCycleDisplay
	commandVirtualDisplay
	commandToggleHDR
	commandRefresh
	commandTypeClipboard
	commandFetchDrag
	commandScreenshot
	commandQuit
)

// Modifier bits. The GTK layer folds GDK's modifier state into these, so the
// table needs no GTK import and is testable anywhere.
const (
	modShift uint16 = 1 << iota
	modCtrl
	modAlt
	modGUI
)

// chordKeys are the viewer commands behind the Ctrl+Alt+Shift leader. Most of
// them also have a menu entry; the chord is the keyboard route for a session
// that has the keyboard grabbed.
var chordKeys = map[uint32]command{
	16: commandCycleMode,        // M
	11: commandToggleStats,      // H
	9:  commandFullscreen,       // F
	24: commandCycleQuality,     // U
	7:  commandCycleDisplay,     // D
	25: commandVirtualDisplay,   // V
	27: commandToggleHDR,        // X
	21: commandRefresh,          // R
	23: commandTypeClipboard,    // T
	10: commandFetchDrag,        // G
	22: commandScreenshot,       // S
	20: commandQuit,             // Q
}

// leaderHeld reports whether Ctrl, Alt and Shift are all down.
func leaderHeld(mod uint16) bool {
	return mod&modCtrl != 0 && mod&modAlt != 0 && mod&modShift != 0
}

// hidEscape is the HID usage of the Escape key.
const hidEscape = 41

// chordCommand maps a key press to a viewer command. Ctrl+Alt+Esc releases
// exclusive mode; every other command needs the whole Ctrl+Alt+Shift leader.
func chordCommand(scancode uint32, mod uint16) command {
	if scancode == hidEscape && mod&modCtrl != 0 && mod&modAlt != 0 && mod&(modShift|modGUI) == 0 {
		return commandReleaseExclusive
	}
	if !leaderHeld(mod) {
		return commandNone
	}
	return chordKeys[scancode]
}

// nextCycle is the index after current in a ring of count items.
func nextCycle(current, count int) int {
	return (current + 1) % count
}

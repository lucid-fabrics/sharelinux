package main

// The viewer's own commands. Every key goes to the Mac, so commands need a chord
// the Mac never uses: all three of Ctrl, Alt and Shift held, then a letter.
type command int

const (
	commandNone command = iota
	commandReleaseExclusive
	commandCycleMode
	commandToggleHUD
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

// SDL modifier bits, repeated here so the table needs no SDL import.
const (
	modLeftShift  = 0x0001
	modRightShift = 0x0002
	modLeftCtrl   = 0x0040
	modRightCtrl  = 0x0080
	modLeftAlt    = 0x0100
	modRightAlt   = 0x0200
	modShift      = modLeftShift | modRightShift
	modCtrl       = modLeftCtrl | modRightCtrl
	modAlt        = modLeftAlt | modRightAlt
	modLeftGUI    = 0x0400
	modRightGUI   = 0x0800
	modGUI        = modLeftGUI | modRightGUI
)

var chordKeys = map[uint32]command{
	41: commandReleaseExclusive, // Escape
	16: commandCycleMode,        // M
	11: commandToggleHUD,        // H
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

// chordCommand maps a key press to a viewer command; it is commandNone unless the leader is held.
func chordCommand(scancode uint32, mod uint16) command {
	if !leaderHeld(mod) {
		return commandNone
	}
	return chordKeys[scancode]
}

// nextCycle is the index after current in a ring of count items.
func nextCycle(current, count int) int {
	return (current + 1) % count
}

package main

// textFilter stops a character from being typed twice. A printable key already
// reaches the Mac as a key code, and SDL then also reports the character as
// text input. Text that no key press explains (an input method, a dead-key
// composition, a pasted character) is typed instead.
type textFilter struct {
	expecting bool
}

// KeyDown notes a key that types its own character. Keys held with Ctrl, Alt or
// the Super key type nothing, so they set no expectation.
func (f *textFilter) KeyDown(scancode uint32, mod uint16) {
	if printable(scancode) && mod&(modCtrl|modAlt|modGUI) == 0 {
		f.expecting = true
	}
}

// Consume reports whether the text only repeats the key just pressed.
func (f *textFilter) Consume(text string) bool {
	if !f.expecting {
		return false
	}
	f.expecting = false
	for _, character := range text {
		if character >= 0x80 {
			return false
		}
	}
	return true
}

// Reset forgets the expectation at the end of an event batch.
func (f *textFilter) Reset() {
	f.expecting = false
}

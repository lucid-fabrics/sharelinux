package main

// textFilter stops a character from being typed twice. A printable key already
// reaches the Mac as a key code, and SDL then also reports the character as
// text input, whatever that character is (o with a diaeresis, an AltGr symbol).
// The key code wins and the text is dropped. Only text that no key press
// explains (an input method or a composition) is typed instead.
type textFilter struct {
	expecting bool
}

// KeyDown notes a key that was sent as a key code and also types a character.
// Keys held with Ctrl or the Super key type nothing, so they set no expectation.
func (f *textFilter) KeyDown(scancode uint32, mod uint16) {
	if printable(scancode) && mod&(modCtrl|modGUI) == 0 {
		f.expecting = true
	}
}

// Consume reports whether the text only repeats the key just pressed.
func (f *textFilter) Consume(text string) bool {
	if !f.expecting {
		return false
	}
	f.expecting = false
	return true
}

// Reset forgets the expectation at the end of an event batch.
func (f *textFilter) Reset() {
	f.expecting = false
}

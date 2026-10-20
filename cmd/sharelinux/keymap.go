package main

import (
	"unicode"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// SDL scancodes are USB HID usage numbers, so the tables below are keyed by
// them and need no SDL import. Mac key codes are the kVK_* values.
type macKey struct {
	Code   uint16
	Keysym uint32
	Valid  bool
}

const (
	hidA          = 4
	hidGrave      = 53
	hidNonUSSlash = 100
	hidCapsLock   = 57
)

const (
	keysymBackSpace uint32 = 0xff08
	keysymTab       uint32 = 0xff09
	keysymReturn    uint32 = 0xff0d
	keysymEscape    uint32 = 0xff1b
	keysymDelete    uint32 = 0xffff
	keysymHome      uint32 = 0xff50
	keysymLeft      uint32 = 0xff51
	keysymUp        uint32 = 0xff52
	keysymRight     uint32 = 0xff53
	keysymDown      uint32 = 0xff54
	keysymPageUp    uint32 = 0xff55
	keysymPageDown  uint32 = 0xff56
	keysymEnd       uint32 = 0xff57
	keysymInsert    uint32 = 0xff63
	keysymHelp      uint32 = 0xff6a
	keysymMenu      uint32 = 0xff67
	keysymF1        uint32 = 0xffbe
	keysymKeypad0   uint32 = 0xffb0
)

type keyEntry struct {
	code   uint16
	keysym uint32
}

// namedKeys are the keys that produce no text. Printable keys carry the
// character of the user's own layout as their keysym.
var namedKeys = map[uint32]keyEntry{
	40:  {36, keysymReturn},
	41:  {53, keysymEscape},
	42:  {51, keysymBackSpace},
	43:  {48, keysymTab},
	44:  {49, ' '},
	57:  {57, 0},
	70:  {105, 0xff61},
	71:  {107, 0xff14},
	72:  {113, 0xff13},
	73:  {114, keysymInsert},
	74:  {115, keysymHome},
	75:  {116, keysymPageUp},
	76:  {117, keysymDelete},
	77:  {119, keysymEnd},
	78:  {121, keysymPageDown},
	79:  {124, keysymRight},
	80:  {123, keysymLeft},
	81:  {125, keysymDown},
	82:  {126, keysymUp},
	83:  {71, 0xff7f},
	84:  {75, 0xffaf},
	85:  {67, 0xffaa},
	86:  {78, 0xffad},
	87:  {69, 0xffab},
	88:  {76, 0xff8d},
	99:  {65, 0xffae},
	101: {110, keysymMenu},
	103: {81, 0xffbd},
	117: {114, keysymHelp},
	127: {74, 0},
	128: {72, 0},
	129: {73, 0},
	133: {95, 0xffac},
	// Japanese keyboards: ro, yen, kana (Hangul/English) and eisu keys.
	135: {94, 0},
	136: {104, 0}, // Katakana/Hiragana is the Kana key
	137: {93, 0},
	138: {104, 0}, // Henkan converts, which is the Kana key
	139: {102, 0}, // Muhenkan cancels the conversion, which is the Eisu key
	144: {104, 0},
	145: {102, 0},
	// Modifiers. Alt is Option and the Super key is Command.
	224: {59, 0xffe3},
	225: {56, 0xffe1},
	226: {58, 0xffe9},
	227: {55, 0xffeb},
	228: {62, 0xffe4},
	229: {60, 0xffe2},
	230: {61, 0xffea},
	231: {54, 0xffec},
}

// functionCodes are F1 to F20 (HID 58 to 69 for F1 to F12, 104 to 111 for F13 to F20).
var functionCodes = [...]uint16{122, 120, 99, 118, 96, 97, 98, 100, 101, 109, 103, 111, 105, 107, 113, 106, 64, 79, 80, 90}

// letterCodes are A to Z; digitCodes are 1 to 9 then 0 (HID 30 to 39).
var letterCodes = [...]uint16{0, 11, 8, 2, 14, 3, 5, 4, 34, 38, 40, 37, 46, 45, 31, 35, 12, 15, 1, 17, 32, 9, 13, 7, 16, 6}
var digitCodes = [...]uint16{18, 19, 20, 21, 23, 22, 26, 28, 25, 29}

// punctuationCodes maps the punctuation keys, minus to slash. HID 50 is the ISO
// pound key, which a Mac reads like backslash.
var punctuationCodes = map[uint32]uint16{
	45: 27, 46: 24, 47: 33, 48: 30, 49: 42, 50: 42, 51: 41, 52: 39, 54: 43, 55: 47, 56: 44,
}

var keypadDigits = [...]uint16{82, 83, 84, 85, 86, 87, 88, 89, 91, 92}

// lookupScancode finds the Mac key code and the keysym of a key that is not
// printable. keyboard selects between the ANSI, ISO and JIS meaning of the keys
// next to left Shift and the grave key.
func lookupScancode(scancode uint32, keyboard rfb.KeyboardType) (macKey, bool) {
	switch {
	case scancode >= hidA && scancode < hidA+26:
		return macKey{Code: letterCodes[scancode-hidA], Valid: true}, true
	case scancode >= 30 && scancode <= 39:
		return macKey{Code: digitCodes[scancode-30], Valid: true}, true
	case scancode >= 58 && scancode <= 69:
		number := scancode - 58
		return macKey{Code: functionCodes[number], Keysym: keysymF1 + number, Valid: true}, true
	case scancode >= 104 && scancode <= 111:
		number := scancode - 104 + 12
		return macKey{Code: functionCodes[number], Keysym: keysymF1 + number, Valid: true}, true
	case scancode >= 89 && scancode <= 98:
		digit := (scancode - 88) % 10
		return macKey{Code: keypadDigits[digit], Keysym: keysymKeypad0 + uint32(digit), Valid: true}, true
	case scancode == hidGrave:
		if keyboard == rfb.KeyboardISO {
			return macKey{Code: 10, Valid: true}, true
		}
		return macKey{Code: 50, Valid: true}, true
	case scancode == hidNonUSSlash:
		if keyboard == rfb.KeyboardISO {
			return macKey{Code: 50, Valid: true}, true
		}
		return macKey{Code: 10, Valid: true}, true
	}
	entry, found := namedKeys[scancode]
	if found {
		return macKey{Code: entry.code, Keysym: entry.keysym, Valid: true}, true
	}
	code, found := punctuationCodes[scancode]
	if found {
		return macKey{Code: code, Valid: true}, true
	}
	return macKey{}, false
}

// keysymFor is the keysym of the character a key types on the user's layout.
func keysymFor(character rune, shift bool) uint32 {
	if character <= 0 {
		return 0
	}
	if shift && character < unicode.MaxASCII && unicode.IsLetter(character) {
		character = unicode.ToUpper(character)
	}
	if character < 0x100 {
		return uint32(character)
	}
	return 0x01000000 | uint32(character)
}

// mapKey turns one physical key press into a Mac key code and keysym.
func mapKey(scancode uint32, character rune, shift bool, keyboard rfb.KeyboardType) (macKey, bool) {
	key, found := lookupScancode(scancode, keyboard)
	if !found {
		return macKey{}, false
	}
	if key.Keysym == 0 && scancode != hidCapsLock {
		key.Keysym = keysymFor(character, shift)
	}
	return key, true
}

// printable reports whether a key types text, so a following text input event is
// the character of the key and must not be typed a second time.
func printable(scancode uint32) bool {
	switch {
	case scancode >= hidA && scancode < hidA+26, scancode >= 30 && scancode <= 39:
		return true
	case scancode == 44, scancode == hidGrave, scancode == hidNonUSSlash:
		return true
	}
	_, found := punctuationCodes[scancode]
	return found
}

// keyboardLearner starts at the configured keyboard type and learns ISO or JIS
// from keys only those keyboards have, like the Mac app does for hardware keyboards.
type keyboardLearner struct {
	current rfb.KeyboardType
	fixed   bool
}

func newKeyboardLearner(setting string) *keyboardLearner {
	learner := &keyboardLearner{current: rfb.KeyboardANSI, fixed: setting != "auto"}
	switch setting {
	case "iso":
		learner.current = rfb.KeyboardISO
	case "jis":
		learner.current = rfb.KeyboardJIS
	}
	return learner
}

func (l *keyboardLearner) Type() rfb.KeyboardType {
	return l.current
}

func (l *keyboardLearner) Learn(scancode uint32) {
	if l.fixed {
		return
	}
	switch scancode {
	case 133, 135, 136, 137, 138, 139, 144, 145:
		l.current = rfb.KeyboardJIS
	case hidNonUSSlash, 50:
		if l.current != rfb.KeyboardJIS {
			l.current = rfb.KeyboardISO
		}
	}
}

// evdevToHID converts a Linux evdev key code (GDK's hardware keycode minus 8,
// on X11 and Wayland alike) to the USB HID usage the tables above are keyed by.
var evdevToHID = map[uint32]uint32{
	1: 41, 2: 30, 3: 31, 4: 32, 5: 33, 6: 34, 7: 35, 8: 36, 9: 37, 10: 38, 11: 39,
	12: 45, 13: 46, 14: 42, 15: 43,
	16: 20, 17: 26, 18: 8, 19: 21, 20: 23, 21: 28, 22: 24, 23: 12, 24: 18, 25: 19,
	26: 47, 27: 48, 28: 40, 29: 224,
	30: 4, 31: 22, 32: 7, 33: 9, 34: 10, 35: 11, 36: 13, 37: 14, 38: 15,
	39: 51, 40: 52, 41: 53, 42: 225, 43: 49,
	44: 29, 45: 27, 46: 6, 47: 25, 48: 5, 49: 17, 50: 16,
	51: 54, 52: 55, 53: 56, 54: 229, 55: 85, 56: 226, 57: 44, 58: 57,
	59: 58, 60: 59, 61: 60, 62: 61, 63: 62, 64: 63, 65: 64, 66: 65, 67: 66, 68: 67,
	69: 83, 70: 71,
	71: 95, 72: 96, 73: 97, 74: 86, 75: 92, 76: 93, 77: 94, 78: 87, 79: 89, 80: 90, 81: 91, 82: 98, 83: 99,
	86: 100, 87: 68, 88: 69,
	89: 135, 90: 136, 91: 136, 92: 138, 93: 136, 94: 139,
	96: 88, 97: 228, 98: 84, 99: 70, 100: 230,
	102: 74, 103: 82, 104: 75, 105: 80, 106: 79, 107: 77, 108: 81, 109: 78, 110: 73, 111: 76,
	117: 103, 119: 72, 121: 133, 122: 144, 123: 145, 124: 137,
	125: 227, 126: 231, 127: 101, 138: 117, 139: 101,
	183: 104, 184: 105, 185: 106, 186: 107, 187: 108, 188: 109, 189: 110, 190: 111,
}

// evdevOffset is what X11 and GDK Wayland add to an evdev code.
const evdevOffset = 8

// hidFromKeycode turns a GDK hardware keycode into a HID usage, or 0 when the key is unknown.
func hidFromKeycode(keycode uint32) uint32 {
	if keycode < evdevOffset {
		return 0
	}
	return evdevToHID[keycode-evdevOffset]
}

// Command key choices of the preferences.
const (
	commandKeySuper = "super"
	commandKeyAlt   = "alt"
)

// remapCommandKey swaps the Super and Alt keys when Alt is the Command key, so
// the key next to the space bar acts as Command like on a Mac keyboard.
func remapCommandKey(scancode uint32, setting string) uint32 {
	if setting != commandKeyAlt {
		return scancode
	}
	switch scancode {
	case 226:
		return 227
	case 227:
		return 226
	case 230:
		return 231
	case 231:
		return 230
	}
	return scancode
}

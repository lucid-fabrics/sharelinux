package main

import (
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestLetterAndDigitCodes(t *testing.T) {
	cases := map[uint32]uint16{4: 0, 5: 11, 20: 12, 29: 6, 30: 18, 31: 19, 38: 25, 39: 29}
	for scancode, want := range cases {
		key, found := lookupScancode(scancode, rfb.KeyboardANSI)
		if !found || key.Code != want {
			t.Errorf("scancode %d: got %+v want code %d", scancode, key, want)
		}
	}
}

func TestModifiersMapToMacModifiers(t *testing.T) {
	cases := []struct {
		scancode uint32
		code     uint16
		keysym   uint32
	}{
		{224, 59, 0xffe3}, {225, 56, 0xffe1}, {226, 58, 0xffe9}, {227, 55, 0xffeb},
		{228, 62, 0xffe4}, {229, 60, 0xffe2}, {230, 61, 0xffea}, {231, 54, 0xffec},
	}
	for _, c := range cases {
		key, found := mapKey(c.scancode, 0, false, rfb.KeyboardANSI)
		if !found || key.Code != c.code || key.Keysym != c.keysym {
			t.Errorf("scancode %d: %+v", c.scancode, key)
		}
	}
}

func TestNamedAndFunctionKeys(t *testing.T) {
	cases := map[uint32]uint16{40: 36, 41: 53, 42: 51, 43: 48, 44: 49, 79: 124, 80: 123, 81: 125, 82: 126,
		58: 122, 59: 120, 69: 111, 104: 105, 111: 90, 76: 117, 74: 115, 77: 119, 75: 116, 78: 121}
	for scancode, want := range cases {
		key, found := lookupScancode(scancode, rfb.KeyboardANSI)
		if !found || key.Code != want {
			t.Errorf("scancode %d: got %+v want %d", scancode, key, want)
		}
	}
	key, _ := lookupScancode(58, rfb.KeyboardANSI)
	if key.Keysym != 0xffbe {
		t.Errorf("F1 keysym %#x", key.Keysym)
	}
}

func TestKeypad(t *testing.T) {
	cases := map[uint32]uint16{89: 83, 97: 92, 98: 82, 96: 91}
	for scancode, want := range cases {
		key, _ := lookupScancode(scancode, rfb.KeyboardANSI)
		if key.Code != want {
			t.Errorf("scancode %d: got %d want %d", scancode, key.Code, want)
		}
	}
}

func TestISOSwapsGraveAndSection(t *testing.T) {
	ansiGrave, _ := lookupScancode(hidGrave, rfb.KeyboardANSI)
	isoGrave, _ := lookupScancode(hidGrave, rfb.KeyboardISO)
	ansiExtra, _ := lookupScancode(hidNonUSSlash, rfb.KeyboardANSI)
	isoExtra, _ := lookupScancode(hidNonUSSlash, rfb.KeyboardISO)
	if ansiGrave.Code != 50 || isoGrave.Code != 10 || ansiExtra.Code != 10 || isoExtra.Code != 50 {
		t.Fatal(ansiGrave, isoGrave, ansiExtra, isoExtra)
	}
}

func TestJISKeys(t *testing.T) {
	cases := map[uint32]uint16{135: 94, 137: 93, 144: 104, 145: 102, 133: 95}
	for scancode, want := range cases {
		key, found := lookupScancode(scancode, rfb.KeyboardJIS)
		if !found || key.Code != want {
			t.Errorf("scancode %d: %+v", scancode, key)
		}
	}
}

func TestUnknownScancode(t *testing.T) {
	if _, found := mapKey(500, 0, false, rfb.KeyboardANSI); found {
		t.Fatal("scancode 500 must be unmapped")
	}
}

func TestKeysymForCharacters(t *testing.T) {
	if keysymFor('a', false) != 'a' || keysymFor('a', true) != 'A' || keysymFor('1', true) != '1' {
		t.Fatal("ascii keysyms")
	}
	if keysymFor('é', false) != 0xe9 || keysymFor('€', false) != 0x010020ac || keysymFor(0, false) != 0 {
		t.Fatal("non-ascii keysyms")
	}
	key, _ := mapKey(4, 'a', true, rfb.KeyboardANSI)
	if key.Code != 0 || key.Keysym != 'A' {
		t.Fatalf("%+v", key)
	}
	caps, _ := mapKey(hidCapsLock, 0, false, rfb.KeyboardANSI)
	if caps.Code != 57 || caps.Keysym != 0 {
		t.Fatalf("%+v", caps)
	}
}

func TestKeyboardLearner(t *testing.T) {
	learner := newKeyboardLearner("auto")
	if learner.Type() != rfb.KeyboardANSI {
		t.Fatal("starts ANSI")
	}
	learner.Learn(hidNonUSSlash)
	if learner.Type() != rfb.KeyboardISO {
		t.Fatal("ISO key")
	}
	learner.Learn(135)
	if learner.Type() != rfb.KeyboardJIS {
		t.Fatal("JIS key")
	}
	learner.Learn(hidNonUSSlash)
	if learner.Type() != rfb.KeyboardJIS {
		t.Fatal("JIS stays")
	}
	fixed := newKeyboardLearner("ansi")
	fixed.Learn(135)
	if fixed.Type() != rfb.KeyboardANSI {
		t.Fatal("fixed type does not learn")
	}
	if newKeyboardLearner("iso").Type() != rfb.KeyboardISO || newKeyboardLearner("jis").Type() != rfb.KeyboardJIS {
		t.Fatal("configured types")
	}
}

func TestPrintable(t *testing.T) {
	for _, scancode := range []uint32{4, 29, 30, 39, 44, 45, 56, hidGrave} {
		if !printable(scancode) {
			t.Errorf("%d should be printable", scancode)
		}
	}
	for _, scancode := range []uint32{40, 41, 58, 79, 224} {
		if printable(scancode) {
			t.Errorf("%d should not be printable", scancode)
		}
	}
}

func TestTextFilter(t *testing.T) {
	var f textFilter
	f.KeyDown(4, 0)
	if !f.Consume("a") {
		t.Fatal("character of the key is swallowed")
	}
	if f.Consume("a") {
		t.Fatal("only once")
	}
	f.KeyDown(4, modLeftCtrl)
	if f.Consume("a") {
		t.Fatal("ctrl+a types nothing, so no expectation")
	}
	f.KeyDown(4, 0)
	f.Reset()
	if f.Consume("a") {
		t.Fatal("reset forgets")
	}
	if f.Consume("x") {
		t.Fatal("text without a key is typed")
	}
}

// typedCharacters counts how many characters reach the Mac for one key press:
// one for the key code and one more for every text event the filter lets through.
func typedCharacters(scancode uint32, mod uint16, text string) int {
	var f textFilter
	f.KeyDown(scancode, mod)
	typed := 1
	if !f.Consume(text) {
		typed++
	}
	return typed
}

func TestTextFilterNonASCIIIsTypedOnce(t *testing.T) {
	cases := []struct {
		name     string
		scancode uint32
		mod      uint16
		text     string
	}{
		{"o with diaeresis", 51, 0, "ö"},
		{"e with acute", 8, 0, "é"},
		{"AltGr+Q", 20, modRightAlt, "@"},
		{"shifted umlaut", 52, modLeftShift, "Ä"},
	}
	for _, c := range cases {
		if got := typedCharacters(c.scancode, c.mod, c.text); got != 1 {
			t.Errorf("%s typed %d times", c.name, got)
		}
	}
}

func TestTextFilterTypesInputMethodText(t *testing.T) {
	var f textFilter
	f.KeyDown(44, modLeftCtrl)
	if f.Consume("こんにちは") {
		t.Fatal("composition text no key explains is typed")
	}
	f.KeyDown(224, 0)
	if f.Consume("日本") {
		t.Fatal("a modifier key explains no text")
	}
}

func TestMuhenkanHenkanKatakana(t *testing.T) {
	cases := map[uint32]uint16{136: 104, 138: 104, 139: 102}
	for scancode, code := range cases {
		key, found := lookupScancode(scancode, rfb.KeyboardJIS)
		if !found || key.Code != code {
			t.Errorf("HID %d: %+v %v", scancode, key, found)
		}
		learner := newKeyboardLearner("auto")
		learner.Learn(scancode)
		if learner.Type() != rfb.KeyboardJIS {
			t.Errorf("HID %d should mark the keyboard JIS", scancode)
		}
	}
}

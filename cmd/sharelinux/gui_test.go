//go:build linux

package main

import (
	"runtime"
	"testing"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// requireDisplay skips a test that needs GTK when there is no display to open.
// A second gtk.InitCheck after a failed one reports success, so the default
// display is checked as well.
func requireDisplay(t *testing.T) {
	t.Helper()
	if !gtk.InitCheck() || gdk.DisplayGetDefault() == nil {
		t.Skip("GTK needs a display (run under Xvfb)")
	}
	// A GL context belongs to one thread, so a test that draws must not hop between threads: on
	// llvmpipe a hop aborts in epoxy ("Couldn't find current GLX or EGL context").
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
}

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

// A toast reads Pango markup unless told not to. Folder names, Mac names and
// error texts reach toasts, and "<" or "&" in one made the title fail to parse.
func TestToastShowsExternalTextAsWritten(t *testing.T) {
	requireDisplay(t)
	adw.Init()
	for _, text := range []string{"Saved 1 item to <b>Reports</b> & more", "a < b", "Bob's <Mac>"} {
		toast := newToast(text)
		if toast.UseMarkup() {
			t.Fatalf("%q would be parsed as markup", text)
		}
		if toast.Title() != text {
			t.Fatalf("title %q, want %q", toast.Title(), text)
		}
	}
}

// The HDR switch of the preferences is off and dimmed, with the reason under it, where the desktop cannot
// show HDR; where it can, it works and says what it does.
func TestHDRSwitchLooksLikeTheDesktop(t *testing.T) {
	requireDisplay(t)
	adw.Init()
	cases := []struct {
		name      string
		env       hdrEnv
		wanted    bool
		active    bool
		sensitive bool
		subtitle  string
	}{
		{"X11", hdrEnv{}, true, false, false, "Needs a Wayland session, X11 shows standard range only"},
		{"no color manager", hdrEnv{Wayland: true}, true, false, false, "This desktop has no color management, so it cannot show HDR"},
		{"color managed, off", hdrDisplay, false, false, true, hdrSubtitle},
		{"color managed, on", hdrDisplay, true, true, true, hdrSubtitle},
		{"turned on in this run", hdrEnv{Wayland: true, ColorManager: true, OutputKnown: true, OutputHDR: true}, true, true, true, hdrRestart},
		{"standard range screen", hdrEnv{Wayland: true, ColorManager: true, ColorManagement: true, OutputKnown: true}, true, false, false, "This screen is not showing HDR. Turn HDR on for it in the display settings of the desktop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &gui{app: hdrApp(hdrDecoders, c.env)}
			prefs := defaultSettings()
			prefs.HDR = c.wanted
			row := g.hdrRow(prefs, func(func(*settings)) {})
			if row.Active() != c.active || row.Sensitive() != c.sensitive || row.Subtitle() != c.subtitle {
				t.Fatalf("active %v sensitive %v subtitle %q, want %v %v %q", row.Active(), row.Sensitive(), row.Subtitle(), c.active, c.sensitive, c.subtitle)
			}
		})
	}
	// No decoder of the profile: dimmed whatever the desktop is.
	g := &gui{app: hdrApp(videoCaps{HEVCSoftware: true}, hdrDisplay)}
	row := g.hdrRow(defaultSettings(), func(func(*settings)) {})
	if row.Sensitive() || row.Subtitle() != "No decoder here handles HEVC 4:4:4 10-bit" {
		t.Fatalf("sensitive %v subtitle %q", row.Sensitive(), row.Subtitle())
	}
}

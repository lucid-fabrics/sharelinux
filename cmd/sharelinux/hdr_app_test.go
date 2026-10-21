package main

import (
	"log/slog"
	"testing"
)

// hdrApp is an app whose decoders and display server are what the test says.
func hdrApp(caps videoCaps, env hdrEnv) *app {
	a := &app{opts: &options{}, logger: slog.New(slog.DiscardHandler)}
	a.videoOnce.Do(func() { a.caps = caps })
	a.hdrOnce.Do(func() { a.hdrEnv = env })
	return a
}

var (
	hdrDecoders = videoCaps{HEVCVAAPI: true, HEVCSoftware: true, HEVC444x10VAAPI: true}
	hdrDisplay  = hdrEnv{Wayland: true, ColorManager: true, ColorManagement: true, OutputKnown: true, OutputHDR: true}
)

func TestAppHDREffective(t *testing.T) {
	cases := []struct {
		name   string
		caps   videoCaps
		env    hdrEnv
		wanted bool
		want   bool
		reason string
	}{
		{"wanted and shown", hdrDecoders, hdrDisplay, true, true, ""},
		{"not wanted", hdrDecoders, hdrDisplay, false, false, ""},
		{"X11", hdrDecoders, hdrEnv{ColorManagement: true}, true, false, "Needs a Wayland session, X11 shows standard range only"},
		{"no color manager", hdrDecoders, hdrEnv{Wayland: true, ColorManagement: true}, true, false, "This desktop has no color management, so it cannot show HDR"},
		{"no 4:4:4 decoder", videoCaps{HEVCSoftware: true}, hdrDisplay, true, false, "No decoder here handles HEVC 4:4:4 10-bit"},
		{"GTK started without color management", hdrDecoders, hdrEnv{Wayland: true, ColorManager: true, OutputKnown: true, OutputHDR: true}, true, false, hdrNeedsRestart},
		{"standard range screen", hdrDecoders, hdrEnv{Wayland: true, ColorManager: true, ColorManagement: true, OutputKnown: true}, true, false, "This screen is not showing HDR. Turn HDR on for it in the display settings of the desktop"},
		{"software decoder alone", videoCaps{HEVCSoftware: true, HEVC444x10Software: true}, hdrDisplay, true, false, "HDR needs a GPU decoder, the CPU alone is too slow for it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := hdrApp(c.caps, c.env)
			if got := a.hdrEffective(c.wanted); got != c.want {
				t.Fatalf("effective %v, want %v", got, c.want)
			}
			if c.wanted && !c.want && a.hdrOffReason() != c.reason {
				t.Fatalf("reason %q, want %q", a.hdrOffReason(), c.reason)
			}
		})
	}
}

func TestAppHDRWanted(t *testing.T) {
	a := hdrApp(hdrDecoders, hdrDisplay)
	if a.hdrWanted() {
		t.Fatal("HDR is off by default")
	}
	a.settings.HDR = true
	if !a.hdrWanted() {
		t.Fatal("the preference asks for HDR")
	}
	a.settings.HDR = false
	a.opts.HDR = true
	if !a.hdrWanted() {
		t.Fatal("the flag asks for HDR")
	}
}

// Where HDR cannot be shown the virtual display stays standard range: no Prepare step sets its HDR flag.
func TestRFBOptionsHDRFollowsTheMachine(t *testing.T) {
	cases := []struct {
		name string
		caps videoCaps
		env  hdrEnv
		want bool
	}{
		{"shown here", hdrDecoders, hdrDisplay, true},
		{"X11", hdrDecoders, hdrEnv{ColorManagement: true}, false},
		{"no decoder", videoCaps{HEVCSoftware: true}, hdrDisplay, false},
		{"software decoder alone", videoCaps{HEVCSoftware: true, HEVC444x10Software: true}, hdrDisplay, false},
		{"standard range screen", hdrDecoders, hdrEnv{Wayland: true, ColorManager: true, ColorManagement: true, OutputKnown: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := hdrApp(c.caps, c.env)
			req := &request{target: target{User: "me", Host: "mac.local"}, opts: options{HDR: true}}
			options := a.rfbOptions(req, nil)
			if (options.Prepare != nil) != c.want {
				t.Fatalf("Prepare set: %v, want %v", options.Prepare != nil, c.want)
			}
		})
	}
	a := hdrApp(hdrDecoders, hdrDisplay)
	req := &request{target: target{User: "me", Host: "mac.local"}}
	if a.rfbOptions(req, nil).Prepare != nil {
		t.Fatal("HDR off, no virtual display: nothing to prepare")
	}
}

// The screen is asked again when a session is about to start and when the window moves: HDR follows
// the screen the window is on now, and a screen the compositor will not describe is standard range.
func TestAppHDRFollowsTheScreenOfTheWindow(t *testing.T) {
	a := hdrApp(hdrDecoders, hdrEnv{Wayland: true, ColorManager: true, ColorManagement: true})
	screen := outputColor{}
	hdrOutputProbe = func() outputColor { return screen }
	defer func() { hdrOutputProbe = nil }()

	if a.refreshHDROutput() || a.hdrEffective(true) {
		t.Fatal("a screen the desktop would not describe must stay standard range")
	}
	if got := a.hdrOffReason(); got != "The desktop would not say whether this screen shows HDR, so it stays standard range" {
		t.Fatalf("reason %q", got)
	}
	screen = outputColor{Known: true, Transfer: 9, MaxLum: 80, ReferenceLum: 80}
	if a.refreshHDROutput() || a.hdrEffective(true) {
		t.Fatal("a standard range screen must not ask the Mac for HDR")
	}
	screen = outputColor{Known: true, Transfer: wpTransferPQ, MaxLum: 1000, ReferenceLum: 203}
	if !a.refreshHDROutput() || !a.hdrEffective(true) {
		t.Fatal("an HDR screen should")
	}
	screen = outputColor{Known: true, Transfer: 9, MaxLum: 80, ReferenceLum: 80}
	if a.refreshHDROutput() || a.hdrEffective(true) {
		t.Fatal("the window moved to a standard range screen")
	}
}

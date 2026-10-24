package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

func TestHDRDecoder(t *testing.T) {
	tests := []struct {
		name string
		caps videoCaps
		want bool
	}{
		{
			name: "no decoder",
			caps: videoCaps{},
			want: false,
		},
		{
			name: "VA-API decoder",
			caps: videoCaps{HEVC444x10VAAPI: true},
			want: true,
		},
		{
			// The CPU is not enough for the stream, see withHDR.
			name: "software decoder alone",
			caps: videoCaps{HEVC444x10Software: true},
			want: false,
		},
		{
			name: "both decoders",
			caps: videoCaps{
				HEVC444x10VAAPI:    true,
				HEVC444x10Software: true,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.caps.hdrDecoder(); got != tt.want {
				t.Fatalf("hdrDecoder() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHDRAvailable(t *testing.T) {
	shown := hdrEnv{Wayland: true, ColorManager: true, OutputKnown: true, OutputHDR: true}
	tests := []struct {
		name string
		caps videoCaps
		mode videoMode
		env  hdrEnv
		want hdrState
	}{
		{
			name: "no 4:4:4 decoder",
			caps: videoCaps{},
			env:  shown,
			want: hdrState{Reason: "No decoder here handles HEVC 4:4:4 10-bit"},
		},
		{
			// Software decoding takes the stream but not at a rate that keeps a big picture smooth.
			name: "software decoder alone",
			caps: videoCaps{HEVC444x10Software: true},
			env:  shown,
			want: hdrState{Reason: "HDR needs a GPU decoder, the CPU alone is too slow for it"},
		},
		{
			name: "software decoding chosen",
			caps: videoCaps{HEVC444x10VAAPI: true},
			mode: videoSoftware,
			env:  shown,
			want: hdrState{Reason: "HDR needs the GPU decoder, and Video is set to software decoding"},
		},
		{
			name: "X11",
			caps: videoCaps{HEVC444x10VAAPI: true},
			env:  hdrEnv{ColorManager: true},
			want: hdrState{Reason: "Needs a Wayland session, X11 shows standard range only"},
		},
		{
			name: "no color manager",
			caps: videoCaps{HEVC444x10VAAPI: true},
			env:  hdrEnv{Wayland: true},
			want: hdrState{Reason: "This desktop has no color management, so it cannot show HDR"},
		},
		{
			// GNOME and KDE offer the color manager on standard range screens too.
			name: "color manager on a standard range screen",
			caps: videoCaps{HEVC444x10VAAPI: true},
			env:  hdrEnv{Wayland: true, ColorManager: true, ColorManagement: true, OutputKnown: true},
			want: hdrState{Reason: "This screen is not showing HDR. Turn HDR on for it in the display settings of the desktop"},
		},
		{
			name: "screen the desktop would not describe",
			caps: videoCaps{HEVC444x10VAAPI: true},
			env:  hdrEnv{Wayland: true, ColorManager: true, ColorManagement: true},
			want: hdrState{Reason: "The desktop would not say whether this screen shows HDR, so it stays standard range"},
		},
		{
			name: "available",
			caps: videoCaps{HEVC444x10VAAPI: true},
			env:  shown,
			want: hdrState{Available: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode := tt.mode
			if mode == "" {
				mode = videoAuto
			}
			got := hdrAvailable(tt.caps, mode, tt.env)
			if got != tt.want {
				t.Fatalf("hdrAvailable() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestOutputColorIsHDR(t *testing.T) {
	tests := []struct {
		name  string
		color outputColor
		want  bool
	}{
		{"PQ", outputColor{Known: true, Transfer: wpTransferPQ}, true},
		{"HLG", outputColor{Known: true, Transfer: wpTransferHLG}, true},
		{"sRGB at its reference", outputColor{Known: true, Transfer: 9, MaxLum: 80, ReferenceLum: 80}, false},
		{"gamma 2.2 on a bright standard range screen", outputColor{Known: true, Transfer: 2, MaxLum: 300, ReferenceLum: 203}, false},
		{"peak far above the reference", outputColor{Known: true, Transfer: 2, MaxLum: 1000, ReferenceLum: 203}, true},
		{"target peak far above the reference", outputColor{Known: true, Transfer: 2, MaxLum: 80, ReferenceLum: 80, TargetMaxLum: 600}, true},
		{"peak above an unnamed reference", outputColor{Known: true, MaxLum: 1000}, true},
		{"no luminance at all", outputColor{Known: true}, false},
		{"not described in full", outputColor{Transfer: wpTransferPQ, MaxLum: 1000}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.color.hdr(); got != tt.want {
				t.Fatalf("hdr() = %v for %+v, want %v", got, tt.color, tt.want)
			}
		})
	}
}

func TestHDRStartupNeedsARealHDRScreen(t *testing.T) {
	gpu := videoCaps{HEVC444x10VAAPI: true}
	hdrScreen := outputColor{Known: true, Transfer: wpTransferPQ}
	sdrScreen := outputColor{Known: true, Transfer: 9, MaxLum: 80, ReferenceLum: 80}
	tests := []struct {
		name        string
		wanted      bool
		caps        videoCaps
		wayland, cm bool
		screens     []outputColor
		want        bool
	}{
		{"everything there", true, gpu, true, true, []outputColor{hdrScreen}, true},
		{"one of two screens shows HDR", true, gpu, true, true, []outputColor{sdrScreen, hdrScreen}, true},
		{"not wanted", false, gpu, true, true, []outputColor{hdrScreen}, false},
		{"standard range screens only", true, gpu, true, true, []outputColor{sdrScreen}, false},
		{"screens the desktop would not describe", true, gpu, true, true, []outputColor{{}}, false},
		{"no screens", true, gpu, true, true, nil, false},
		{"decoder probe found no 4:4:4 decoder", true, videoCaps{HEVCSoftware: true}, true, true, []outputColor{hdrScreen}, false},
		{"software decoder alone", true, videoCaps{HEVC444x10Software: true}, true, true, []outputColor{hdrScreen}, false},
		{"no color manager", true, gpu, true, false, []outputColor{hdrScreen}, false},
		{"X11", true, gpu, false, true, []outputColor{hdrScreen}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hdrStartup(tt.wanted, tt.caps, videoAuto, tt.wayland, tt.cm, tt.screens); got != tt.want {
				t.Fatalf("hdrStartup() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHDREnvChangesKeepWhatThePersonSet(t *testing.T) {
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) { value, ok := env[name]; return value, ok }
	}
	t.Run("nothing set", func(t *testing.T) {
		changes := hdrEnvChanges(lookup(nil), 4, 22)
		want := []envChange{
			{Name: "GDK_DEBUG", Value: "color-mgmt"},
			{Name: "GSK_RENDERER", Value: "gl"},
		}
		if !slices.Equal(changes, want) {
			t.Fatalf("%+v, want %+v", changes, want)
		}
	})
	t.Run("their flags and renderer stay", func(t *testing.T) {
		changes := hdrEnvChanges(lookup(map[string]string{"GDK_DEBUG": "portals", "GSK_RENDERER": "vulkan"}), 4, 22)
		want := []envChange{{Name: "GDK_DEBUG", Value: "portals,color-mgmt", Before: "portals", Had: true}}
		if !slices.Equal(changes, want) {
			t.Fatalf("%+v, want %+v", changes, want)
		}
	})
	t.Run("already color managed", func(t *testing.T) {
		changes := hdrEnvChanges(lookup(map[string]string{"GDK_DEBUG": "color-mgmt", "GSK_RENDERER": "gl"}), 4, 22)
		if len(changes) != 0 {
			t.Fatalf("%+v", changes)
		}
	})
}

// What was added for GTK is taken out again, so that nothing the app starts inherits it; what the
// person had set comes back as it was, and a variable changed meanwhile is left alone.
func TestRestoreEnvStripsWhatWasAdded(t *testing.T) {
	env := map[string]string{"GDK_DEBUG": "portals", "KEEP": "1"}
	lookup := func(name string) (string, bool) { value, ok := env[name]; return value, ok }
	set := func(name, value string) error { env[name] = value; return nil }
	unset := func(name string) error { delete(env, name); return nil }
	changes := hdrEnvChanges(lookup, 4, 22)
	applyEnv(changes, set)
	if env["GDK_DEBUG"] != "portals,color-mgmt" || env["GSK_RENDERER"] != "gl" {
		t.Fatalf("not applied: %v", env)
	}
	restoreEnv(changes, lookup, set, unset)
	if env["GDK_DEBUG"] != "portals" || env["KEEP"] != "1" {
		t.Fatalf("the person's value is not back: %v", env)
	}
	if _, found := env["GSK_RENDERER"]; found {
		t.Fatalf("GSK_RENDERER leaked: %v", env)
	}

	applyEnv(changes, set)
	env["GSK_RENDERER"] = "vulkan"
	restoreEnv(changes, lookup, set, unset)
	if env["GSK_RENDERER"] != "vulkan" {
		t.Fatalf("a value changed meanwhile was overwritten: %v", env)
	}
}

func TestHDRMoveNotice(t *testing.T) {
	tests := []struct {
		name                       string
		sessionHDR, wanted, screen bool
		want                       string
	}{
		{"HDR session stays on an HDR screen", true, true, true, ""},
		{"HDR session moves to a standard range screen", true, true, false, "This screen is not showing HDR, so the desktop shows the HDR picture in standard range"},
		{"standard session moves to an HDR screen and HDR is wanted", false, true, true, "This screen shows HDR. Reconnect to get HDR video, it cannot start in the middle of a session"},
		{"HDR is not wanted", false, false, true, ""},
		{"standard session on a standard screen", false, true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hdrMoveNotice(tt.sessionHDR, tt.wanted, tt.screen); got != tt.want {
				t.Fatalf("%q, want %q", got, tt.want)
			}
		})
	}
}

func TestHDRRefusalWording(t *testing.T) {
	unavailable := hdrState{Reason: "X"}
	if got := hdrRefusal(true, unavailable); got != "HDR is not available here: X" {
		t.Fatal(got)
	}
	// The desktop can show HDR but GTK started without color management: it is not "not available",
	// and "turned on" is only true when the preference is on.
	for _, prefOn := range []bool{true, false} {
		got := hdrRefusal(prefOn, hdrState{Available: true})
		if strings.Contains(got, "not available") || prefOn && got != hdrNeedsRestart || !prefOn && strings.Contains(got, "Turned on") {
			t.Fatalf("pref %v: %q", prefOn, got)
		}
	}
}

func TestHDREffective(t *testing.T) {
	available := hdrState{Available: true}
	unavailable := hdrState{Reason: "unavailable"}

	tests := []struct {
		name   string
		wanted bool
		state  hdrState
		env    hdrEnv
		want   bool
	}{
		{
			name:   "wanted and available",
			wanted: true,
			state:  available,
			env:    hdrEnv{ColorManagement: true},
			want:   true,
		},
		{
			name:   "not wanted",
			wanted: false,
			state:  available,
			env:    hdrEnv{ColorManagement: true},
			want:   false,
		},
		{
			name:   "unavailable",
			wanted: true,
			state:  unavailable,
			env:    hdrEnv{ColorManagement: true},
			want:   false,
		},
		{
			name:   "GTK color management inactive",
			wanted: true,
			state:  available,
			env:    hdrEnv{ColorManagement: false},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hdrEffective(tt.wanted, tt.state, tt.env); got != tt.want {
				t.Fatalf("hdrEffective() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHDRRowFor(t *testing.T) {
	tests := []struct {
		name   string
		wanted bool
		state  hdrState
		env    hdrEnv
		want   hdrRow
	}{
		{
			name:   "unavailable",
			wanted: true,
			state:  hdrState{Reason: "no suitable display"},
			env:    hdrEnv{ColorManagement: true},
			want: hdrRow{
				Active:    false,
				Sensitive: false,
				Subtitle:  "no suitable display",
			},
		},
		{
			name:   "wanted without color management",
			wanted: true,
			state:  hdrState{Available: true},
			env:    hdrEnv{ColorManagement: false},
			want: hdrRow{
				Active:    true,
				Sensitive: true,
				Subtitle:  hdrRestart,
			},
		},
		{
			name:   "wanted and available",
			wanted: true,
			state:  hdrState{Available: true},
			env:    hdrEnv{ColorManagement: true},
			want: hdrRow{
				Active:    true,
				Sensitive: true,
				Subtitle:  hdrSubtitle,
			},
		},
		{
			name:   "not wanted",
			wanted: false,
			state:  hdrState{Available: true},
			env:    hdrEnv{ColorManagement: true},
			want: hdrRow{
				Active:    false,
				Sensitive: true,
				Subtitle:  hdrSubtitle,
			},
		},
		{
			name:   "not wanted without color management",
			wanted: false,
			state:  hdrState{Available: true},
			env:    hdrEnv{ColorManagement: false},
			want: hdrRow{
				Active:    false,
				Sensitive: true,
				Subtitle:  hdrSubtitle,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hdrRowFor(tt.wanted, tt.state, tt.env)
			if got != tt.want {
				t.Fatalf("hdrRowFor() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDebugFlags(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{
			name:  "empty",
			value: "",
			want:  nil,
		},
		{
			name:  "comma",
			value: "one,two",
			want:  []string{"one", "two"},
		},
		{
			name:  "all separators",
			value: "one,two:three four;five",
			want:  []string{"one", "two", "three", "four", "five"},
		},
		{
			name:  "whitespace only",
			value: "  \t ",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := debugFlags(tt.value)
			if len(got) != len(tt.want) {
				t.Fatalf("debugFlags(%q) = %#v, want %#v", tt.value, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("debugFlags(%q) = %#v, want %#v", tt.value, got, tt.want)
				}
			}
		})
	}
}

func TestGDKDebugWithColorManagement(t *testing.T) {
	tests := []struct {
		name    string
		current string
		major   int
		minor   int
		want    string
	}{
		{
			name:    "empty",
			current: "",
			major:   4,
			minor:   22,
			want:    "color-mgmt",
		},
		{
			name:    "existing flags",
			current: "foo,bar",
			major:   4,
			minor:   22,
			want:    "foo,bar,color-mgmt",
		},
		{
			name:    "mixed separators",
			current: "foo:bar baz;quux",
			major:   4,
			minor:   22,
			want:    "foo,bar,baz,quux,color-mgmt",
		},
		{
			name:    "already present",
			current: "foo,color-mgmt,bar",
			major:   4,
			minor:   22,
			want:    "foo,color-mgmt,bar",
		},
		{
			name:    "already present by itself",
			current: "color-mgmt",
			major:   4,
			minor:   22,
			want:    "color-mgmt",
		},
		{
			name:    "at default GTK version",
			current: "foo,bar",
			major:   gtkColorManagementDefault[0],
			minor:   gtkColorManagementDefault[1],
			want:    "foo,bar",
		},
		{
			name:    "above default GTK version",
			current: "foo,bar",
			major:   gtkColorManagementDefault[0] + 1,
			minor:   0,
			want:    "foo,bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gdkDebugWithColorManagement(tt.current, tt.major, tt.minor)
			if got != tt.want {
				t.Fatalf("gdkDebugWithColorManagement(%q, %d, %d) = %q, want %q",
					tt.current, tt.major, tt.minor, got, tt.want)
			}
		})
	}
}

func TestColorManagementActive(t *testing.T) {
	tests := []struct {
		name  string
		debug string
		major int
		minor int
		want  bool
	}{
		{
			name:  "older GTK without flag",
			debug: "",
			major: 4,
			minor: 22,
			want:  false,
		},
		{
			name:  "older GTK with unrelated flag",
			debug: "other",
			major: 4,
			minor: 22,
			want:  false,
		},
		{
			name:  "older GTK with color management flag",
			debug: "other,color-mgmt",
			major: 4,
			minor: 22,
			want:  true,
		},
		{
			name:  "default GTK version",
			debug: "",
			major: gtkColorManagementDefault[0],
			minor: gtkColorManagementDefault[1],
			want:  true,
		},
		{
			name:  "default GTK version with other flags",
			debug: "other",
			major: gtkColorManagementDefault[0],
			minor: gtkColorManagementDefault[1],
			want:  true,
		},
		{
			name:  "newer GTK version",
			debug: "",
			major: gtkColorManagementDefault[0] + 1,
			minor: 0,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := colorManagementActive(tt.debug, tt.major, tt.minor)
			if got != tt.want {
				t.Fatalf("colorManagementActive(%q, %d, %d) = %v, want %v",
					tt.debug, tt.major, tt.minor, got, tt.want)
			}
		})
	}
}

func TestWaylandSession(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "GDK_BACKEND selects Wayland",
			env: map[string]string{
				"GDK_BACKEND": "wayland",
			},
			want: true,
		},
		{
			name: "GDK_BACKEND takes precedence",
			env: map[string]string{
				"GDK_BACKEND":     "x11",
				"WAYLAND_DISPLAY": "wayland-0",
			},
			want: false,
		},
		{
			name: "WAYLAND_DISPLAY",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0",
			},
			want: true,
		},
		{
			name: "not a Wayland session",
			env:  map[string]string{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string {
				return tt.env[key]
			}
			if got := waylandSession(getenv); got != tt.want {
				t.Fatalf("waylandSession() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTransferName(t *testing.T) {
	tests := []struct {
		name     string
		transfer int
		want     string
	}{
		{
			name:     "PQ",
			transfer: 16,
			want:     "PQ",
		},
		{
			name:     "HLG",
			transfer: 18,
			want:     "HLG",
		},
		{
			name:     "unspecified",
			transfer: 13,
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transferName(tt.transfer); got != tt.want {
				t.Fatalf("transferName(%d) = %q, want %q", tt.transfer, got, tt.want)
			}
		})
	}
}

func TestIs444TenBit(t *testing.T) {
	tests := []struct {
		name  string
		color media.Color
		want  bool
	}{
		{
			name: "4:4:4 10-bit",
			color: media.Color{
				BitDepth:     10,
				ChromaFormat: 3,
			},
			want: true,
		},
		{
			name: "4:4:4 8-bit",
			color: media.Color{
				BitDepth:     8,
				ChromaFormat: 3,
			},
			want: false,
		},
		{
			name: "4:2:0 10-bit",
			color: media.Color{
				BitDepth:     10,
				ChromaFormat: 1,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := is444TenBit(tt.color); got != tt.want {
				t.Fatalf("is444TenBit(%#v) = %v, want %v", tt.color, got, tt.want)
			}
		})
	}
}

func TestStreamSupported(t *testing.T) {
	tests := []struct {
		name  string
		color media.Color
		hdr   bool
		want  bool
	}{
		{
			name: "standard range 4:2:0 8-bit",
			color: media.Color{
				BitDepth:     8,
				ChromaFormat: 1,
			},
			hdr:  false,
			want: true,
		},
		{
			name: "standard range with HDR requested",
			color: media.Color{
				BitDepth:     8,
				ChromaFormat: 1,
			},
			hdr:  true,
			want: true,
		},
		{
			name: "4:4:4 10-bit without HDR",
			color: media.Color{
				BitDepth:     10,
				ChromaFormat: 3,
			},
			hdr:  false,
			want: false,
		},
		{
			name: "4:4:4 10-bit with HDR",
			color: media.Color{
				BitDepth:     10,
				ChromaFormat: 3,
			},
			hdr:  true,
			want: true,
		},
		{
			name: "unsupported 4:2:2 8-bit",
			color: media.Color{
				BitDepth:     8,
				ChromaFormat: 2,
			},
			hdr:  true,
			want: false,
		},
		{
			name: "unsupported 4:2:0 10-bit",
			color: media.Color{
				BitDepth:     10,
				ChromaFormat: 1,
			},
			hdr:  true,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := streamSupported(tt.color, tt.hdr, false); got != tt.want {
				t.Fatalf("streamSupported(%#v, %v) = %v, want %v",
					tt.color, tt.hdr, got, tt.want)
			}
		})
	}
}

func TestStreamLabel(t *testing.T) {
	tests := []struct {
		name  string
		color media.Color
		kind  decoderKind
		want  string
	}{
		{
			name: "HEVC 4:4:4 10-bit HDR PQ VA-API",
			color: media.Color{
				BitDepth:     10,
				ChromaFormat: 3,
				Transfer:     16,
			},
			kind: decoderVAAPI,
			want: "HEVC 4:4:4 10-bit HDR (PQ, VA-API)",
		},
		{
			name: "HEVC 4:4:4 10-bit HDR HLG software",
			color: media.Color{
				BitDepth:     10,
				ChromaFormat: 3,
				Transfer:     18,
			},
			kind: decoderSoftware,
			want: "HEVC 4:4:4 10-bit HDR (HLG, software)",
		},
		{
			name: "HEVC 4:4:4 10-bit without transfer function",
			color: media.Color{
				BitDepth:     10,
				ChromaFormat: 3,
				Transfer:     13,
			},
			kind: decoderVAAPI,
			want: "HEVC 4:4:4 10-bit (VA-API)",
		},
		{
			name: "standard range uses video label",
			color: media.Color{
				BitDepth:     8,
				ChromaFormat: 1,
			},
			kind: decoderVAAPI,
			want: videoLabel(media.CodecHEVC, decoderVAAPI),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := streamLabel(media.CodecHEVC, tt.color, tt.kind)
			if got != tt.want {
				t.Fatalf("streamLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRendererForHDR(t *testing.T) {
	cases := []struct {
		current      string
		major, minor int
		want         string
	}{
		{"", 4, 22, "gl"},
		{"", 4, 18, "gl"},
		{"", 4, 16, "ngl"},
		{"", 5, 0, "gl"},
		{"vulkan", 4, 22, "vulkan"},
		{"cairo", 4, 16, "cairo"},
	}
	for _, c := range cases {
		if got := rendererForHDR(c.current, c.major, c.minor); got != c.want {
			t.Errorf("rendererForHDR(%q, %d, %d) = %q, want %q", c.current, c.major, c.minor, got, c.want)
		}
	}
}

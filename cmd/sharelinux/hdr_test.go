package main

import (
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
			name: "software decoder",
			caps: videoCaps{HEVC444x10Software: true},
			want: true,
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
	tests := []struct {
		name string
		caps videoCaps
		env  hdrEnv
		want hdrState
	}{
		{
			name: "no 4:4:4 decoder",
			caps: videoCaps{},
			env: hdrEnv{
				Wayland:         true,
				ColorManager:    true,
				ColorManagement: true,
			},
			want: hdrState{Reason: "No decoder here handles HEVC 4:4:4 10-bit"},
		},
		{
			name: "X11",
			caps: videoCaps{HEVC444x10VAAPI: true},
			env: hdrEnv{
				Wayland:      false,
				ColorManager: true,
			},
			want: hdrState{Reason: "Needs a Wayland session, X11 shows standard range only"},
		},
		{
			name: "no color manager",
			caps: videoCaps{HEVC444x10Software: true},
			env: hdrEnv{
				Wayland:      true,
				ColorManager: false,
			},
			want: hdrState{Reason: "This desktop has no color management, so it cannot show HDR"},
		},
		{
			name: "available",
			caps: videoCaps{HEVC444x10VAAPI: true},
			env: hdrEnv{
				Wayland:         true,
				ColorManager:    true,
				ColorManagement: false,
			},
			want: hdrState{Available: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hdrAvailable(tt.caps, tt.env)
			if got != tt.want {
				t.Fatalf("hdrAvailable() = %#v, want %#v", got, tt.want)
			}
		})
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
			if got := streamSupported(tt.color, tt.hdr); got != tt.want {
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

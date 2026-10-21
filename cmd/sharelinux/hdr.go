package main

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// HDR from the Mac is HEVC 4:4:4 10-bit in full range, with PQ or HLG in the stream's color
// description. This file decides whether the viewer can ask for it and how to say so; none of it
// needs GTK or libavcodec, so the decisions are tested without them.

// hdrEnv is what the display server offers for showing HDR.
type hdrEnv struct {
	// Wayland is set in a Wayland session. X11 can only show standard dynamic range.
	Wayland bool
	// ColorManager is set when the compositor offers wp_color_manager_v1, so it can be told what
	// the colors of a surface mean.
	ColorManager bool
	// ColorManagement is set when GTK in this process speaks that protocol: it is opt-in
	// (GDK_DEBUG=color-mgmt) and decided when GTK starts.
	ColorManagement bool
}

// hdrEnvironment asks the display server what it offers for HDR. The Linux build sets it; it
// stays nil where there is no display code.
var hdrEnvironment func() hdrEnv

// hdrState says whether this machine can show HDR video at all.
type hdrState struct {
	Available bool
	// Reason says why not, in words for the preferences.
	Reason string
}

// hdrDecoder reports whether some decoder here takes HEVC 4:4:4 10-bit.
func (c videoCaps) hdrDecoder() bool {
	return c.HEVC444x10VAAPI || c.HEVC444x10Software
}

// hdrAvailable decides from the decoders and the display server.
func hdrAvailable(caps videoCaps, env hdrEnv) hdrState {
	switch {
	case !caps.hdrDecoder():
		return hdrState{Reason: "No decoder here handles HEVC 4:4:4 10-bit"}
	case !env.Wayland:
		return hdrState{Reason: "Needs a Wayland session, X11 shows standard range only"}
	case !env.ColorManager:
		return hdrState{Reason: "This desktop has no color management, so it cannot show HDR"}
	}
	return hdrState{Available: true}
}

// hdrEffective says whether a session asks the Mac for HDR: the person wants it and the machine
// can show it in this process.
func hdrEffective(wanted bool, state hdrState, env hdrEnv) bool {
	return wanted && state.Available && env.ColorManagement
}

// hdrRow is how the HDR switch of the preferences looks.
type hdrRow struct {
	Active, Sensitive bool
	Subtitle          string
}

const (
	hdrSubtitle = "Ask the Mac for HDR video. The Mac sends it for a virtual display"
	// hdrRestart is shown when the switch was turned on in this run: GTK decides about color
	// management when it starts.
	hdrRestart = "Turned on. Restart Screen Sharing to show HDR"
)

// hdrRowFor decides the switch: off and dimmed, with the reason, where HDR cannot be shown.
func hdrRowFor(wanted bool, state hdrState, env hdrEnv) hdrRow {
	if !state.Available {
		return hdrRow{Subtitle: state.Reason}
	}
	if wanted && !env.ColorManagement {
		return hdrRow{Active: true, Sensitive: true, Subtitle: hdrRestart}
	}
	return hdrRow{Active: wanted, Sensitive: true, Subtitle: hdrSubtitle}
}

// gtkColorManagementDefault is the first GTK version, as (major, minor), that turns color
// management on by itself. As of 4.22 it is still opt-in; lower this when a release changes that.
var gtkColorManagementDefault = [2]int{99, 0}

const colorManagementFlag = "color-mgmt"

func debugFlags(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ':' || r == ';' || unicode.IsSpace(r) })
}

// gdkDebugWithColorManagement returns GDK_DEBUG with color management added. It returns the value
// as it is when the flag is there already, or when this GTK turns color management on itself.
func gdkDebugWithColorManagement(current string, major, minor int) string {
	if major > gtkColorManagementDefault[0] || major == gtkColorManagementDefault[0] && minor >= gtkColorManagementDefault[1] {
		return current
	}
	flags := debugFlags(current)
	if slices.Contains(flags, colorManagementFlag) {
		return current
	}
	return strings.Join(append(flags, colorManagementFlag), ",")
}

// colorManagementActive says whether a GTK of this version started with this GDK_DEBUG draws with
// color management.
func colorManagementActive(debug string, major, minor int) bool {
	return gdkDebugWithColorManagement("", major, minor) == "" || slices.Contains(debugFlags(debug), colorManagementFlag)
}

// waylandSession says whether GTK opens a Wayland display, from the environment it reads.
func waylandSession(getenv func(string) string) bool {
	backend := getenv("GDK_BACKEND")
	if backend != "" {
		return strings.HasPrefix(backend, "wayland")
	}
	return getenv("WAYLAND_DISPLAY") != ""
}

// transferName is the name of a transfer function code point: PQ or HLG for HDR, empty otherwise.
func transferName(transfer int) string {
	switch transfer {
	case media.TransferPQ:
		return "PQ"
	case media.TransferHLG:
		return "HLG"
	}
	return ""
}

// is444TenBit reports whether a stream is the HEVC RExt 4:4:4 10-bit one that HDR needs.
func is444TenBit(color media.Color) bool {
	return color.BitDepth == 10 && color.ChromaFormat == 3
}

// streamSupported reports whether the viewer decodes a stream of this color description:
// standard range 4:2:0 8-bit always, 4:4:4 10-bit when HDR was asked for.
func streamSupported(color media.Color, hdr bool) bool {
	if color.BitDepth == 8 && color.ChromaFormat == 1 {
		return true
	}
	return hdr && is444TenBit(color)
}

// streamLabel is the stats card's Codec row: HEVC 4:4:4 10-bit HDR (PQ, VA-API) for HDR video.
func streamLabel(codec media.Codec, color media.Color, kind decoderKind) string {
	if !is444TenBit(color) {
		return videoLabel(codec, kind)
	}
	name := codecName(codec) + " 4:4:4 10-bit"
	if transfer := transferName(color.Transfer); transfer != "" {
		return fmt.Sprintf("%s HDR (%s, %s)", name, transfer, kind)
	}
	return fmt.Sprintf("%s (%s)", name, kind)
}

// rendererForHDR is the GSK_RENDERER to start GTK with when HDR is wanted. The GPU makes a P010 picture
// of HDR video for GTK, and only the GL renderer draws that straight from the DMA-BUF: the Vulkan one
// downloads it and converts it on the CPU, about a quarter of the speed at 4K. GTK 4.18 renamed the
// new GL renderer from ngl to gl. A renderer the person chose stays.
func rendererForHDR(current string, major, minor int) string {
	if current != "" {
		return current
	}
	if major > 4 || major == 4 && minor >= 18 {
		return "gl"
	}
	return "ngl"
}

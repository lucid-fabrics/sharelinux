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
	// OutputKnown is set when the compositor described the screen the window is on, and OutputHDR
	// when that description is HDR. GNOME and KDE offer the color manager on standard range screens
	// too, so the protocol being there says nothing about the screen.
	OutputKnown, OutputHDR bool
}

// outputColor is what the compositor says about the colors of one screen: the transfer function as a
// wp_color_manager_v1 code point (0 when none is named) and the luminances in cd/m2.
type outputColor struct {
	Known bool
	// Transfer is the named transfer function of the screen's image description.
	Transfer uint32
	// MaxLum and ReferenceLum are the luminances; TargetMaxLum is the target luminance, 0 when none is named.
	MaxLum, ReferenceLum, TargetMaxLum uint32
}

// Code points of wp_color_manager_v1.transfer_function that name HDR.
const (
	wpTransferPQ  = 11
	wpTransferHLG = 13
)

// sdrReferenceNits is the white of standard range content when a screen does not say (ITU-R BT.2408).
const sdrReferenceNits = 203

// hdr reports whether the screen shows HDR: its image description names PQ or HLG, or its peak
// luminance is clearly above the reference white. A description the compositor did not give in
// full is standard range.
func (o outputColor) hdr() bool {
	if !o.Known {
		return false
	}
	if o.Transfer == wpTransferPQ || o.Transfer == wpTransferHLG {
		return true
	}
	reference := o.ReferenceLum
	if reference == 0 {
		reference = sdrReferenceNits
	}
	return max(o.MaxLum, o.TargetMaxLum) > 2*reference
}

// withOutput returns the environment with the screen's description in it.
func (e hdrEnv) withOutput(o outputColor) hdrEnv {
	e.OutputKnown, e.OutputHDR = o.Known, o.hdr()
	return e
}

// hdrEnvironment asks the display server what it offers for HDR. The Linux build sets it; it
// stays nil where there is no display code.
var hdrEnvironment func() hdrEnv

// hdrOutputProbe asks the compositor what the screen the window is on shows. The Linux build sets
// it; it runs on the GTK thread.
var hdrOutputProbe func() outputColor

// hdrState says whether this machine can show HDR video at all.
type hdrState struct {
	Available bool
	// Reason says why not, in words for the preferences.
	Reason string
}

// hdrDecoder reports whether the GPU decodes HEVC 4:4:4 10-bit. HDR is offered only with it: the
// stream is twice the samples of the 4:2:0 the decoder pixel rate is measured for, at 10 bits, and
// software decoding has no rate that keeps a big picture smooth (see withHDR).
func (c videoCaps) hdrDecoder() bool {
	return c.HEVC444x10VAAPI
}

// hdrAvailable decides from the decoders, the video mode and the display server.
func hdrAvailable(caps videoCaps, mode videoMode, env hdrEnv) hdrState {
	switch {
	case !caps.hdrDecoder() && caps.HEVC444x10Software:
		return hdrState{Reason: "HDR needs a GPU decoder, the CPU alone is too slow for it"}
	case !caps.hdrDecoder():
		return hdrState{Reason: "No decoder here handles HEVC 4:4:4 10-bit"}
	case mode == videoSoftware:
		return hdrState{Reason: "HDR needs the GPU decoder, and Video is set to software decoding"}
	case !env.Wayland:
		return hdrState{Reason: "Needs a Wayland session, X11 shows standard range only"}
	case !env.ColorManager:
		return hdrState{Reason: "This desktop has no color management, so it cannot show HDR"}
	case !env.OutputKnown:
		return hdrState{Reason: "The desktop would not say whether this screen shows HDR, so it stays standard range"}
	case !env.OutputHDR:
		return hdrState{Reason: "This screen is not showing HDR. Turn HDR on for it in the display settings of the desktop"}
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
	// hdrNeedsRestart is what to do when GTK started without color management: it decides about
	// that when it starts.
	hdrNeedsRestart = "Restart Screen Sharing to show HDR"
	// hdrRestart is shown on the switch of the preferences when it was turned on in this run.
	hdrRestart = "Turned on. " + hdrNeedsRestart
)

// hdrRefusal is what the HDR button of a session says when HDR cannot be switched on. A machine that
// cannot show HDR gets the reason; one that can, once Screen Sharing starts again, gets the restart,
// and "turned on" only when the preference is on.
func hdrRefusal(prefOn bool, state hdrState) string {
	switch {
	case !state.Available:
		return "HDR is not available here: " + state.Reason
	case prefOn:
		return hdrNeedsRestart
	}
	return "Turn on HDR in the preferences, then restart Screen Sharing to show it"
}

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

// envChange is a variable this app set for GTK before it started: what it was, and what it is now.
type envChange struct {
	Name, Value string
	// Before is the value the variable had, when Had is set.
	Before string
	Had    bool
}

// hdrEnvChanges is what GTK needs in its environment to draw HDR: GDK_DEBUG with color management
// and the GL renderer. Whatever the person set stays: their GDK_DEBUG flags keep their place
// and their GSK_RENDERER is not touched.
func hdrEnvChanges(lookup func(string) (string, bool), major, minor int) []envChange {
	var changes []envChange
	debug, had := lookup("GDK_DEBUG")
	if value := gdkDebugWithColorManagement(debug, major, minor); value != debug {
		changes = append(changes, envChange{Name: "GDK_DEBUG", Value: value, Before: debug, Had: had})
	}
	if renderer, had := lookup("GSK_RENDERER"); renderer == "" {
		changes = append(changes, envChange{Name: "GSK_RENDERER", Value: rendererForHDR("", major, minor), Before: renderer, Had: had})
	}
	return changes
}

// applyEnv sets the changes.
func applyEnv(changes []envChange, set func(name, value string) error) {
	for _, change := range changes {
		_ = set(change.Name, change.Value)
	}
}

// restoreEnv puts the variables back to what they were once GTK has read them, so that nothing the
// app starts (a browser opened from the About link) inherits what only GTK needed. A variable that
// someone changed meanwhile is left alone.
func restoreEnv(changes []envChange, lookup func(string) (string, bool), set func(name, value string) error, unset func(string) error) {
	for _, change := range changes {
		if current, _ := lookup(change.Name); current != change.Value {
			continue
		}
		if change.Had {
			_ = set(change.Name, change.Before)
		} else {
			_ = unset(change.Name)
		}
	}
}

// hdrStartup says, before GTK starts, whether to switch on what HDR needs: the person wants it, and
// it can really be shown (a Wayland session, the GPU decodes 4:4:4 10-bit, the compositor has color
// management and some screen shows HDR). Otherwise a standard range session would run on the GL
// renderer for nothing.
func hdrStartup(wanted bool, caps videoCaps, mode videoMode, wayland, colorManager bool, screens []outputColor) bool {
	if !wanted || !wayland {
		return false
	}
	env := hdrEnv{Wayland: true, ColorManager: colorManager}
	for _, screen := range screens {
		env.OutputKnown = env.OutputKnown || screen.Known
		env.OutputHDR = env.OutputHDR || screen.hdr()
	}
	return hdrAvailable(caps, mode, env).Available
}

// hdrMoveNotice is what to tell the person when the window comes to a screen that is not the kind
// the session was started for. HDR is decided when a session connects, because the Mac sends a
// different stream, so a session started in standard range does not turn to HDR on its own on an HDR
// screen: it needs a reconnect, and the message says so. An HDR session on a standard range screen
// keeps going, and the desktop converts the picture.
func hdrMoveNotice(sessionHDR, wanted, screenHDR bool) string {
	switch {
	case sessionHDR && !screenHDR:
		return "This screen is not showing HDR, so the desktop shows the HDR picture in standard range"
	case !sessionHDR && wanted && screenHDR:
		return "This screen shows HDR. Reconnect to get HDR video, it cannot start in the middle of a session"
	}
	return ""
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

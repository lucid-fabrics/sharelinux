package main

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// videoProbe asks the machine what it can decode. The Linux build sets it; it
// stays nil where there is no decoder code.
var videoProbe func(device string) videoCaps

// soundDecoderAvailable says whether this build can decode the Mac's sound. The Linux build sets it.
var soundDecoderAvailable func() bool

// serverHasVideo reports whether the Mac offers the media stream: the same two
// client messages the Swift apps check before they ask for High Performance: the
// media stream (0x1c) and the display configuration (0x1d).
func serverHasVideo(info *rfb.ServerInfo) bool {
	return info.Accepts(media.ClientMessageMediaStream) && info.Accepts(0x1d)
}

// The video stream is the Mac's HEVC picture: far lighter on the network and on
// the CPU than the framebuffer tiles when the screen moves. This file decides
// which decoder to use and what to offer the Mac; none of it needs the GPU or
// libavcodec, so the decisions are tested without them.

// videoMode is the person's choice for the video stream.
type videoMode string

const (
	// videoAuto uses the video stream when the Mac has it and this machine can decode it.
	videoAuto videoMode = "auto"
	// videoAlways uses the video stream even where the picture tiles would cost less.
	videoAlways videoMode = "always"
	// videoSoftware never uses the GPU decoder.
	videoSoftware videoMode = "software"
	// videoOff keeps to the framebuffer tiles.
	videoOff videoMode = "off"
)

var videoModes = []videoMode{videoAuto, videoAlways, videoSoftware, videoOff}

// parseVideoMode reads a setting or environment value; anything unknown is auto.
func parseVideoMode(value string) videoMode {
	switch videoMode(strings.ToLower(strings.TrimSpace(value))) {
	case videoSoftware:
		return videoSoftware
	case videoAlways:
		return videoAlways
	case videoOff, "tiles", "0", "false", "no":
		return videoOff
	}
	return videoAuto
}

// videoModeLabel is the word in the preferences.
func videoModeLabel(mode videoMode) string {
	switch mode {
	case videoAlways:
		return "Always (even when the tiles are cheaper)"
	case videoSoftware:
		return "Software decoding only"
	case videoOff:
		return "Off (picture tiles)"
	}
	return "Automatic"
}

// decoderKind is how a stream is decoded.
type decoderKind int

const (
	decoderNone decoderKind = iota
	decoderVAAPI
	decoderSoftware
)

func (k decoderKind) String() string {
	switch k {
	case decoderVAAPI:
		return "VA-API"
	case decoderSoftware:
		return "software"
	}
	return "none"
}

// outputKind is how a decoded picture reaches the screen.
type outputKind int

const (
	outputNone outputKind = iota
	// outputDMABuf shows the decoder's own GPU surface, with no copy.
	outputDMABuf
	// outputCopy reads the picture back from the GPU into memory.
	outputCopy
	// outputMemory is a picture decoded in memory.
	outputMemory
)

func (o outputKind) String() string {
	switch o {
	case outputDMABuf:
		return "DMA-BUF"
	case outputCopy:
		return "GPU copy"
	case outputMemory:
		return "Memory"
	}
	return "none"
}

// videoCaps are the decoders a machine has, from probing it.
type videoCaps struct {
	HEVCVAAPI, HEVCSoftware bool
	H264VAAPI, H264Software bool
	// HEVC444x10VAAPI and HEVC444x10Software say whether a decoder really decoded a tiny
	// HEVC 4:4:4 10-bit stream, the profile of HDR video.
	HEVC444x10VAAPI, HEVC444x10Software bool
	// HEVC444x8VAAPI and HEVC444x8Software say the same for a 4:4:4 8-bit stream, the picture the
	// Mac sends in standard range when it is offered.
	HEVC444x8VAAPI, HEVC444x8Software bool
	// Device is the VA-API node that was probed, Driver what the driver calls itself.
	Device, Driver string
	// Error says why VA-API is missing, when it is.
	Error string
}

// any reports whether some decoder exists.
func (c videoCaps) any() bool {
	return c.HEVCVAAPI || c.HEVCSoftware || c.H264VAAPI || c.H264Software
}

// videoHint is the line under the Video preference that says what this machine can do.
// emptyParens matches the "()" a VA-API driver prints after its version when it has no build detail.
var emptyParens = regexp.MustCompile(`\s*\(\s*\)`)

// driverLabel is the driver's own name without an empty "()" left over, and without stray space.
func driverLabel(name string) string {
	return strings.TrimSpace(emptyParens.ReplaceAllString(name, ""))
}

func videoHint(caps videoCaps) string {
	switch {
	case !caps.any():
		return "No HEVC decoder was found. In the Flatpak it comes with org.freedesktop.Platform.codecs-extra"
	case caps.HEVCVAAPI || caps.H264VAAPI:
		if name := driverLabel(caps.Driver); name != "" {
			return "Decodes on the GPU: " + name
		}
		return "Decodes on the GPU"
	case caps.Error != "":
		return "Decodes on the CPU, there is no VA-API decoder here (" + caps.Error + ")"
	}
	return "Decodes on the CPU"
}

// softwareRenderLimit is the picture size, in pixels, up to which a window drawn by the CPU still
// shows video at 60 Hz without choking: on llvmpipe a 1920x1080 picture takes about 1.4 cores,
// 2560x1600 takes all four and drops half the pictures.
const softwareRenderLimit = 2_200_000

// videoPaysOff says whether the video stream beats the picture tiles. Video repaints the whole
// picture every frame, which a GPU does for free and a CPU renderer does not: a big picture on
// llvmpipe or Cairo costs several cores for 15 frames a second, where tiles repaint only what
// changed. Only the automatic mode decides this; the others do what they say.
func videoPaysOff(mode videoMode, accelerated bool, pixels int) (bool, string) {
	if mode == videoAuto && !accelerated && pixels > softwareRenderLimit {
		return false, "the window is drawn by the CPU and the picture is big; the tiles are cheaper (-video always forces the video)"
	}
	return true, ""
}

// videoPlan is what to ask the Mac for and how to decode what comes back.
type videoPlan struct {
	// Reason says why there is no plan, for the log and the preferences.
	Reason string
	Offer  media.OfferOptions
	Codec  media.Codec
	// Kinds are the decoders to try for Codec, best first. A decoder that fails
	// hands over to the next one; when the last fails the tiles take over.
	Kinds []decoderKind
	// HDR is set when the offer asks for HEVC 4:4:4 10-bit too. HDRKinds are the decoders that
	// take such a stream; Kinds minus them still decode the 4:2:0 one.
	HDR      bool
	HDRKinds []decoderKind
	// Chroma444 is set when the offer asks for HEVC 4:4:4 8-bit too. Kinds444 are the decoders that
	// take such a stream.
	Chroma444 bool
	Kinds444  []decoderKind
	// Sound is set when the offer asks for the Mac's sound.
	Sound bool
}

func (p videoPlan) enabled() bool { return len(p.Kinds) > 0 }

// planVideo picks the codec and the decoders. HEVC is what the Mac sends best;
// H.264 is only offered on a machine with no HEVC decoder at all. A server
// without the video stream, or the person turning it off, gives no plan.
func planVideo(mode videoMode, caps videoCaps, serverHasVideo bool) videoPlan {
	switch {
	case mode == videoOff:
		return videoPlan{Reason: "video is off"}
	case !serverHasVideo:
		return videoPlan{Reason: "the server has no video stream"}
	case !caps.any():
		return videoPlan{Reason: "no HEVC or H.264 decoder here"}
	}
	plan := videoPlan{}
	vaapi, software := caps.HEVCVAAPI, caps.HEVCSoftware
	plan.Codec = media.CodecHEVC
	if !vaapi && !software {
		plan.Codec = media.CodecH264
		vaapi, software = caps.H264VAAPI, caps.H264Software
	}
	if mode == videoSoftware {
		vaapi = false
	}
	if vaapi {
		plan.Kinds = append(plan.Kinds, decoderVAAPI)
	}
	if software {
		plan.Kinds = append(plan.Kinds, decoderSoftware)
	}
	if !plan.enabled() {
		plan.Reason = "no usable decoder in " + string(mode) + " mode"
		return plan
	}
	plan.Offer = media.OfferOptions{
		Codecs: []media.Codec{plan.Codec},
		// Standard dynamic range, 4:2:0, full or video range. HDR adds 4:4:4 10-bit (withHDR).
		PixelFormats:  media.PixelFormatsSDR420,
		TilesPerFrame: 1,
		// A viewer on a LAN wants the newest picture, not a smooth delay: the library hands
		// units over as they complete and the widget shows the latest.
		ImmediatePlayout: true,
	}
	return plan
}

// withHDR adds HDR to a plan: the offer names 4:4:4 10-bit full range, which makes the Mac
// offer HDR mode 3, and only the GPU decoder, when it took the probe stream of that profile, may
// decode it. Software decoding is left out on purpose. The decoder pixel rate the Mac gets is
// measured for 4:2:0 8-bit, and 4:4:4 10-bit is twice the samples at more bits each; a rate that
// held the picture smooth would be so low that a virtual display drops to a few frames a second,
// and a rate that was not lowered would let the Mac send what the CPU cannot decode. A plan whose
// codec is not HEVC, or whose machine has no such GPU decoder, stays as it is.
func (p videoPlan) withHDR(mode videoMode, caps videoCaps) videoPlan {
	if !p.enabled() || p.Codec != media.CodecHEVC {
		return p
	}
	if !caps.hdrDecoder() || mode == videoSoftware || !slices.Contains(p.Kinds, decoderVAAPI) {
		return p
	}
	p.HDR = true
	p.HDRKinds = []decoderKind{decoderVAAPI}
	p.Offer.PixelFormats |= media.PixelFormat444TenBitFullRange
	return p
}

// with444 adds 4:4:4 8-bit to a plan: standard range video keeps the full resolution of the colors,
// which is what fine colored text and thin lines need. The Mac sends it whenever the offer allows.
// Only the GPU decoder is trusted with it, and only when it decoded a probe stream of the profile: a
// 4:4:4 picture has twice the samples of a 4:2:0 one, which the CPU of this machine may not keep up
// with. A plan whose codec is not HEVC, or whose machine has no such GPU decoder, stays as it is.
func (p videoPlan) with444(mode videoMode, caps videoCaps) videoPlan {
	if !p.enabled() || p.Codec != media.CodecHEVC {
		return p
	}
	if !caps.HEVC444x8VAAPI || mode == videoSoftware || !slices.Contains(p.Kinds, decoderVAAPI) {
		return p
	}
	p.Chroma444 = true
	p.Kinds444 = []decoderKind{decoderVAAPI}
	p.Offer.PixelFormats |= media.PixelFormat444FullRange
	return p
}

// without444 takes 4:4:4 8-bit out of the offer again, for a machine whose GPU decoder failed on it.
func (p videoPlan) without444() videoPlan {
	p.Chroma444 = false
	p.Kinds444 = nil
	p.Offer.PixelFormats &^= media.PixelFormat444FullRange
	return p
}

// withSound asks the Mac for its sound as well: the offer stops being receive-only. It does nothing
// where the sound is off or this machine has no AAC decoder.
func (p videoPlan) withSound(wanted, decoder bool) videoPlan {
	if !p.enabled() || !wanted || !decoder {
		return p
	}
	p.Sound = true
	p.Offer.Audio = true
	return p
}

// kindsFor are the decoders to try for a stream, best first: HDR video only goes to the ones
// that handle 4:4:4 10-bit, and 4:4:4 8-bit to the ones that handle that.
func (p videoPlan) kindsFor(color media.Color) []decoderKind {
	var allowed []decoderKind
	switch {
	case is444TenBit(color):
		allowed = p.HDRKinds
	case is444EightBit(color):
		allowed = p.Kinds444
	default:
		return p.Kinds
	}
	kinds := make([]decoderKind, 0, len(allowed))
	for _, kind := range allowed {
		if slices.Contains(p.Kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// withoutKind drops a decoder that failed, and what is left is the plan to carry on with.
func (p videoPlan) withoutKind(kind decoderKind) videoPlan {
	drop := func(kinds []decoderKind) []decoderKind {
		kept := make([]decoderKind, 0, len(kinds))
		for _, k := range kinds {
			if k != kind {
				kept = append(kept, k)
			}
		}
		return kept
	}
	kept := drop(p.Kinds)
	p.Kinds = kept
	p.HDRKinds = drop(p.HDRKinds)
	p.Kinds444 = drop(p.Kinds444)
	if len(kept) == 0 {
		p.Reason = kind.String() + " decoding failed"
	}
	return p
}

// Pixel rates (pixels per second times three, the way the Mac divides them) that
// each decoder is trusted with. The Mac lowers the frame rate to 30 when a
// display is too big for the rate at 60.
const (
	// rateVAAPI is 3840x2160 at 60 Hz: the media block of any Intel GPU since Gen 9.
	rateVAAPI int64 = 3840 * 2160 * 60 * 3
	// rateSoftwareThread is what one thread of software decoding keeps up with, a quarter of
	// 2560x1600 at 60 Hz. Measured on four cores of a 12th gen Intel with busy test video: 5120x1440
	// decodes in 15 ms (about 480 megapixels a second), twice what this rate asks for.
	rateSoftwareThread int64 = 2560 * 1600 * 60 * 3 / 4
	softwareThreads          = 4
)

// decoderPixelRate is what the first decoder of the plan can keep up with, given the threads
// software decoding may use. A plan that offers 4:4:4 8-bit counts every picture twice, as it has
// twice the samples of a 4:2:0 one: the Mac then picks a lower frame rate for a display that is too
// big for the GPU at 4:4:4.
func (p videoPlan) decoderPixelRate(threads int) int64 {
	if !p.enabled() {
		return 0
	}
	first := p.Kinds[0]
	if p.HDR && len(p.HDRKinds) > 0 {
		// HDR video is decoded by the HDR decoders alone, whatever else the plan holds.
		first = p.HDRKinds[0]
	}
	if first == decoderVAAPI {
		if p.Chroma444 && !p.HDR {
			return rateVAAPI / 2
		}
		return rateVAAPI
	}
	return rateSoftwareThread * int64(max(1, min(threads, softwareThreads)))
}

// codecName is the stream's codec as a person writes it.
func codecName(codec media.Codec) string {
	if codec == media.CodecH264 {
		return "H.264"
	}
	return "HEVC"
}

// videoLabel is the stats card's Codec row for a running stream.
func videoLabel(codec media.Codec, kind decoderKind) string {
	return fmt.Sprintf("%s (%s)", codecName(codec), kind)
}

// videoStats is what the stats card and the log show about the running stream.
type videoStats struct {
	Active bool
	Codec  media.Codec
	Kind   decoderKind
	// Color is what the stream's header says about its picture, once it is known.
	Color  media.Color
	Output outputKind
	// Units went into the decoder, Frames came out, Presented were drawn.
	Units, Frames, Presented uint64
	// Dropped counts pictures thrown away: units the queue shed and pictures replaced
	// before they were drawn.
	Dropped, Errors uint64
	DecodeAvg       time.Duration
	DecodeLast      time.Duration
	DecodeMax       time.Duration
	// Age is how old a picture was, from its first packet to being drawn.
	AgeAvg, AgeLast time.Duration
	Width, Height   int
	// Sound is the Mac's sound, when the stream carries it (SoundOn).
	Sound   soundStats
	SoundOn bool
}

// applyVideoRows changes a stats card for a running video stream: the Codec row
// names the decoder, and the decode time, the path to the screen and the
// drops follow it.
func applyVideoRows(rows []statRow, v videoStats) []statRow {
	if !v.Active {
		return rows
	}
	out := make([]statRow, 0, len(rows)+3)
	for _, row := range rows {
		if row.Label == "Codec" {
			row.Value = streamLabel(v.Codec, v.Color, v.Kind)
			out = append(out, row)
			out = append(out,
				statRow{"Decode", formatMillis(v.DecodeAvg)},
				statRow{"Output", v.Output.String()},
				statRow{"Dropped", fmt.Sprint(v.Dropped)},
			)
			if v.SoundOn {
				out = append(out, statRow{"Sound", soundRow(v.Sound)})
			}
			continue
		}
		out = append(out, row)
	}
	return out
}

// soundRow is the stats card's Sound row: the delay from the network to the speakers, and the gaps
// and drops so far.
func soundRow(s soundStats) string {
	if s.Muted {
		return "Muted"
	}
	return fmt.Sprintf("%.0f ms, %d gaps, %d dropped", float64(s.Delay())/float64(time.Millisecond), s.Counters.Underruns, s.Counters.Dropped)
}

func formatMillis(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
}

// videoThreads is how many threads software decoding may use.
func videoThreads() int {
	if value, err := strconv.Atoi(os.Getenv("SHARELINUX_THREADS")); err == nil && value > 0 {
		return value
	}
	return min(runtime.NumCPU(), 4)
}

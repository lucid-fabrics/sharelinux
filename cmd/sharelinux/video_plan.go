package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// videoProbe asks the machine what it can decode. The Linux build sets it; it
// stays nil where there is no decoder code.
var videoProbe func(device string) videoCaps

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
	// videoSoftware never uses the GPU decoder.
	videoSoftware videoMode = "software"
	// videoOff keeps to the framebuffer tiles.
	videoOff videoMode = "off"
)

var videoModes = []videoMode{videoAuto, videoSoftware, videoOff}

// parseVideoMode reads a setting or environment value; anything unknown is auto.
func parseVideoMode(value string) videoMode {
	switch videoMode(strings.ToLower(strings.TrimSpace(value))) {
	case videoSoftware:
		return videoSoftware
	case videoOff, "tiles", "0", "false", "no":
		return videoOff
	}
	return videoAuto
}

// videoModeLabel is the word in the preferences.
func videoModeLabel(mode videoMode) string {
	switch mode {
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
	// Device is the VA-API node that was probed, Driver what the driver calls itself.
	Device, Driver string
	// Error says why VA-API is missing, when it is.
	Error string
}

// any reports whether some decoder exists.
func (c videoCaps) any() bool {
	return c.HEVCVAAPI || c.HEVCSoftware || c.H264VAAPI || c.H264Software
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
		// Standard dynamic range, 4:2:0, full or video range. HDR and 4:4:4 are later work.
		PixelFormats:  media.PixelFormatsSDR420,
		FrameRate60:   true,
		TilesPerFrame: 1,
		// A viewer on a LAN wants the newest picture, not a smooth delay: the library hands
		// units over as they complete and the widget shows the latest.
		ImmediatePlayout: true,
	}
	return plan
}

// withoutKind drops a decoder that failed, and what is left is the plan to carry on with.
func (p videoPlan) withoutKind(kind decoderKind) videoPlan {
	kept := make([]decoderKind, 0, len(p.Kinds))
	for _, k := range p.Kinds {
		if k != kind {
			kept = append(kept, k)
		}
	}
	p.Kinds = kept
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
	// rateSoftware is 1920x1080 at 60 Hz.
	rateSoftware int64 = 1920 * 1080 * 60 * 3
)

// decoderPixelRate is what the first decoder of the plan can keep up with.
func (p videoPlan) decoderPixelRate() int64 {
	if !p.enabled() {
		return 0
	}
	if p.Kinds[0] == decoderVAAPI {
		return rateVAAPI
	}
	return rateSoftware
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
			row.Value = videoLabel(v.Codec, v.Kind)
			out = append(out, row)
			out = append(out,
				statRow{"Decode", formatMillis(v.DecodeAvg)},
				statRow{"Output", v.Output.String()},
				statRow{"Dropped", fmt.Sprint(v.Dropped)},
			)
			continue
		}
		out = append(out, row)
	}
	return out
}

func formatMillis(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
}

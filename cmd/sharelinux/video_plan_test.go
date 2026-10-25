package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

func TestParseVideoMode(t *testing.T) {
	cases := map[string]videoMode{
		"":          videoAuto,
		"auto":      videoAuto,
		"AUTO":      videoAuto,
		"software":  videoSoftware,
		" Software": videoSoftware,
		"off":       videoOff,
		"tiles":     videoOff,
		"0":         videoOff,
		"false":     videoOff,
		"no":        videoOff,
		"vaapi":     videoAuto,
		"nonsense":  videoAuto,
	}
	for in, want := range cases {
		if got := parseVideoMode(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestPlanVideo(t *testing.T) {
	both := videoCaps{HEVCVAAPI: true, HEVCSoftware: true, H264VAAPI: true, H264Software: true}
	cases := []struct {
		name      string
		mode      videoMode
		caps      videoCaps
		server    bool
		wantKinds []decoderKind
		wantCodec media.Codec
	}{
		{"auto prefers the GPU then software", videoAuto, both, true, []decoderKind{decoderVAAPI, decoderSoftware}, media.CodecHEVC},
		{"software mode never uses the GPU", videoSoftware, both, true, []decoderKind{decoderSoftware}, media.CodecHEVC},
		{"off is off", videoOff, both, true, nil, 0},
		{"no video stream on the server", videoAuto, both, false, nil, 0},
		{"no decoder at all", videoAuto, videoCaps{}, true, nil, 0},
		{"software only machine", videoAuto, videoCaps{HEVCSoftware: true}, true, []decoderKind{decoderSoftware}, media.CodecHEVC},
		{"GPU only machine in software mode has nothing", videoSoftware, videoCaps{HEVCVAAPI: true}, true, nil, 0},
		{"H.264 only when HEVC is missing", videoAuto, videoCaps{H264VAAPI: true, H264Software: true}, true, []decoderKind{decoderVAAPI, decoderSoftware}, media.CodecH264},
		{"HEVC wins over H.264", videoAuto, videoCaps{HEVCSoftware: true, H264VAAPI: true}, true, []decoderKind{decoderSoftware}, media.CodecHEVC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := planVideo(c.mode, c.caps, c.server)
			if !slices.Equal(plan.Kinds, c.wantKinds) {
				t.Fatalf("kinds %v, want %v", plan.Kinds, c.wantKinds)
			}
			if plan.enabled() != (c.wantKinds != nil) {
				t.Fatalf("enabled %v", plan.enabled())
			}
			if !plan.enabled() {
				if plan.Reason == "" {
					t.Fatal("a plan that is off says why")
				}
				return
			}
			if plan.Codec != c.wantCodec || len(plan.Offer.Codecs) != 1 || plan.Offer.Codecs[0] != c.wantCodec {
				t.Fatalf("codec %v offer %v, want %v", plan.Codec, plan.Offer.Codecs, c.wantCodec)
			}
		})
	}
}

// What is offered to the Mac is standard dynamic range, one band, delivered as it arrives.
func TestPlanVideoOffer(t *testing.T) {
	plan := planVideo(videoAuto, videoCaps{HEVCSoftware: true}, true)
	offer := plan.Offer
	if offer.PixelFormats != media.PixelFormatsSDR420 {
		t.Fatalf("pixel formats %#x, want SDR 4:2:0", uint32(offer.PixelFormats))
	}
	if offer.PixelFormats&media.PixelFormat444TenBitFullRange != 0 {
		t.Fatal("HDR is not offered")
	}
	if offer.TilesPerFrame != 1 {
		t.Fatalf("%d bands, one decoder takes one", offer.TilesPerFrame)
	}
	if !offer.ImmediatePlayout {
		t.Fatalf("offer %+v", offer)
	}
	if _, err := media.NewOffer(offer); err != nil {
		t.Fatalf("the core refuses the offer: %v", err)
	}
}

func TestPlanWithoutKind(t *testing.T) {
	plan := planVideo(videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true}, true)
	next := plan.withoutKind(decoderVAAPI)
	if !slices.Equal(next.Kinds, []decoderKind{decoderSoftware}) || !next.enabled() {
		t.Fatalf("%v", next.Kinds)
	}
	if !slices.Equal(plan.Kinds, []decoderKind{decoderVAAPI, decoderSoftware}) {
		t.Fatal("the plan it started from must not change")
	}
	last := next.withoutKind(decoderSoftware)
	if last.enabled() || last.Reason == "" {
		t.Fatalf("no decoder left: %+v", last)
	}
}

func TestDecoderPixelRate(t *testing.T) {
	if (videoPlan{}).decoderPixelRate(4) != 0 {
		t.Fatal("no plan, no rate")
	}
	gpu := planVideo(videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true}, true)
	if gpu.decoderPixelRate(4) != rateVAAPI {
		t.Fatal("the first decoder sets the rate")
	}
	software := planVideo(videoSoftware, videoCaps{HEVCVAAPI: true, HEVCSoftware: true}, true)
	if software.decoderPixelRate(4) != 4*rateSoftwareThread || software.decoderPixelRate(1) != rateSoftwareThread || software.decoderPixelRate(64) != software.decoderPixelRate(4) {
		t.Fatal("software decoding gets a rate per thread, up to four threads")
	}
	// The Mac keeps 60 Hz for a display while width x height x 3 x 60 fits the rate.
	if !media.Supports60FPS(rateVAAPI, 3840, 2160) {
		t.Fatal("the GPU rate must cover 4K at 60 Hz")
	}
	if !media.Supports60FPS(software.decoderPixelRate(4), 2560, 1600) {
		t.Fatal("four software threads cover 2560x1600 at 60 Hz")
	}
	if media.Supports60FPS(software.decoderPixelRate(1), 2560, 1600) {
		t.Fatal("one software thread asks for 30 Hz at 2560x1600")
	}
}

func TestVideoLabels(t *testing.T) {
	if videoLabel(media.CodecHEVC, decoderVAAPI) != "HEVC (VA-API)" {
		t.Fatal(videoLabel(media.CodecHEVC, decoderVAAPI))
	}
	if videoLabel(media.CodecH264, decoderSoftware) != "H.264 (software)" {
		t.Fatal(videoLabel(media.CodecH264, decoderSoftware))
	}
	if outputDMABuf.String() != "DMA-BUF" || outputCopy.String() != "GPU copy" || outputMemory.String() != "Memory" || outputNone.String() != "none" {
		t.Fatal("output names")
	}
	if formatMillis(1500*time.Microsecond) != "1.5 ms" {
		t.Fatal(formatMillis(1500 * time.Microsecond))
	}
	if videoModeLabel(videoAuto) == videoModeLabel(videoOff) || videoModeLabel(videoSoftware) == "" {
		t.Fatal("each mode has its own label")
	}
}

func TestApplyVideoRows(t *testing.T) {
	info := rfb.ServerInfo{Dialect: rfb.DialectApple}
	rows := statRows(info, 60, 12, 5e6, rfb.EncodingMediaStream, 2560, 1600)

	idle := applyVideoRows(rows, videoStats{})
	if !slices.Equal(idle, rows) {
		t.Fatal("a stream that is not showing changes nothing")
	}

	stats := videoStats{Active: true, Codec: media.CodecHEVC, Kind: decoderVAAPI, Output: outputDMABuf, DecodeAvg: 2300 * time.Microsecond, Dropped: 4}
	got := applyVideoRows(rows, stats)
	if len(got) != len(rows)+3 {
		t.Fatalf("%d rows from %d", len(got), len(rows))
	}
	want := map[string]string{"Codec": "HEVC (VA-API)", "Decode": "2.3 ms", "Output": "DMA-BUF", "Dropped": "4"}
	position := -1
	for index, row := range got {
		if row.Label == "Codec" {
			position = index
		}
		if value, ok := want[row.Label]; ok && row.Value != value {
			t.Errorf("%s: %q, want %q", row.Label, row.Value, value)
		}
	}
	if position != 0 || got[len(got)-3].Label != "Decode" || got[len(got)-2].Label != "Output" || got[len(got)-1].Label != "Dropped" {
		t.Fatalf("the video rows follow the stream rows: %v", got)
	}
	if got[1].Label != "Picture" || got[2].Label != "Delay" {
		t.Fatalf("Picture and Delay stay next to the Codec row: %v", got)
	}
	if rows[position].Value == "HEVC (VA-API)" {
		t.Fatal("the rows it was given must not change")
	}
}

func TestServerHasVideo(t *testing.T) {
	// Message types are numbered from the high bit: 0x1c is bit 3 of byte 3, 0x1d bit 2.
	var both, mediaOnly [16]byte
	both[3] = 0x08 | 0x04
	mediaOnly[3] = 0x08
	cases := []struct {
		name string
		info rfb.ServerInfo
		want bool
	}{
		{"both messages", rfb.ServerInfo{Extended: true, Commands: both}, true},
		{"media stream without display configuration", rfb.ServerInfo{Extended: true, Commands: mediaOnly}, false},
		{"not an extended server", rfb.ServerInfo{Commands: both}, false},
		{"nothing", rfb.ServerInfo{Extended: true}, false},
	}
	for _, c := range cases {
		if got := serverHasVideo(&c.info); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestVideoHint(t *testing.T) {
	cases := []struct {
		name string
		caps videoCaps
		want string
	}{
		{"nothing", videoCaps{}, "org.freedesktop.Platform.codecs-extra"},
		{"gpu", videoCaps{HEVCVAAPI: true, HEVCSoftware: true, Driver: "Intel iHD driver"}, "GPU: Intel iHD driver"},
		{"driver name with an empty detail", videoCaps{HEVCVAAPI: true, Driver: "Intel iHD driver for Intel(R) Gen Graphics - 25.2.3 ()"}, "GPU: Intel iHD driver for Intel(R) Gen Graphics - 25.2.3"},
		{"gpu without a driver name", videoCaps{H264VAAPI: true}, "GPU"},
		{"cpu with a reason", videoCaps{HEVCSoftware: true, Error: "VA-API: No such file"}, "no VA-API decoder here (VA-API: No such file)"},
		{"cpu", videoCaps{HEVCSoftware: true}, "Software"},
	}
	for _, c := range cases {
		got := videoHint(c.caps)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q lacks %q", c.name, got, c.want)
		}
		if strings.Contains(got, "()") {
			t.Errorf("%s: %q has empty parentheses", c.name, got)
		}
	}
}

func TestVideoPaysOff(t *testing.T) {
	big, small := 5120*1440, 1920*1080
	cases := []struct {
		name        string
		mode        videoMode
		accelerated bool
		pixels      int
		want        bool
	}{
		{"GPU, big", videoAuto, true, big, true},
		{"CPU renderer, small", videoAuto, false, small, true},
		{"CPU renderer, big", videoAuto, false, big, false},
		{"CPU renderer, big, always", videoAlways, false, big, true},
		{"CPU renderer, big, software decoding asked for", videoSoftware, false, big, true},
	}
	for _, c := range cases {
		got, reason := videoPaysOff(c.mode, c.accelerated, c.pixels)
		if got != c.want || (!got && reason == "") {
			t.Errorf("%s: %v %q", c.name, got, reason)
		}
	}
	if parseVideoMode("ALWAYS") != videoAlways || planVideo(videoAlways, videoCaps{HEVCSoftware: true}, true).Kinds[0] != decoderSoftware {
		t.Fatal("always plans like auto")
	}
}

func TestPlanWithHDR(t *testing.T) {
	both := videoCaps{HEVCVAAPI: true, HEVCSoftware: true, HEVC444x10VAAPI: true, HEVC444x10Software: true}
	cases := []struct {
		name      string
		mode      videoMode
		caps      videoCaps
		wantHDR   bool
		wantKinds []decoderKind
	}{
		// Only the GPU decodes HDR: the CPU's pixel rate is measured for 4:2:0 8-bit.
		{"both decoders, the GPU alone takes HDR", videoAuto, both, true, []decoderKind{decoderVAAPI}},
		{"software mode never asks the GPU, so there is no HDR", videoSoftware, both, false, nil},
		{"only the GPU takes 4:4:4", videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true, HEVC444x10VAAPI: true}, true, []decoderKind{decoderVAAPI}},
		{"only software takes 4:4:4: no HDR offered", videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true, HEVC444x10Software: true}, false, nil},
		{"no GPU at all, software takes 4:4:4: no HDR offered", videoAuto, videoCaps{HEVCSoftware: true, HEVC444x10Software: true}, false, nil},
		{"no 4:4:4 decoder keeps the plan standard range", videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true}, false, nil},
		{"GPU-only 4:4:4 in software mode has nothing", videoSoftware, videoCaps{HEVCSoftware: true, HEVC444x10VAAPI: true}, false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := planVideo(c.mode, c.caps, true).withHDR(c.mode, c.caps)
			if plan.HDR != c.wantHDR {
				t.Fatalf("HDR %v, want %v", plan.HDR, c.wantHDR)
			}
			if !slices.Equal(plan.HDRKinds, c.wantKinds) {
				t.Fatalf("HDR decoders %v, want %v", plan.HDRKinds, c.wantKinds)
			}
			offered := plan.Offer.PixelFormats&media.PixelFormat444TenBitFullRange != 0
			if offered != c.wantHDR {
				t.Fatalf("the offer names 4:4:4 10-bit: %v, want %v", offered, c.wantHDR)
			}
			if plan.Offer.PixelFormats&media.PixelFormatsSDR420 != media.PixelFormatsSDR420 {
				t.Fatal("the standard range formats stay in the offer")
			}
		})
	}
}

// The pixel rate the Mac is told is the GPU's whenever the session has HDR, and a machine whose
// only HDR-capable decoder is software never gets the offer, so it is never told a CPU rate for it.
func TestHDRPlanRateIsTheGPUs(t *testing.T) {
	caps := videoCaps{HEVCVAAPI: true, HEVCSoftware: true, HEVC444x10VAAPI: true, HEVC444x10Software: true}
	plan := planVideo(videoAuto, caps, true).withHDR(videoAuto, caps)
	if got := plan.decoderPixelRate(4); got != rateVAAPI {
		t.Fatalf("rate %d, want the GPU's %d", got, rateVAAPI)
	}
	soft := videoCaps{HEVCSoftware: true, HEVC444x10Software: true}
	if plan := planVideo(videoAuto, soft, true).withHDR(videoAuto, soft); plan.HDR {
		t.Fatal("software decoding alone is offered HDR")
	}
}

func TestPlanWithHDRNeedsHEVC(t *testing.T) {
	caps := videoCaps{H264Software: true, HEVC444x10Software: true}
	plan := planVideo(videoAuto, caps, true).withHDR(videoAuto, caps)
	if plan.Codec != media.CodecH264 || plan.HDR || plan.Offer.PixelFormats&media.PixelFormat444TenBitFullRange != 0 {
		t.Fatalf("an H.264 plan cannot ask for HDR: %+v", plan)
	}
	off := planVideo(videoOff, caps, true).withHDR(videoOff, caps)
	if off.enabled() || off.HDR {
		t.Fatalf("no plan stays no plan: %+v", off)
	}
}

func TestPlanKindsFor(t *testing.T) {
	caps := videoCaps{HEVCVAAPI: true, HEVCSoftware: true, HEVC444x10VAAPI: true}
	plan := planVideo(videoAuto, caps, true).withHDR(videoAuto, caps)
	sdr := media.Color{BitDepth: 8, ChromaFormat: 1}
	hdr := media.Color{BitDepth: 10, ChromaFormat: 3, Transfer: media.TransferPQ}
	if got := plan.kindsFor(sdr); !slices.Equal(got, []decoderKind{decoderVAAPI, decoderSoftware}) {
		t.Fatalf("SDR video: %v", got)
	}
	if got := plan.kindsFor(hdr); !slices.Equal(got, []decoderKind{decoderVAAPI}) {
		t.Fatalf("HDR video goes to the GPU alone: %v", got)
	}
	// A decoder that failed is gone for both kinds of stream.
	failed := plan.withoutKind(decoderVAAPI)
	if got := failed.kindsFor(hdr); len(got) != 0 {
		t.Fatalf("no decoder left for HDR: %v", got)
	}
	if got := failed.kindsFor(sdr); !slices.Equal(got, []decoderKind{decoderSoftware}) {
		t.Fatalf("SDR video keeps the CPU: %v", got)
	}
}

func TestVideoRowsNameHDR(t *testing.T) {
	rows := []statRow{{"Codec", "x"}}
	hdr := videoStats{Active: true, Codec: media.CodecHEVC, Kind: decoderVAAPI,
		Color: media.Color{BitDepth: 10, ChromaFormat: 3, Transfer: media.TransferHLG}}
	got := applyVideoRows(rows, hdr)
	if got[0].Value != "HEVC 4:4:4 10-bit HDR (HLG, VA-API)" {
		t.Fatalf("%q", got[0].Value)
	}
	sdr := videoStats{Active: true, Codec: media.CodecHEVC, Kind: decoderSoftware, Color: media.Color{BitDepth: 8, ChromaFormat: 1}}
	if got := applyVideoRows(rows, sdr); got[0].Value != "HEVC (software)" {
		t.Fatalf("%q", got[0].Value)
	}
}

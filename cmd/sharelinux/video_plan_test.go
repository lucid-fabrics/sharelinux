package main

import (
	"slices"
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
	if !offer.ImmediatePlayout || !offer.FrameRate60 {
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
	if (videoPlan{}).decoderPixelRate() != 0 {
		t.Fatal("no plan, no rate")
	}
	gpu := planVideo(videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true}, true)
	if gpu.decoderPixelRate() != rateVAAPI {
		t.Fatal("the first decoder sets the rate")
	}
	software := planVideo(videoSoftware, videoCaps{HEVCVAAPI: true, HEVCSoftware: true}, true)
	if software.decoderPixelRate() != rateSoftware {
		t.Fatal("software decoding gets the software rate")
	}
	// The Mac keeps 60 Hz for a display while width x height x 3 x 60 fits the rate.
	if !media.Supports60FPS(rateVAAPI, 3840, 2160) {
		t.Fatal("the GPU rate must cover 4K at 60 Hz")
	}
	if media.Supports60FPS(rateSoftware, 2560, 1600) {
		t.Fatal("the software rate asks for 30 Hz at 2560x1600")
	}
	if !media.Supports60FPS(rateSoftware, 1920, 1080) {
		t.Fatal("the software rate must cover 1080p at 60 Hz")
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
	if position < 0 || got[position+1].Label != "Decode" || got[position+2].Label != "Output" || got[position+3].Label != "Dropped" {
		t.Fatalf("the video rows follow the Codec row: %v", got)
	}
	if got[len(got)-1].Label != "Resolution" {
		t.Fatal("Resolution stays last")
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

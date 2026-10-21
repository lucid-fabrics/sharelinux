//go:build linux

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/cmd/sharelinux/internal/mockmedia"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// hdrColor is what the SPS of the Mac's HDR stream says: BT.2020 primaries and matrix, full range.
func hdrColor(transfer int) media.Color {
	return media.Color{BitDepth: 10, ChromaFormat: 3, Primaries: 9, Transfer: transfer, Matrix: 9, FullRange: true}
}

func hdrClip(t *testing.T, transfer int) *mockmedia.Clip {
	t.Helper()
	open := mockmedia.BuiltinPQ
	if transfer == media.TransferHLG {
		open = mockmedia.BuiltinHLG
	}
	clip, err := open()
	if err != nil {
		t.Fatal(err)
	}
	return clip
}

func hdrFormat(clip *mockmedia.Clip, transfer int) media.VideoFormat {
	format := clipFormat(clip)
	format.Color = hdrColor(transfer)
	return format
}

// decodeHDR decodes n pictures of an HDR clip.
func decodeHDR(t *testing.T, kind decoderKind, clip *mockmedia.Clip, transfer, n int) []*videoFrame {
	t.Helper()
	d, err := openDecoder(decoderOptions{Codec: media.CodecHEVC, Kind: kind, Threads: 1}, hdrFormat(clip, transfer))
	if err != nil {
		t.Skipf("no %v decoder for HEVC 4:4:4 10-bit: %v", kind, err)
	}
	t.Cleanup(d.close)
	var frames []*videoFrame
	for index := range n {
		unit := clipUnit(clip, index)
		count, result := d.decode(unit.Data, unit.Sync, 0)
		if result != decodeOK {
			t.Fatalf("picture %d: %v", index, result)
		}
		for ; count > 0; count-- {
			frames = append(frames, d.next())
		}
	}
	return frames
}

func TestProbeFindsHDRDecoders(t *testing.T) {
	caps := probeVideo("")
	if !caps.HEVCSoftware {
		t.Skip("this libavcodec has no HEVC decoder")
	}
	if !caps.HEVC444x10Software {
		t.Fatal("libavcodec decodes HEVC 4:4:4 10-bit in software but the probe says it does not")
	}
	if !caps.hdrDecoder() {
		t.Fatal("a software decoder is a decoder for HDR")
	}
	t.Logf("HEVC 4:4:4 10-bit: VA-API %v, software %v (driver %q)", caps.HEVC444x10VAAPI, caps.HEVC444x10Software, caps.Driver)
}

func TestProbeHDRWithoutGPU(t *testing.T) {
	caps := probeVideo("/dev/dri/not-a-device")
	if caps.HEVC444x10VAAPI {
		t.Fatalf("a missing device cannot decode: %+v", caps)
	}
	if caps.HEVCSoftware && !caps.HEVC444x10Software {
		t.Fatalf("the software decoder does not depend on the GPU: %+v", caps)
	}
}

func TestSoftwareDecodesHDRClips(t *testing.T) {
	for _, transfer := range []int{media.TransferPQ, media.TransferHLG} {
		clip := hdrClip(t, transfer)
		frames := decodeHDR(t, decoderSoftware, clip, transfer, len(clip.Units))
		if len(frames) != len(clip.Units) {
			t.Fatalf("transfer %d: %d frames for %d pictures", transfer, len(frames), len(clip.Units))
		}
		for _, frame := range frames {
			if frame.Width != 320 || frame.Height != 192 || frame.Hardware {
				t.Fatalf("transfer %d: %dx%d hardware %v", transfer, frame.Width, frame.Height, frame.Hardware)
			}
			frame.free()
		}
	}
}

// The left half of the clip is neutral gray at luma 500 of 1023, the right half has chroma Cb 300 and Cr 800.
// The texture holds the picture in the stream's own transfer function, so the byte of the gray is
// 500/1023 of 255, and the colored half shows the BT.2020 matrix: BT.709 would put red at 238.
func TestHDRTextureColors(t *testing.T) {
	requireDisplay(t)
	for _, c := range []struct {
		name     string
		transfer int
		swscale  bool
	}{
		{"PQ planes", media.TransferPQ, false},
		{"HLG planes", media.TransferHLG, false},
		{"PQ swscale", media.TransferPQ, true},
		{"HLG swscale", media.TransferHLG, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			forceSwscaleForTest(c.swscale)
			defer forceSwscaleForTest(false)
			clip := hdrClip(t, c.transfer)
			frames := decodeHDR(t, decoderSoftware, clip, c.transfer, 1)
			defer frames[0].free()
			expect := hdrColor(c.transfer)
			gray, matches, ok := frames[0].textureForTest(0, 0, 40, 40, expect)
			if !ok {
				t.Fatal("no texture")
			}
			if !matches {
				t.Fatal("the texture is not in the color state of the stream")
			}
			for index, value := range gray[:3] {
				if value < 123 || value > 127 {
					t.Fatalf("gray channel %d is %d, want 125 (%v)", index, value, gray)
				}
			}
			color, _, ok := frames[0].textureForTest(0, 0, 240, 40, expect)
			if !ok {
				t.Fatal("no texture")
			}
			b, g, r := int(color[0]), int(color[1]), int(color[2])
			if r < 228 || r > 233 || g < 90 || g > 95 || b < 23 || b > 28 {
				t.Fatalf("BT.2020 chroma gives r %d g %d b %d, want about 230 92 25", r, g, b)
			}
		})
	}
}

// A GPU decoder that takes 4:4:4 10-bit gives the same picture, read back or imported.
func TestVAAPIDecodesHDR(t *testing.T) {
	caps := probeVideo("")
	if !caps.HEVC444x10VAAPI {
		t.Skipf("no VA-API HEVC 4:4:4 10-bit decoder (%q %s)", caps.Driver, caps.Error)
	}
	requireDisplay(t)
	clip := hdrClip(t, media.TransferPQ)
	view := shownScreen(t, clip)
	frames := decodeHDR(t, decoderVAAPI, clip, media.TransferPQ, 4)
	if !frames[0].Hardware {
		t.Fatal("the VA-API decoder gave a picture in memory")
	}
	view.setVideo(true)
	pump(func() bool { return false }, 300*time.Millisecond)
	renderer := view.chooseDMABuf()
	var last outputKind
	for _, frame := range frames {
		last = view.present(frame)
	}
	t.Logf("renderer %s accelerated %v, HDR picture went as %v", renderer.Name, renderer.Accelerated, last)
	if last != outputDMABuf && last != outputCopy {
		t.Fatalf("a GPU picture reached the screen as %v", last)
	}
	// What was read back or imported has the colors of the clip.
	again := decodeHDR(t, decoderVAAPI, clip, media.TransferPQ, 1)
	pixel, matches, ok := again[0].textureForTest(0, 0, 40, 40, hdrColor(media.TransferPQ))
	if !ok || !matches {
		t.Fatalf("texture ok %v color state matches %v", ok, matches)
	}
	if pixel[1] < 123 || pixel[1] > 127 {
		t.Fatalf("gray is %v, want 125", pixel)
	}
}

// The stream of the Mac when HDR was asked for: offered, negotiated, decoded and shown as such, and the stats
// card says so. A viewer that did not ask for HDR offers no 4:4:4 and is never sent it.
func TestMockHDRStreamEndToEnd(t *testing.T) {
	for _, c := range []struct {
		name     string
		transfer int
		label    string
	}{
		{"PQ", media.TransferPQ, "HEVC 4:4:4 10-bit HDR (PQ, software)"},
		{"HLG", media.TransferHLG, "HEVC 4:4:4 10-bit HDR (HLG, software)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := videoLive()
			clip := hdrClip(t, c.transfer)
			client, mac, closeAll := openMockMac(t, clip)
			defer closeAll()
			caps := videoCaps{HEVCSoftware: true, HEVC444x10Software: true}
			plan := planVideo(videoAuto, caps, true).withHDR(videoAuto, caps)
			if !plan.HDR {
				t.Fatal("the plan has no HDR")
			}
			var log bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&log, nil))
			presenter := &countingPresenter{}
			stream := newVideoStream(logger, plan, "", 1, presenter, clientKeyframe(client))
			if err := client.StartMediaStream(plan.Offer, stream); err != nil {
				stream.close()
				t.Fatal(err)
			}
			waitFor(t, "HDR pictures", 20*time.Second, func() bool { return presenter.count() >= 10 })
			if formats := media.PixelFormats(mac.pixelFormats.Load()); formats&media.PixelFormat444TenBitFullRange == 0 {
				t.Fatalf("the offer did not name 4:4:4 10-bit: %v", formats)
			}
			stats := stream.stats()
			if !is444TenBit(stats.Color) || stats.Color.Transfer != c.transfer || stats.Kind != decoderSoftware {
				t.Fatalf("stats %+v", stats)
			}
			stats.Active = true
			rows := applyVideoRows([]statRow{{"Codec", "x"}}, stats)
			if rows[0].Value != c.label {
				t.Fatalf("codec row %q, want %q", rows[0].Value, c.label)
			}
			line := log.String()
			for _, want := range []string{"bit-depth=10", "chroma=3", "transfer=" + map[int]string{media.TransferPQ: "16", media.TransferHLG: "18"}[c.transfer], "hdr=true"} {
				if !strings.Contains(line, want) {
					t.Fatalf("the format line lacks %q:\n%s", want, line)
				}
			}
			stream.close()
			_ = client.StopMediaStream()
			if live := videoLive(); live != before {
				t.Fatalf("left behind: %+v, before %+v", live, before)
			}
		})
	}
}

// Without HDR in the plan the offer names no 4:4:4 and a stream that comes anyway is refused.
func TestMockSDRStreamOffersNoHDR(t *testing.T) {
	clip := builtinClip(t)
	client, mac, closeAll := openMockMac(t, clip)
	defer closeAll()
	plan := planVideo(videoAuto, videoCaps{HEVCSoftware: true, HEVC444x10Software: true}, true)
	presenter := &countingPresenter{}
	stream := newVideoStream(quiet, plan, "", 1, presenter, clientKeyframe(client))
	defer stream.close()
	if err := client.StartMediaStream(plan.Offer, stream); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pictures", 20*time.Second, func() bool { return presenter.count() >= 3 })
	if formats := media.PixelFormats(mac.pixelFormats.Load()); formats&media.PixelFormat444TenBitFullRange != 0 {
		t.Fatalf("an SDR plan offered 4:4:4 10-bit: %v", formats)
	}
}

func TestSDRPlanRefusesHDRStream(t *testing.T) {
	stream := newVideoStream(quiet, planVideo(videoAuto, videoCaps{HEVCSoftware: true}, true), "", 1, &countingPresenter{}, func() {})
	defer stream.close()
	format := hdrFormat(hdrClip(t, media.TransferPQ), media.TransferPQ)
	stream.VideoFormat(format)
	select {
	case event := <-stream.events:
		if event.Kind != videoFailed || !strings.Contains(event.Text, "10 bit") {
			t.Fatalf("%+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a stream the plan did not ask for was taken")
	}
}

//go:build linux

package main

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
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

const hdrChildEnv = "SHARELINUX_TEST_CHILD"

// tail is the last n bytes of output.
func tail(output []byte, n int) []byte {
	if len(output) > n {
		return output[len(output)-n:]
	}
	return output
}

func hdrFormat(clip *mockmedia.Clip, transfer int) media.VideoFormat {
	return hdrFormatOf(clip, hdrColor(transfer))
}

func hdrFormatOf(clip *mockmedia.Clip, color media.Color) media.VideoFormat {
	format := clipFormat(clip)
	format.Color = color
	return format
}

// p3Color is what the SPS of the Mac's real HDR stream says: Display P3 primaries, the BT.709 matrix, full
// range, PQ.
func p3Color() media.Color {
	return media.Color{BitDepth: 10, ChromaFormat: 3, Primaries: 12, Transfer: media.TransferPQ, Matrix: 1, FullRange: true}
}

// decodeHDR decodes n pictures of an HDR clip.
func decodeHDR(t *testing.T, kind decoderKind, clip *mockmedia.Clip, transfer, n int) []*videoFrame {
	t.Helper()
	return decodeHDRColor(t, kind, clip, hdrColor(transfer), n)
}

func decodeHDRColor(t *testing.T, kind decoderKind, clip *mockmedia.Clip, color media.Color, n int) []*videoFrame {
	t.Helper()
	d, err := openDecoder(decoderOptions{Codec: media.CodecHEVC, Kind: kind, Threads: 1}, hdrFormatOf(clip, color))
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

// softwareHDRPlan is a plan that decodes HDR on the CPU. A real session never has one (withHDR leaves
// the CPU out), but the stream path is tried on it where there is no GPU decoder.
func softwareHDRPlan() videoPlan {
	plan := planVideo(videoAuto, videoCaps{HEVCSoftware: true}, true)
	plan.HDR = true
	plan.HDRKinds = []decoderKind{decoderSoftware}
	plan.Offer.PixelFormats |= media.PixelFormat444TenBitFullRange
	return plan
}

func TestProbeFindsHDRDecoders(t *testing.T) {
	caps := probeVideo("")
	if !caps.HEVCSoftware {
		t.Skip("this libavcodec has no HEVC decoder")
	}
	if !caps.HEVC444x10Software {
		t.Fatal("libavcodec decodes HEVC 4:4:4 10-bit in software but the probe says it does not")
	}
	if caps.hdrDecoder() != caps.HEVC444x10VAAPI {
		t.Fatal("only the GPU decoder is a decoder for HDR")
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

// The clip the Mac really sends: Display P3, matrix 1 (BT.709), full range, PQ. The right half of the
// picture is luma 500 with Cb 300 and Cr 800, which the BT.709 matrix turns into about r 238 g 101 b 27
// (BT.2020 would give 230 92 25), so the right matrix reaches the texture.
func TestP3PQTextureColors(t *testing.T) {
	requireDisplay(t)
	for _, swscale := range []bool{false, true} {
		name := map[bool]string{false: "planes", true: "swscale"}[swscale]
		t.Run(name, func(t *testing.T) {
			forceSwscaleForTest(swscale)
			defer forceSwscaleForTest(false)
			clip, err := mockmedia.BuiltinP3PQ()
			if err != nil {
				t.Fatal(err)
			}
			frames := decodeHDRColor(t, decoderSoftware, clip, p3Color(), 1)
			defer frames[0].free()
			gray, matches, ok := frames[0].textureForTest(0, 0, 40, 40, p3Color())
			if !ok || !matches {
				t.Fatalf("texture ok %v, in the color state of the stream %v", ok, matches)
			}
			for index, value := range gray[:3] {
				if value < 123 || value > 127 {
					t.Fatalf("gray channel %d is %d, want 125 (%v)", index, value, gray)
				}
			}
			color, _, ok := frames[0].textureForTest(0, 0, 240, 40, p3Color())
			if !ok {
				t.Fatal("no texture")
			}
			b, g, r := int(color[0]), int(color[1]), int(color[2])
			if r < 234 || r > 242 || g < 97 || g > 105 || b < 23 || b > 31 {
				t.Fatalf("BT.709 chroma gives r %d g %d b %d, want about 238 101 27", r, g, b)
			}
		})
	}
}

// The stream of the Mac, end to end over the mock: the stats card shows what its parameter sets say.
func TestMockP3PQStreamEndToEnd(t *testing.T) {
	clip, err := mockmedia.BuiltinP3PQ()
	if err != nil {
		t.Fatal(err)
	}
	client, _, closeAll := openMockMac(t, clip)
	defer closeAll()
	plan := softwareHDRPlan()
	presenter := &countingPresenter{}
	stream := newVideoStream(quiet, plan, "", 1, presenter, clientKeyframe(client))
	if err := client.StartMediaStream(plan.Offer, stream); err != nil {
		stream.close()
		t.Fatal(err)
	}
	waitFor(t, "P3 pictures", 20*time.Second, func() bool { return presenter.count() >= 10 })
	if got := stream.stats().Color; got != p3Color() {
		t.Fatalf("stats color %+v, want %+v", got, p3Color())
	}
	stream.close()
	_ = client.StopMediaStream()
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
			plan := softwareHDRPlan()
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

// Where GDK takes no 4:4:4 surface, the GPU's video processor makes a P010 one that it does take, with
// the same colors: the left half of the clip is gray at luma 500, the right half the BT.2020 chroma.
func TestVAAPIHDRThroughTheVideoProcessor(t *testing.T) {
	// Downloading a DMA-BUF texture makes GDK's one GL context current on this thread, and a context
	// that an earlier test made current on another thread aborts the process ("Couldn't find current
	// GLX or EGL context"). The test runs in a process of its own.
	if os.Getenv(hdrChildEnv) == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestVAAPIHDRThroughTheVideoProcessor$", "-test.v")
		command.Env = append(os.Environ(), hdrChildEnv+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, tail(output, 3000))
		}
		if bytes.Contains(output, []byte("--- SKIP")) {
			t.Skip("skipped in its own process: " + string(tail(output, 300)))
		}
		return
	}
	caps := probeVideo("")
	if !caps.HEVC444x10VAAPI {
		t.Skipf("no VA-API HEVC 4:4:4 10-bit decoder (%q %s)", caps.Driver, caps.Error)
	}
	requireDisplay(t)
	defer hdrGPUForTest(-1)
	clip := hdrClip(t, media.TransferPQ)
	// A DMA-BUF of the GPU can be drawn only by a renderer on the GPU.
	view := shownScreen(t, clip)
	if renderer := view.chooseDMABuf(); !renderer.Accelerated {
		t.Skipf("the renderer %s does not draw on the GPU", renderer.Name)
	}
	before := videoLive()
	frames := decodeHDR(t, decoderVAAPI, clip, media.TransferPQ, 3)
	frame := frames[len(frames)-1]
	expect := hdrColor(media.TransferPQ)

	// A small picture is not worth the chroma: it is read back at full resolution.
	hdrGPUForTest(-1)
	_, output, _, ok := frame.gpuTextureForTest(40, 40, expect)
	if !ok || output != outputCopy {
		t.Fatalf("a small picture goes through memory, not %v (ok %v)", output, ok)
	}
	hdrGPUForTest(0)
	if _, output, _, _ = frame.gpuTextureForTest(40, 40, expect); output != outputCopy {
		t.Fatalf("with the processor off the picture went as %v", output)
	}

	hdrGPUForTest(1)
	gray, output, matches, ok := frame.gpuTextureForTest(40, 40, expect)
	if !ok {
		t.Fatal("no texture")
	}
	if output != outputDMABuf {
		t.Skipf("this display takes no P010 DMA-BUF of the GPU (went as %v)", output)
	}
	if !matches {
		t.Fatal("the texture is not in the color state of the stream")
	}
	for index, value := range gray[:3] {
		if value < 122 || value > 128 {
			t.Fatalf("gray channel %d is %d, want 125 (%v)", index, value, gray)
		}
	}
	color, _, _, _ := frame.gpuTextureForTest(240, 40, expect)
	b, g, r := int(color[0]), int(color[1]), int(color[2])
	if r < 227 || r > 234 || g < 88 || g > 96 || b < 21 || b > 29 {
		t.Fatalf("chroma through the processor gives r %d g %d b %d, want about 230 92 25", r, g, b)
	}
	for _, frame := range frames {
		frame.free()
	}
	// The textures went with the test's pictures: no descriptor or frame is left.
	view.clear()
	if !pump(func() bool {
		live := videoLive()
		return live.FDs == before.FDs && live.Textures == before.Textures && live.Frames == before.Frames
	}, 5*time.Second) {
		t.Fatalf("left behind: %+v, before %+v", videoLive(), before)
	}
}

// HDR switched on in the preferences must not change how GTK starts where HDR cannot be shown: a standard
// range session would run on the GL renderer for nothing, and the variables would reach whatever the app
// starts. Where the machine does have an HDR screen the variables are added, and taken out once GTK has them.
func TestPrepareHDRLeavesTheEnvironmentWhereHDRCannotBeShown(t *testing.T) {
	if !waylandSession(os.Getenv) {
		t.Setenv("GDK_BACKEND", "wayland")
	}
	t.Setenv("GDK_DEBUG", "")
	t.Setenv("GSK_RENDERER", "")
	os.Unsetenv("GDK_DEBUG")
	os.Unsetenv("GSK_RENDERER")
	caps := probeVideo("")
	a := hdrApp(caps, hdrEnv{})
	a.settings.HDR = true
	screens, colorManager := probeScreens()
	shows := hdrStartup(true, caps, videoAuto, true, colorManager, screens)
	prepareHDR(a)
	defer releaseGTKEnv(a)
	_, debug := os.LookupEnv("GDK_DEBUG")
	_, renderer := os.LookupEnv("GSK_RENDERER")
	if shows {
		if !debug || !renderer {
			t.Fatal("an HDR screen was found but GTK was not set up for it")
		}
		releaseGTKEnv(a)
		if _, found := os.LookupEnv("GSK_RENDERER"); found {
			t.Fatal("GSK_RENDERER stayed after GTK read it")
		}
		return
	}
	if debug || renderer || a.gtkEnv != nil {
		t.Fatalf("GTK was set up for HDR on a machine that cannot show it: GDK_DEBUG %v GSK_RENDERER %v", debug, renderer)
	}
	t.Logf("no HDR screen here (color manager %v, screens %+v), the environment is as it was", colorManager, screens)
}

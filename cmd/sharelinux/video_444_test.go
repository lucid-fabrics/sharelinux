//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/cmd/sharelinux/internal/mockmedia"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

func clip444(t *testing.T) *mockmedia.Clip {
	t.Helper()
	clip, err := mockmedia.Builtin444()
	if err != nil {
		t.Fatal(err)
	}
	return clip
}

func format444(clip *mockmedia.Clip) media.VideoFormat {
	format := clipFormat(clip)
	format.Color = color444()
	return format
}

func decode444(t *testing.T, kind decoderKind, clip *mockmedia.Clip, n int) []*videoFrame {
	t.Helper()
	d, err := openDecoder(decoderOptions{Codec: media.CodecHEVC, Kind: kind, Threads: 1}, format444(clip))
	if err != nil {
		t.Skipf("no %v decoder for HEVC 4:4:4 8-bit: %v", kind, err)
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

// pixelNear says whether a pixel (b, g, r) is within slack of the wanted r, g, b.
func pixelNear(pixel [4]byte, r, g, b, slack int) bool {
	abs := func(v int) int {
		if v < 0 {
			return -v
		}
		return v
	}
	return abs(int(pixel[2])-r) <= slack && abs(int(pixel[1])-g) <= slack && abs(int(pixel[0])-b) <= slack
}

// A missing GPU cannot decode 4:4:4, and the plan then keeps to 4:2:0; the software probe does not depend on the GPU.
func TestProbe444WithoutGPU(t *testing.T) {
	caps := probeVideo("/dev/dri/not-a-device")
	if caps.HEVC444x8VAAPI {
		t.Fatalf("a missing device cannot decode: %+v", caps)
	}
	if caps.HEVCSoftware && !caps.HEVC444x8Software {
		t.Fatalf("the software decoder does not depend on the GPU: %+v", caps)
	}
	if plan := planVideo(videoAuto, caps, true).with444(videoAuto, caps); plan.Chroma444 {
		t.Fatalf("a machine without a GPU decoder offered 4:4:4: %+v", plan)
	}
}

// The software decoder gives the full resolution picture and the texture shows it in the stream's colors: the left
// half is gray (128), the right half red, with the edge between them one pixel wide.
func TestSoftwareDecodes444Clip(t *testing.T) {
	clip := clip444(t)
	frames := decode444(t, decoderSoftware, clip, len(clip.Units))
	if len(frames) != len(clip.Units) {
		t.Fatalf("%d frames for %d pictures", len(frames), len(clip.Units))
	}
	for _, frame := range frames {
		if frame.Width != 320 || frame.Height != 192 || frame.Hardware {
			t.Fatalf("%dx%d hardware %v", frame.Width, frame.Height, frame.Hardware)
		}
	}
	expect := color444()
	gray, matches, ok := frames[0].textureForTest(0, 0, 40, 40, expect)
	if !ok || !matches || !pixelNear(gray, 128, 128, 128, 3) {
		t.Fatalf("gray half %v matches %v ok %v", gray, matches, ok)
	}
	red, _, ok := frames[0].textureForTest(0, 0, 240, 40, expect)
	if !ok || !pixelNear(red, 255, 0, 0, 6) {
		t.Fatalf("red half %v ok %v", red, ok)
	}
	// Full chroma resolution: the pixels on either side of the edge at x 158|159 keep their own colors.
	left, _, _ := frames[0].textureForTest(0, 0, 158, 40, expect)
	right, _, _ := frames[0].textureForTest(0, 0, 159, 40, expect)
	if !pixelNear(left, 128, 128, 128, 2) || !pixelNear(right, 255, 0, 0, 3) {
		t.Fatalf("the edge is blurred: left %v right %v", left, right)
	}
	for _, frame := range frames {
		frame.free()
	}
}

func TestProbeFinds444Decoders(t *testing.T) {
	caps := probeVideo("")
	t.Logf("444 8-bit: vaapi %v software %v (driver %q)", caps.HEVC444x8VAAPI, caps.HEVC444x8Software, caps.Driver)
	if !caps.HEVC444x8Software {
		t.Skip("libavcodec cannot decode the 4:4:4 probe in software")
	}
}

// A GPU decoder that takes 4:4:4 8-bit decodes the clip to surfaces, and the GPU turns them into RGB for GDK with
// every chroma sample kept.
func TestVAAPIShows444OnTheGPU(t *testing.T) {
	// See TestVAAPIHDRThroughTheVideoProcessor: downloading a DMA-BUF texture needs a process of its own.
	if os.Getenv(hdrChildEnv) == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestVAAPIShows444OnTheGPU$", "-test.v")
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
	if !caps.HEVC444x8VAAPI {
		t.Skipf("no VA-API HEVC 4:4:4 8-bit decoder (%q %s)", caps.Driver, caps.Error)
	}
	requireDisplay(t)
	clip := clip444(t)
	view := shownScreen(t, clip)
	if renderer := view.chooseDMABuf(); !renderer.Accelerated {
		t.Skipf("the renderer %s does not draw on the GPU", renderer.Name)
	}
	before := videoLive()
	// The converted surfaces are kept for reuse and go with the decoder: a cleanup that runs after the
	// decoder's own says none is left.
	t.Cleanup(func() {
		if n := videoPoolForTest(); n != 0 {
			t.Errorf("%d output surfaces left after the decoder closed", n)
		}
	})
	frames := decode444(t, decoderVAAPI, clip, 3)
	frame := frames[len(frames)-1]
	if !frame.Hardware {
		t.Fatal("the VA-API decoder gave a picture in memory")
	}
	expect := color444()
	gray, output, matches, ok := frame.gpuTextureForTest(40, 40, expect)
	if !ok {
		t.Fatal("no texture")
	}
	t.Logf("4:4:4 went to the screen as %v, gray %v", output, gray)
	if output != outputDMABuf {
		t.Skipf("this display takes no RGB DMA-BUF of the GPU (went as %v)", output)
	}
	if !matches {
		t.Fatal("the texture is not in the color state of the stream")
	}
	if !pixelNear(gray, 128, 128, 128, 4) {
		t.Fatalf("gray half is %v", gray)
	}
	red, _, _, _ := frame.gpuTextureForTest(240, 40, expect)
	if !pixelNear(red, 255, 0, 0, 8) {
		t.Fatalf("red half is %v", red)
	}
	left, _, _, _ := frame.gpuTextureForTest(158, 40, expect)
	right, _, _, _ := frame.gpuTextureForTest(159, 40, expect)
	if !pixelNear(left, 128, 128, 128, 4) || !pixelNear(right, 255, 0, 0, 8) {
		t.Fatalf("the chroma edge is blurred: left %v right %v", left, right)
	}
	// A picture after picture reuses the same few surfaces instead of making one for each.
	for range 30 {
		if _, output, _, _ := frame.gpuTextureForTest(40, 40, expect); output != outputDMABuf {
			t.Fatalf("a picture went as %v", output)
		}
	}
	if n := videoPoolForTest(); n < 1 || n > 3 {
		t.Fatalf("%d output surfaces in the pool after 30 pictures, want 1 to 3", n)
	}
	for _, frame := range frames {
		frame.free()
	}
	view.clear()
	if !pump(func() bool {
		live := videoLive()
		return live.FDs == before.FDs && live.Textures == before.Textures && live.Frames == before.Frames
	}, 5*time.Second) {
		t.Fatalf("left behind: %+v, before %+v", videoLive(), before)
	}
}

// plan444Software is a plan that decodes 4:4:4 8-bit in software. A real session never has one (with444 leaves
// the CPU out), but the stream path is tried on it where there is no GPU decoder.
func plan444Software() videoPlan {
	plan := planVideo(videoAuto, videoCaps{HEVCSoftware: true}, true)
	plan.Chroma444 = true
	plan.Kinds444 = []decoderKind{decoderSoftware}
	plan.Offer.PixelFormats |= media.PixelFormat444FullRange
	return plan
}

// The Mac's 4:4:4 8-bit stream against the mock: offered, negotiated, decoded, and the stats card says so.
func TestMock444StreamEndToEnd(t *testing.T) {
	before := videoLive()
	clip := clip444(t)
	client, mac, closeAll := openMockMac(t, clip)
	defer closeAll()
	plan := plan444Software()
	presenter := &countingPresenter{}
	stream := newVideoStream(quiet, plan, "", 1, presenter, clientKeyframe(client))
	if err := client.StartMediaStream(plan.Offer, stream); err != nil {
		stream.close()
		t.Fatal(err)
	}
	waitFor(t, "4:4:4 pictures", 20*time.Second, func() bool { return presenter.count() >= 10 })
	if formats := mac.pixelFormats.Load(); media.PixelFormats(formats)&media.PixelFormat444FullRange == 0 {
		t.Fatalf("the offer did not name 4:4:4 8-bit: %v", media.PixelFormats(formats))
	}
	stats := stream.stats()
	if !is444EightBit(stats.Color) || stats.Kind != decoderSoftware {
		t.Fatalf("stats %+v", stats)
	}
	stats.Active = true
	rows := applyVideoRows([]statRow{{"Codec", "x"}}, stats)
	if rows[0].Value != "HEVC 4:4:4 (software)" {
		t.Fatalf("codec row %q", rows[0].Value)
	}
	stream.close()
	_ = client.StopMediaStream()
	if live := videoLive(); live != before {
		t.Fatalf("left behind: %+v, before %+v", live, before)
	}
}

// A plan that did not offer 4:4:4 refuses a 4:4:4 stream, and a plan that did takes it.
func TestPlanWithout444RefusesA444Stream(t *testing.T) {
	stream := newVideoStream(quiet, planVideo(videoAuto, videoCaps{HEVCSoftware: true}, true), "", 1, &countingPresenter{}, func() {})
	defer stream.close()
	stream.VideoFormat(format444(clip444(t)))
	select {
	case event := <-stream.events:
		if event.Kind != videoFailed {
			t.Fatalf("%+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a stream the plan did not ask for was taken")
	}
}

// When the GPU decoder dies on a 4:4:4 stream nothing else may take it, and the failure says that asking for 4:2:0
// would help, so the session starts over with that offer instead of giving up on video.
func TestFailedGPUOn444AsksFor420(t *testing.T) {
	caps := probeVideo("")
	if !caps.HEVC444x8VAAPI {
		t.Skip("no VA-API HEVC 4:4:4 8-bit decoder")
	}
	defer failDecodersForTest()
	clip := clip444(t)
	plan := planVideo(videoAuto, caps, true).with444(videoAuto, caps)
	if !plan.Chroma444 {
		t.Fatal("the plan has no 4:4:4")
	}
	stream := newVideoStream(quiet, plan, caps.Device, 1, &countingPresenter{}, func() {})
	defer stream.close()
	failDecodersForTest(decoderVAAPI)
	stream.VideoFormat(format444(clip))
	stream.VideoAccessUnit(clipUnit(clip, 0))
	deadline := time.After(10 * time.Second)
	for {
		select {
		case event := <-stream.events:
			if event.Kind != videoFailed {
				continue
			}
			if !event.Without444 {
				t.Fatalf("the failure does not say 4:2:0 would help: %+v", event)
			}
			return
		case <-deadline:
			t.Fatal("no failure reported")
		}
	}
}

// A machine with no sound server must not break the stream: the player fails on its own goroutine, the network side
// never waits, mute and close still work.
func TestStreamSoundWithoutASoundServer(t *testing.T) {
	t.Setenv("PULSE_SERVER", "unix:/nonexistent/oss-test-pulse")
	t.Setenv("PIPEWIRE_REMOTE", "/nonexistent/oss-test-pipewire")
	plan := planVideo(videoAuto, videoCaps{HEVCSoftware: true}, true).withSound(true, true)
	stream := newVideoStream(quiet, plan, "", 1, &countingPresenter{}, func() {})
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream.AudioFormat(media.AudioFormat{FormatID: 0x61616365, SampleRate: 48000, Channels: 2, FramesPerPacket: 480, AudioSpecificConfig: []byte{0xf8, 0xe6, 0x50, 0x00}})
		for range 200 {
			stream.AudioPacket(media.AudioPacket{Data: []byte{1, 2, 3, 4}})
		}
		stream.setMuted(true)
		stream.setMuted(false)
		stream.close()
		// A format announced after the stream is closed must not leave a player behind.
		stream.AudioFormat(media.AudioFormat{SampleRate: 48000, Channels: 2, FramesPerPacket: 480, AudioSpecificConfig: []byte{0xf8, 0xe6, 0x50, 0x00}})
		stream.AudioPacket(media.AudioPacket{Data: []byte{1}})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream waited for the sound")
	}
	if _, ok := stream.soundStats(); ok {
		t.Fatal("a closed stream still has a player")
	}
}

// A plan without sound ignores the Mac's sound altogether.
func TestStreamWithoutSoundPlansIgnoresAudio(t *testing.T) {
	stream := newVideoStream(quiet, planVideo(videoAuto, videoCaps{HEVCSoftware: true}, true), "", 1, &countingPresenter{}, func() {})
	defer stream.close()
	stream.AudioFormat(media.AudioFormat{SampleRate: 48000, Channels: 2, FramesPerPacket: 480, AudioSpecificConfig: []byte{0xf8, 0xe6, 0x50, 0x00}})
	stream.AudioPacket(media.AudioPacket{Data: []byte{1}})
	if _, ok := stream.soundStats(); ok {
		t.Fatal("a plan without sound started a player")
	}
}

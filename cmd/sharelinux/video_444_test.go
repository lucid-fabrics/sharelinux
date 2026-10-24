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

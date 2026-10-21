//go:build linux

package main

import (
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/nekohasekai/open-screen-sharing/cmd/sharelinux/internal/mockmedia"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// shownScreen puts a screen widget in a window, so it draws.
func shownScreen(t *testing.T, clip *mockmedia.Clip) *screenView {
	t.Helper()
	requireDisplay(t)
	view := newScreenView()
	window := gtk.NewWindow()
	window.SetDefaultSize(400, 300)
	window.SetChild(view)
	view.resize(clip.Width, clip.Height)
	view.setViewport(viewport{0, 0, float32(clip.Width), float32(clip.Height)})
	window.Present()
	t.Cleanup(func() {
		view.clear()
		window.Destroy()
		pump(func() bool { return false }, 100*time.Millisecond)
	})
	return view
}

// decodeFrames decodes n pictures of the clip and returns their frames.
func decodeFrames(t *testing.T, kind decoderKind, clip *mockmedia.Clip, n int) []*videoFrame {
	t.Helper()
	d, err := openDecoder(decoderOptions{Codec: media.CodecHEVC, Kind: kind, Threads: 1}, clipFormat(clip))
	if err != nil {
		t.Skipf("no %v decoder: %v", kind, err)
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

// Only the newest picture is drawn: one that was not drawn before the next arrived is dropped, and
// counted.
func TestScreenDrawsTheLatestPicture(t *testing.T) {
	clip := builtinClip(t)
	view := shownScreen(t, clip)
	frames := decodeFrames(t, decoderSoftware, clip, 5)
	view.setVideo(true)
	for _, frame := range frames {
		if output := view.present(frame); output != outputMemory {
			t.Fatalf("a software picture goes through memory, not %v", output)
		}
	}
	counters := view.videoCounters()
	if counters.Queued != 5 || counters.Replaced != 4 {
		t.Fatalf("%+v: five handed over before the loop ran, four of them replaced", counters)
	}
	if !pump(func() bool { return view.videoCounters().Presented == 1 }, 5*time.Second) {
		t.Fatalf("the picture was never drawn: %+v", view.videoCounters())
	}
	counters = view.videoCounters()
	if !counters.Active || counters.Presented != 1 || counters.AgeLast <= 0 {
		t.Fatalf("%+v", counters)
	}
}

// Without a window to draw into, the textures still go when the widget lets go of them.
func TestScreenReleaseVideoFreesPictures(t *testing.T) {
	clip := builtinClip(t)
	caps := vaapiOrSkip(t)
	_ = caps
	before := videoLive()
	view := shownScreen(t, clip)
	view.chooseDMABuf()
	frames := decodeFrames(t, decoderVAAPI, clip, 6)
	view.setVideo(true)
	var outputs []outputKind
	for _, frame := range frames {
		outputs = append(outputs, view.present(frame))
	}
	for _, output := range outputs {
		if output != outputDMABuf && output != outputCopy {
			t.Fatalf("a GPU picture reached the screen as %v", output)
		}
	}
	pump(func() bool { return view.videoCounters().Presented > 0 }, 5*time.Second)
	view.clear()
	// The decoder is still open; what the widget held is gone. GSK may keep the last texture it
	// drew for a moment in its own cache, so the counts get a few seconds to settle.
	settled := pump(func() bool {
		live := videoLive()
		return live.Textures == before.Textures && live.FDs == before.FDs
	}, 5*time.Second)
	if live := videoLive(); !settled {
		t.Fatalf("textures or descriptors left: %+v, before %+v", live, before)
	}
}

// A GPU picture is shown as a DMA-BUF when the display takes NV12 DMA-BUFs, and read back otherwise.
func TestScreenUsesDMABufWhenTheDisplayTakesIt(t *testing.T) {
	clip := builtinClip(t)
	vaapiOrSkip(t)
	view := shownScreen(t, clip)
	supported := dmabufNV12Supported()
	frames := decodeFrames(t, decoderVAAPI, clip, 3)
	view.setVideo(true)
	// The display may learn its DMA-BUF formats a moment after it opens.
	pump(func() bool { return false }, 300*time.Millisecond)
	renderer := view.chooseDMABuf()
	supported = supported && renderer.Accelerated
	t.Logf("renderer %s %q accelerated %v", renderer.Name, renderer.GL, renderer.Accelerated)
	var last outputKind
	for _, frame := range frames {
		last = view.present(frame)
	}
	if supported && last != outputDMABuf {
		t.Fatalf("a GPU draws and the display takes NV12 DMA-BUFs but the picture went as %v", last)
	}
	if !supported && last != outputCopy {
		t.Fatalf("no GPU path for DMA-BUFs here but the picture went as %v", last)
	}
	t.Logf("NV12 DMA-BUF import: %v, output %v", supported, last)
	if last == outputDMABuf {
		if live := videoLive(); live.FDs == 0 || live.Textures == 0 {
			t.Fatalf("a DMA-BUF texture holds descriptors: %+v", live)
		}
	}
	_ = view.setDMABuf(false)
	frames = decodeFrames(t, decoderVAAPI, clip, 2)
	if got := view.present(frames[len(frames)-1]); got != outputCopy {
		t.Fatalf("with DMA-BUF off the picture is read back, not %v", got)
	}
}

// If GDK refuses the import, the picture still shows (read back), and nothing is left over or freed twice.
func TestDMABufFailureFallsBackAndLeaksNothing(t *testing.T) {
	clip := builtinClip(t)
	vaapiOrSkip(t)
	view := shownScreen(t, clip)
	if view.chooseDMABuf(); !view.dmabuf {
		t.Skip("no DMA-BUF path on this display, so there is no import to fail")
	}
	before := videoLive()
	frames := decodeFrames(t, decoderVAAPI, clip, 4)
	view.setVideo(true)
	pump(func() bool { return false }, 300*time.Millisecond)
	breakDMABufForTest(true)
	defer breakDMABufForTest(false)
	for _, frame := range frames {
		if got := view.present(frame); got != outputCopy {
			t.Fatalf("a refused import must fall back to the read back, got %v", got)
		}
	}
	view.clear()
	pump(func() bool {
		live := videoLive()
		return live.Textures == before.Textures && live.FDs == before.FDs
	}, 5*time.Second)
	live := videoLive()
	if live.Textures != before.Textures || live.FDs != before.FDs {
		t.Fatalf("%+v, before %+v: a failed import leaked or freed twice", live, before)
	}
	if live.FDs < 0 || live.Textures < 0 || live.AVFrames < 0 {
		t.Fatalf("negative counts: %+v", live)
	}
}

// The decoder's GPU surface can be bigger than the picture; the widget draws only the picture.
func TestScreenDrawsTheVisiblePartOfAGPUSurface(t *testing.T) {
	clip := croppedClip(t)
	vaapiOrSkip(t)
	view := shownScreen(t, clip)
	view.chooseDMABuf()
	frames := decodeFrames(t, decoderVAAPI, clip, 2)
	view.setVideo(true)
	for _, frame := range frames {
		if frame.Width != 318 || frame.Height != 190 {
			t.Fatalf("the picture is %dx%d, want the visible 318x190 (surface %dx%d)", frame.Width, frame.Height, frame.TexWidth, frame.TexHeight)
		}
		t.Logf("visible %dx%d, surface %dx%d", frame.Width, frame.Height, frame.TexWidth, frame.TexHeight)
		if output := view.present(frame); output == outputNone {
			t.Fatal("the picture was not shown")
		}
	}
	if !pump(func() bool { return view.videoCounters().Presented > 0 }, 5*time.Second) {
		t.Fatal("never drawn")
	}
}

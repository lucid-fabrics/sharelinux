//go:build linux

package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// waitForEvent reads the stream's events until one of the kind comes.
func waitForEvent(t *testing.T, stream *videoStream, kind videoEventKind, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		select {
		case event := <-stream.events:
			if event.Kind == kind {
				return
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("no event %v within %v", kind, limit)
}

// A decoder that breaks in the middle of a stream is closed and replaced; nothing may touch it after
// that (the counters used to be read from the closed decoder, which crashed). With no decoder left
// the stream says it cannot be used, and everything is given back.
func TestMidStreamFailureWithNoDecoderLeftFailsCleanly(t *testing.T) {
	clip := builtinClip(t)
	if !probeVideo("").HEVCSoftware {
		t.Skip("no software HEVC decoder")
	}
	before := videoLive()
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	presenter := &countingPresenter{}
	var keyframes atomic.Int32
	stream := newVideoStream(quiet, plan, "", 1, presenter, func() { keyframes.Add(1) })
	stream.VideoFormat(clipFormat(clip))
	push(stream, clip, 0, 12)
	waitFor(t, "pictures", 5*time.Second, func() bool { return presenter.count() >= 12 })
	waitForEvent(t, stream, videoFirstPicture, time.Second)

	failDecodersForTest(decoderSoftware)
	defer failDecodersForTest()
	push(stream, clip, 12, 36)
	waitForEvent(t, stream, videoFailed, 5*time.Second)
	// The stream stays usable for the session that polls it, and later units are ignored.
	_ = stream.stats()
	_ = stream.idle(time.Now())
	push(stream, clip, 36, 48)
	stream.close()
	if after := videoLive(); after != before {
		t.Fatalf("after teardown %+v, before %+v", after, before)
	}
}

// With a GPU decoder that breaks mid stream, the software decoder takes over at the next keyframe and
// the pictures go on; when that one breaks too, the stream gives up so the session falls back to tiles.
func TestMidStreamGPUFailureFallsBackToSoftwareThenTiles(t *testing.T) {
	clip := builtinClip(t)
	caps := vaapiOrSkip(t)
	if !caps.HEVCSoftware {
		t.Skip("no software HEVC decoder")
	}
	before := videoLive()
	plan := planVideo(videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true}, true)
	presenter := &countingPresenter{}
	stream := newVideoStream(quiet, plan, "", 1, presenter, func() {})
	stream.VideoFormat(clipFormat(clip))
	push(stream, clip, 0, 12)
	waitFor(t, "GPU pictures", 5*time.Second, func() bool { return presenter.count() >= 12 })
	if stats := stream.stats(); stats.Kind != decoderVAAPI {
		t.Fatalf("decoding with %v, want the GPU", stats.Kind)
	}
	hardware := presenter.hardware

	failDecodersForTest(decoderVAAPI)
	defer failDecodersForTest()
	push(stream, clip, 12, 48)
	waitFor(t, "software pictures", 5*time.Second, func() bool {
		return stream.stats().Kind == decoderSoftware && presenter.count() > 12
	})
	if presenter.hardware != hardware {
		t.Fatal("no GPU picture comes after the GPU decoder failed")
	}

	failDecodersForTest(decoderVAAPI, decoderSoftware)
	push(stream, clip, 48, 72)
	waitForEvent(t, stream, videoFailed, 5*time.Second)
	_ = stream.stats()
	stream.close()
	if after := videoLive(); after != before {
		t.Fatalf("after teardown %+v, before %+v", after, before)
	}
}

// When libavcodec answers "full" the same packet is sent again after the pictures are taken. It
// used to be sent emptied, which tells the decoder the stream ended, and nothing decoded after.
func TestDecoderResendsThePacketAfterAFullAnswer(t *testing.T) {
	clip := builtinClip(t)
	d := openSoftware(t, clip, 1)
	defer d.close()
	fullAnswersForTest(1)
	defer fullAnswersForTest(0)
	for index := range 24 {
		unit := clipUnit(clip, index)
		frames, result := d.decode(unit.Data, unit.Sync, 0)
		if result != decodeOK || frames != 1 {
			t.Fatalf("picture %d: %d frames, result %v", index, frames, result)
		}
		d.next().free()
	}
	if _, _, errs, _, _, _ := d.stats(); errs != 0 {
		t.Fatalf("%d errors", errs)
	}
}

// bgraDistance is how far apart two pixels are, at most.
func bgraDistance(a, b [4]byte) int {
	most := 0
	for index := range a {
		difference := int(a[index]) - int(b[index])
		most = max(most, difference, -difference)
	}
	return most
}

// The swscale path shows the visible part of the picture: the crop at the top and left moves where
// the planes are read from, not only the size.
func TestSwscalePathHonoursTheTopLeftCrop(t *testing.T) {
	clip := builtinClip(t)
	forceSwscaleForTest(true)
	defer forceSwscaleForTest(false)
	frames := decodeFrames(t, decoderSoftware, clip, 1)
	frame := frames[0]
	defer frame.free()
	color := media.Color{Primaries: 1, Transfer: 1, Matrix: 1}
	const left, top = 4, 2
	different := false
	for _, at := range [][2]int{{10, 10}, {60, 40}, {150, 90}, {200, 30}, {300, 180}} {
		x, y := at[0], at[1]
		cropped, _, ok := frame.textureForTest(left, top, x, y, color)
		shifted, _, ok2 := frame.textureForTest(0, 0, x+left, y+top, color)
		plain, _, ok3 := frame.textureForTest(0, 0, x, y, color)
		if !ok || !ok2 || !ok3 {
			t.Fatalf("no pixel at %d,%d", x, y)
		}
		if bgraDistance(cropped, shifted) > 6 {
			t.Fatalf("at %d,%d the cropped picture shows %v, the coded picture has %v at %d,%d", x, y, cropped, shifted, x+left, y+top)
		}
		different = different || bgraDistance(cropped, plain) > 6
	}
	if !different {
		t.Fatal("the test picture is too flat to tell a shifted crop from none")
	}
}

// What swscale makes is RGB in the stream's primaries: a wide gamut stream says so, and one in BT.709 does not.
func TestSwscalePathTagsTheStreamsPrimaries(t *testing.T) {
	clip := builtinClip(t)
	forceSwscaleForTest(true)
	defer forceSwscaleForTest(false)
	bt709 := media.Color{Primaries: 1, Transfer: 13, Matrix: 1}
	p3 := media.Color{Primaries: 12, Transfer: 13, Matrix: 1}
	for _, tag := range []media.Color{bt709, p3} {
		format := clipFormat(clip)
		format.Color = media.Color{BitDepth: 8, ChromaFormat: 1, Primaries: tag.Primaries, Transfer: tag.Transfer, Matrix: tag.Matrix}
		d, err := openDecoder(decoderOptions{Codec: media.CodecHEVC, Kind: decoderSoftware, Threads: 1}, format)
		if err != nil {
			t.Skip(err)
		}
		unit := clipUnit(clip, 0)
		if frames, result := d.decode(unit.Data, unit.Sync, 0); result != decodeOK || frames != 1 {
			t.Fatal(frames, result)
		}
		frame := d.next()
		_, own, ok := frame.textureForTest(0, 0, 1, 1, tag)
		_, other, _ := frame.textureForTest(0, 0, 1, 1, map[bool]media.Color{true: p3, false: bt709}[tag == bt709])
		frame.free()
		d.close()
		if !ok || !own || other {
			t.Fatalf("stream %+v: texture tagged as itself %v, as the other gamut %v (pixel read %v)", tag, own, other, ok)
		}
	}
}

// One session giving up on DMA-BUFs does not stop another from using them, and a screen tries again when its
// next run of the video starts.
func TestDMABufGiveUpIsPerScreenAndRecovers(t *testing.T) {
	clip := builtinClip(t)
	vaapiOrSkip(t)
	first := shownScreen(t, clip)
	if first.chooseDMABuf(); !first.dmabuf {
		t.Skip("no DMA-BUF path on this display")
	}
	frames := decodeFrames(t, decoderVAAPI, clip, 10)
	first.setVideo(true)
	pump(func() bool { return false }, 300*time.Millisecond)
	breakDMABufForTest(true)
	for _, frame := range frames[:4] {
		if got := first.present(frame); got != outputCopy {
			t.Fatalf("a refused import is read back, got %v", got)
		}
	}
	breakDMABufForTest(false)
	if got := first.present(frames[4]); got != outputCopy {
		t.Fatalf("the screen that gave up still tries, got %v", got)
	}
	second := shownScreen(t, clip)
	second.chooseDMABuf()
	second.setVideo(true)
	if got := second.present(frames[5]); got != outputDMABuf {
		t.Fatalf("another screen is not affected, got %v", got)
	}
	first.setVideo(true)
	if got := first.present(frames[6]); got != outputDMABuf {
		t.Fatalf("the next run of the video tries again, got %v", got)
	}
	for _, frame := range frames[7:] {
		frame.free()
	}
}

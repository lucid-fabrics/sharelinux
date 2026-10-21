package mockmedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

func mustClip(t *testing.T, open func() (*Clip, error)) *Clip {
	t.Helper()
	clip, err := open()
	if err != nil {
		t.Fatal(err)
	}
	return clip
}

func TestBuiltinClips(t *testing.T) {
	clip := mustClip(t, Builtin)
	if clip.Width != 320 || clip.Height != 192 {
		t.Fatalf("%dx%d", clip.Width, clip.Height)
	}
	if len(clip.Units) != 24 {
		t.Fatalf("%d pictures", len(clip.Units))
	}
	for index, unit := range clip.Units {
		if want := index%12 == 0; unit.Sync != want {
			t.Errorf("picture %d: sync %v, want %v", index, unit.Sync, want)
		}
	}
	// A keyframe carries the parameter sets in front of its slice.
	var kinds []int
	for _, nal := range clip.Units[0].NALs {
		kinds = append(kinds, nalType(nal))
	}
	if len(kinds) < 4 || kinds[0] != 32 || kinds[1] != 33 || kinds[2] != 34 {
		t.Fatalf("NAL types of the first picture: %v", kinds)
	}
	small := mustClip(t, BuiltinSmall)
	if small.Width != 192 || small.Height != 128 {
		t.Fatalf("%dx%d", small.Width, small.Height)
	}
}

func TestCroppedClip(t *testing.T) {
	clip := mustClip(t, BuiltinCropped)
	// The sequence header gives the coded size; the conformance window is what the decoder crops.
	if clip.Width != 320 || clip.Height != 192 || len(clip.Units) != 24 || !clip.Units[0].Sync {
		t.Fatalf("%dx%d with %d pictures", clip.Width, clip.Height, len(clip.Units))
	}
}

func TestParseAnnexBRefusesBadClips(t *testing.T) {
	if _, err := ParseAnnexB(nil); err == nil {
		t.Fatal("an empty clip must be refused")
	}
	if _, err := ParseAnnexB([]byte("not a video at all")); err == nil {
		t.Fatal("garbage must be refused")
	}
	clip := mustClip(t, Builtin)
	// Start in the middle: a P picture first, and no parameter sets.
	var stream []byte
	for _, unit := range clip.Units[1:12] {
		for _, nal := range unit.NALs {
			stream = append(stream, 0, 0, 0, 1)
			stream = append(stream, nal...)
		}
	}
	if _, err := ParseAnnexB(stream); err == nil {
		t.Fatal("a clip that does not start with a keyframe must be refused")
	}
}

// parsePacket splits an RTP packet the way the viewer reads it.
type packet struct {
	marker    bool
	sequence  uint16
	timestamp uint32
	ssrc      uint32
	payload   []byte
}

func parsePacket(t *testing.T, data []byte) packet {
	t.Helper()
	if len(data) < 13 || data[0] != 0x80 || data[1]&0x7f != payloadTypeHEVC {
		t.Fatalf("not an HEVC RTP packet: % x", data[:min(len(data), 14)])
	}
	return packet{
		marker:    data[1]&0x80 != 0,
		sequence:  binary.BigEndian.Uint16(data[2:]),
		timestamp: binary.BigEndian.Uint32(data[4:]),
		ssrc:      binary.BigEndian.Uint32(data[8:]),
		payload:   data[12:],
	}
}

// The packets of every picture must give back its NAL units, and say where the picture ends.
func TestPacketsRebuildEveryPicture(t *testing.T) {
	clip := mustClip(t, Builtin)
	source := clip.newSource(media.ServerVideoRequest{ClientSSRC: 7}, 30)
	var fragmented bool
	var lastSequence uint16
	for index, unit := range clip.Units {
		source.access.Lock()
		source.enqueueLocked(time.Now())
		packets := source.queue
		source.queue = nil
		source.access.Unlock()
		var rebuilt [][]byte
		var fragment []byte
		var timestamp uint32
		for position, raw := range packets {
			if len(raw) > 12+maxPayload+3 {
				t.Fatalf("picture %d: a packet of %d bytes", index, len(raw))
			}
			p := parsePacket(t, raw)
			if p.ssrc != source.info.SSRC {
				t.Fatalf("SSRC %d", p.ssrc)
			}
			if position == 0 {
				timestamp = p.timestamp
			} else if p.timestamp != timestamp {
				t.Fatalf("picture %d: the timestamp changes inside it", index)
			}
			if index > 0 || position > 0 {
				if p.sequence != lastSequence+1 {
					t.Fatalf("sequence %d after %d", p.sequence, lastSequence)
				}
			}
			lastSequence = p.sequence
			if p.marker != (position == len(packets)-1) {
				t.Fatalf("picture %d packet %d: marker %v", index, position, p.marker)
			}
			kind := int(p.payload[0]>>1) & 0x3f
			if kind != nalFragment {
				rebuilt = append(rebuilt, p.payload)
				continue
			}
			fragmented = true
			header := p.payload[2]
			if header&0x80 != 0 {
				fragment = []byte{p.payload[0]&0x81 | (header&0x3f)<<1, p.payload[1]}
			}
			fragment = append(fragment, p.payload[3:]...)
			if header&0x40 != 0 {
				rebuilt = append(rebuilt, fragment)
				fragment = nil
			}
		}
		if len(rebuilt) != len(unit.NALs) {
			t.Fatalf("picture %d: %d NAL units, want %d", index, len(rebuilt), len(unit.NALs))
		}
		for n := range rebuilt {
			if !bytes.Equal(rebuilt[n], unit.NALs[n]) {
				t.Fatalf("picture %d NAL %d differs", index, n)
			}
		}
	}
	if !fragmented {
		t.Fatal("the clip never needed a fragmented NAL unit, so that path is untested")
	}
}

func TestSourceTimestampsAdvance(t *testing.T) {
	clip := mustClip(t, Builtin)
	source := clip.newSource(media.ServerVideoRequest{}, 30)
	source.access.Lock()
	source.enqueueLocked(time.Now())
	first := parsePacket(t, source.queue[0]).timestamp
	source.queue = nil
	source.enqueueLocked(time.Now())
	second := parsePacket(t, source.queue[0]).timestamp
	source.access.Unlock()
	if second-first != 24000/30 {
		t.Fatalf("the clock moved %d, want %d", second-first, 24000/30)
	}
}

func readPicture(t *testing.T, ctx context.Context, source *Source) (first packet, count int) {
	t.Helper()
	buffer := make([]byte, 2000)
	for {
		n, rtcp, err := source.ReadPacket(ctx, buffer)
		if err != nil || rtcp {
			t.Fatalf("read: %v rtcp %v", err, rtcp)
		}
		p := parsePacket(t, append([]byte(nil), buffer[:n]...))
		if count == 0 {
			first = p
		}
		count++
		if p.marker {
			return first, count
		}
	}
}

func TestSourceIsPacedAndLoops(t *testing.T) {
	clip := mustClip(t, Builtin)
	source := clip.newSource(media.ServerVideoRequest{}, 100)
	defer source.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	for range len(clip.Units) + 3 {
		readPicture(t, ctx, source)
	}
	elapsed := time.Since(started)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("27 pictures at 100 fps took %v: the source does not pace itself", elapsed)
	}
	if source.Frames() != uint64(len(clip.Units)+3) {
		t.Fatalf("%d pictures sent", source.Frames())
	}
}

func TestRequestKeyFrameRestartsTheClip(t *testing.T) {
	clip := mustClip(t, Builtin)
	source := clip.newSource(media.ServerVideoRequest{}, 1000)
	defer source.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range 5 {
		readPicture(t, ctx, source)
	}
	if clip.Units[source.index].Sync {
		t.Fatal("the test needs to be in the middle of a run")
	}
	if err := source.RequestKeyFrame(); err != nil {
		t.Fatal(err)
	}
	first, _ := readPicture(t, ctx, source)
	kind := int(first.payload[0]>>1) & 0x3f
	if kind != 32 {
		t.Fatalf("the picture after a request starts with NAL type %d, want the VPS (32)", kind)
	}
}

func TestSourceStopsWhenClosedOrCancelled(t *testing.T) {
	clip := mustClip(t, Builtin)
	source := clip.newSource(media.ServerVideoRequest{}, 1)
	buffer := make([]byte, 2000)
	ctx, cancel := context.WithCancel(context.Background())
	// The first picture is due at once; the second is a second away.
	readPicture(t, ctx, source)
	done := make(chan error, 1)
	go func() {
		_, _, err := source.ReadPacket(ctx, buffer)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled read did not return")
	}
	go func() {
		_, _, err := source.ReadPacket(context.Background(), buffer)
		done <- err
	}()
	source.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a closed source must fail the read")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing did not end the read")
	}
}

func TestSourceInfo(t *testing.T) {
	clip := mustClip(t, Builtin)
	source := clip.newSource(media.ServerVideoRequest{ClientSSRC: 99}, 60)
	info := source.Info()
	if info.Width != 320 || info.Height != 192 || info.FrameRate != 60 || info.TilesPerFrame != 1 || info.ReceiverSSRC != 99 || info.SSRC == 0 {
		t.Fatalf("%+v", info)
	}
	if info.PixelFormat != media.PixelFormat420VideoRange {
		t.Fatalf("pixel format %v", info.PixelFormat)
	}
}

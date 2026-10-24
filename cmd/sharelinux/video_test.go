//go:build linux

package main

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/cmd/sharelinux/internal/mockmedia"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

var quiet = slog.New(slog.DiscardHandler)

// clipFormat is the VideoFormat the core would deliver for a clip: its parameter sets, in BT.709 limited range.
func clipFormat(clip *mockmedia.Clip) media.VideoFormat {
	format := media.VideoFormat{
		Codec: media.CodecHEVC, Bands: 1, Width: clip.Width, Height: clip.Height,
		Color: media.Color{BitDepth: 8, ChromaFormat: 1, Primaries: 1, Transfer: 1, Matrix: 1},
	}
	for _, nal := range clip.Units[0].NALs {
		if kind := int(nal[0]>>1) & 0x3f; kind >= 32 && kind <= 34 {
			format.ParameterSets = append(format.ParameterSets, nal)
		}
	}
	return format
}

// clipUnit is picture index as the core delivers it: the slices behind four byte lengths, parameter sets removed.
func clipUnit(clip *mockmedia.Clip, index int) media.VideoAccessUnit {
	unit := clip.Units[index%len(clip.Units)]
	var data []byte
	for _, nal := range unit.NALs {
		if kind := int(nal[0]>>1) & 0x3f; kind >= 32 && kind <= 34 {
			continue
		}
		size := len(nal)
		data = append(data, byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
		data = append(data, nal...)
	}
	return media.VideoAccessUnit{Data: data, Sync: unit.Sync, Band: 0, Bands: 1, FrameID: int64(index + 1), FrameEnd: true, ReceivedAt: time.Now()}
}

// push delivers pictures from..to at a pace a stream has, not all at once, which the queue would shed.
func push(stream *videoStream, clip *mockmedia.Clip, from, to int) {
	for index := from; index < to; index++ {
		stream.VideoAccessUnit(clipUnit(clip, index))
		time.Sleep(4 * time.Millisecond)
	}
}

func builtinClip(t *testing.T) *mockmedia.Clip {
	t.Helper()
	clip, err := mockmedia.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	return clip
}

func croppedClip(t *testing.T) *mockmedia.Clip {
	t.Helper()
	clip, err := mockmedia.BuiltinCropped()
	if err != nil {
		t.Fatal(err)
	}
	return clip
}

func smallClip(t *testing.T) *mockmedia.Clip {
	t.Helper()
	clip, err := mockmedia.BuiltinSmall()
	if err != nil {
		t.Fatal(err)
	}
	return clip
}

// countingPresenter takes frames the way a widget would and records them.
type countingPresenter struct {
	delay time.Duration

	access   sync.Mutex
	frames   int
	hardware int
	sizes    [][2]int
	luma     []float64
}

func (p *countingPresenter) present(frame *videoFrame) outputKind {
	defer frame.free()
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	p.access.Lock()
	defer p.access.Unlock()
	p.frames++
	if frame.Hardware {
		p.hardware++
	}
	if size := [2]int{frame.Width, frame.Height}; len(p.sizes) == 0 || p.sizes[len(p.sizes)-1] != size {
		p.sizes = append(p.sizes, size)
	}
	p.luma = append(p.luma, frame.lumaMean())
	if frame.Hardware {
		return outputCopy
	}
	return outputMemory
}

func (p *countingPresenter) count() int {
	p.access.Lock()
	defer p.access.Unlock()
	return p.frames
}

func waitFor(t *testing.T, what string, limit time.Duration, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func vaapiOrSkip(t *testing.T) videoCaps {
	t.Helper()
	caps := probeVideo("")
	if !caps.HEVCVAAPI {
		t.Skipf("no VA-API HEVC decoder here (%s)", caps.Error)
	}
	return caps
}

func TestProbeFindsSoftwareDecoder(t *testing.T) {
	caps := probeVideo("")
	if !caps.HEVCSoftware {
		t.Skip("this libavcodec has no HEVC decoder")
	}
	if !caps.any() {
		t.Fatal("a probe with a decoder has some decoder")
	}
	if caps.HEVCVAAPI && caps.Driver == "" {
		t.Fatal("VA-API works but the driver has no name")
	}
	if !caps.HEVCVAAPI && caps.Error == "" {
		t.Fatal("VA-API is missing and the probe does not say why")
	}
}

func TestProbeWithBrokenDevice(t *testing.T) {
	caps := probeVideo("/dev/dri/not-a-device")
	if caps.HEVCVAAPI || caps.H264VAAPI || caps.Error == "" {
		t.Fatalf("%+v", caps)
	}
}

func openSoftware(t *testing.T, clip *mockmedia.Clip, threads int) *decoder {
	t.Helper()
	d, err := openDecoder(decoderOptions{Codec: media.CodecHEVC, Kind: decoderSoftware, Threads: threads}, clipFormat(clip))
	if err != nil {
		t.Skipf("no software HEVC decoder: %v", err)
	}
	return d
}

func TestSoftwareDecodesTheClip(t *testing.T) {
	clip := builtinClip(t)
	d := openSoftware(t, clip, 1)
	defer d.close()
	for index := range len(clip.Units) {
		unit := clipUnit(clip, index)
		frames, result := d.decode(unit.Data, unit.Sync, time.Millisecond)
		if result != decodeOK || frames != 1 {
			t.Fatalf("picture %d: %d frames, result %v", index, frames, result)
		}
		frame := d.next()
		if frame == nil {
			t.Fatalf("picture %d: no frame", index)
		}
		if frame.Width != 320 || frame.Height != 192 || frame.Hardware {
			t.Fatalf("picture %d: %dx%d hardware %v", index, frame.Width, frame.Height, frame.Hardware)
		}
		// The test pattern is neither black nor white, in limited range.
		if mean := frame.lumaMean(); mean < 20 || mean > 235 {
			t.Fatalf("picture %d: luma %.1f", index, mean)
		}
		frame.free()
	}
	units, frames, errs, total, _, _ := d.stats()
	if units != 24 || frames != 24 || errs != 0 || total <= 0 {
		t.Fatalf("stats: %d units %d frames %d errors, %v decoding", units, frames, errs, total)
	}
	if d.next() != nil {
		t.Fatal("nothing is left")
	}
}

// A picture with a conformance window is shown at its visible size, not the coded one.
func TestDecodedPictureIsCropped(t *testing.T) {
	clip := croppedClip(t)
	d := openSoftware(t, clip, 1)
	defer d.close()
	unit := clipUnit(clip, 0)
	if frames, result := d.decode(unit.Data, unit.Sync, 0); result != decodeOK || frames != 1 {
		t.Fatalf("%d frames, %v", frames, result)
	}
	frame := d.next()
	defer frame.free()
	if frame.Width != 318 || frame.Height != 190 {
		t.Fatalf("%dx%d, want the visible 318x190", frame.Width, frame.Height)
	}
	if mean := frame.lumaMean(); mean < 20 || mean > 235 {
		t.Fatalf("luma %.1f", mean)
	}
}

// A unit that is not a keyframe cannot start the stream, and a unit the decoder refuses
// makes it wait for the next keyframe again.
func TestDecoderWaitsForKeyframes(t *testing.T) {
	clip := builtinClip(t)
	d := openSoftware(t, clip, 1)
	defer d.close()
	feed := func(index int) decodeResult {
		unit := clipUnit(clip, index)
		frames, result := d.decode(unit.Data, unit.Sync, 0)
		for ; frames > 0; frames-- {
			d.next().free()
		}
		return result
	}
	if feed(3) != decodeBadData {
		t.Fatal("a P picture cannot start the stream")
	}
	if feed(0) != decodeOK || feed(1) != decodeOK {
		t.Fatal("the stream starts at the keyframe")
	}
	if _, result := d.decode([]byte{0, 0, 0, 200, 1, 2, 3}, false, 0); result != decodeBadData {
		t.Fatal("a length that runs past the unit is refused")
	}
	if _, result := d.decode(nil, true, 0); result != decodeBadData {
		t.Fatal("an empty unit is refused")
	}
	if feed(2) != decodeBadData {
		t.Fatal("after a refused unit the decoder waits for a keyframe")
	}
	if feed(12) != decodeOK {
		t.Fatal("and takes the next one")
	}
	d.flush()
	if feed(13) != decodeBadData {
		t.Fatal("a flush also waits for a keyframe")
	}
	if feed(0) != decodeOK {
		t.Fatal("which starts it again")
	}
}

// Everything the decoder makes is counted, and a closed decoder gives all of it back, also the
// frames nobody took.
func TestDecoderTeardownAccounting(t *testing.T) {
	clip := builtinClip(t)
	before := videoLive()
	d := openSoftware(t, clip, 1)
	if videoLive().Decoders != before.Decoders+1 {
		t.Fatal("the decoder is counted")
	}
	var taken []*videoFrame
	for index := range 6 {
		unit := clipUnit(clip, index)
		if _, result := d.decode(unit.Data, unit.Sync, 0); result != decodeOK {
			t.Fatal(result)
		}
		if index < 3 {
			taken = append(taken, d.next())
		}
	}
	live := videoLive()
	if live.Frames != before.Frames+6 || live.AVFrames != before.AVFrames+6 {
		t.Fatalf("6 frames alive, counted %+v from %+v", live, before)
	}
	for _, frame := range taken {
		frame.free()
	}
	d.close()
	if after := videoLive(); after != before {
		t.Fatalf("after close %+v, before %+v", after, before)
	}
	d.close()
}

// openMock runs a mock Mac that plays clip, and a client connected to it. It uses its own loopback
// address because the viewer's media socket takes the server's port number.
func openMock(t *testing.T, clip *mockmedia.Clip) (*rfb.Client, func()) {
	t.Helper()
	client, _, closeAll := openMockMac(t, clip)
	return client, closeAll
}

// openMockMac is openMock that also returns the Mac, to reach what it plays.
func openMockMac(t *testing.T, clip *mockmedia.Clip) (*rfb.Client, *mockMac, func()) {
	t.Helper()
	mac := &mockMac{clip: clip}
	server, err := rfb.NewServer(rfb.ServerOptions{
		Name: "mock", Username: "mock", Password: "secret", Logger: quiet,
		Handler: mac,
		Flags:   rfb.ServerFlagObserveOnly | rfb.ServerFlagExtended,
		Media: media.ServerStreamConfig{
			AllowLoopbackTarget: true, DisableAudioLeg: true,
			ListenUDP: func(port uint16) (*net.UDPConn, error) {
				return net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: int(port)})
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("no second loopback address: %v", err)
	}
	go server.Serve(listener)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := rfb.Dial(ctx, rfb.Options{
		Address: listener.Addr().String(), Username: "mock", Password: "secret", Mode: rfb.ModeObserve, Logger: quiet,
	}, newHandler(quiet, &latency{}))
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	go client.Run()
	return client, mac, func() {
		_ = client.Close()
		server.Close()
	}
}

// mockMac is the server side of a test: a Mac with no picture of its own and a video stream.
type mockMac struct {
	clip *mockmedia.Clip

	source atomic.Pointer[mockmedia.Source]
	// pixelFormats are the formats the viewer offered, as the media negotiation delivered them.
	pixelFormats atomic.Uint32
	// mute holds back the video packets, as a Mac with a still screen sends none; its sender reports go on.
	mute atomic.Bool
}

// mutableSource is a mock Mac's video that can be held back.
type mutableSource struct {
	*mockmedia.Source
	mute *atomic.Bool
}

func (m mutableSource) ReadPackets(ctx context.Context, packets []media.ServerPacket) (int, error) {
	for m.mute.Load() {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return m.Source.ReadPackets(ctx, packets)
}

func (m *mockMac) Screen() rfb.ServerScreen {
	return rfb.ServerScreen{Width: m.clip.Width, Height: m.clip.Height, Scale: 1}
}
func (m *mockMac) SessionStarted(*rfb.ServerSession)               {}
func (m *mockMac) SessionEnded(*rfb.ServerSession, error)          {}
func (m *mockMac) HandleEvent(*rfb.ServerSession, rfb.ViewerEvent) {}
func (m *mockMac) Clipboard(context.Context) (*rfb.Clipboard, error) {
	return nil, context.Canceled
}
func (m *mockMac) OpenAudio() (media.ServerAudioSource, error) { return nil, media.ErrAudioUnsupported }
func (m *mockMac) OpenVideo(ctx context.Context, request media.ServerVideoRequest) (media.ServerVideoSource, error) {
	m.pixelFormats.Store(uint32(request.PixelFormats))
	source, err := m.clip.OpenVideo(60)(ctx, request)
	if err == nil {
		m.source.Store(source.(*mockmedia.Source))
		source = mutableSource{source.(*mockmedia.Source), &m.mute}
	}
	return source, err
}

func clientKeyframe(client *rfb.Client) func() {
	return func() {
		if stream := client.MediaStream(); stream != nil {
			stream.RequestKeyframe()
		}
	}
}

// The whole path against a mock Mac: negotiation, encryption, packets, pictures, decoding, and a
// teardown that gives everything back.
func TestMockStreamEndToEnd(t *testing.T) {
	clip := builtinClip(t)
	kinds := []decoderKind{decoderSoftware}
	if probeVideo("").HEVCVAAPI {
		kinds = append(kinds, decoderVAAPI)
	}
	for _, kind := range kinds {
		t.Run(kind.String(), func(t *testing.T) {
			before := videoLive()
			client, closeAll := openMock(t, clip)
			defer closeAll()
			plan := planVideo(videoAuto, videoCaps{HEVCSoftware: kind == decoderSoftware, HEVCVAAPI: kind == decoderVAAPI}, true)
			if !plan.enabled() {
				t.Fatal(plan.Reason)
			}
			presenter := &countingPresenter{}
			stream := newVideoStream(quiet, plan, "", 1, presenter, clientKeyframe(client))
			if err := client.StartMediaStream(plan.Offer, stream); err != nil {
				stream.close()
				t.Fatal(err)
			}
			waitFor(t, "40 pictures", 15*time.Second, func() bool { return presenter.count() >= 40 })
			select {
			case event := <-stream.events:
				if event.Kind != videoFirstPicture {
					t.Fatalf("the first event is %v", event)
				}
			default:
				t.Fatal("the first picture is announced")
			}
			stats := stream.stats()
			if stats.Kind != kind || stats.Output == outputNone || stats.Width != 320 || stats.Height != 192 {
				t.Fatalf("%+v", stats)
			}
			if kind == decoderVAAPI && presenter.hardware == 0 || kind == decoderSoftware && presenter.hardware != 0 {
				t.Fatalf("%d hardware frames for the %s decoder", presenter.hardware, kind)
			}
			if err := client.StopMediaStream(); err != nil {
				t.Fatal(err)
			}
			stream.close()
			stream.close()
			if after := videoLive(); after != before {
				t.Fatalf("after teardown %+v, before %+v", after, before)
			}
		})
	}
}

// A stream that stops can be asked for again on the same connection, as a restart after a hiccup does.
func TestMockStreamRestarts(t *testing.T) {
	clip := builtinClip(t)
	before := videoLive()
	client, closeAll := openMock(t, clip)
	defer closeAll()
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	for round := range 3 {
		presenter := &countingPresenter{}
		stream := newVideoStream(quiet, plan, "", 1, presenter, clientKeyframe(client))
		if err := client.StartMediaStream(plan.Offer, stream); err != nil {
			stream.close()
			t.Fatalf("round %d: %v", round, err)
		}
		waitFor(t, "pictures", 15*time.Second, func() bool { return presenter.count() >= 10 })
		if err := client.StopMediaStream(); err != nil {
			t.Fatal(err)
		}
		stream.close()
	}
	if after := videoLive(); after != before {
		t.Fatalf("after three runs %+v, before %+v", after, before)
	}
}

// With a decoder that cannot start, the next one of the plan takes over and the stream goes on.
func TestStreamFallsBackToTheNextDecoder(t *testing.T) {
	clip := builtinClip(t)
	if !probeVideo("").HEVCSoftware {
		t.Skip("no software HEVC decoder")
	}
	plan := planVideo(videoAuto, videoCaps{HEVCVAAPI: true, HEVCSoftware: true}, true)
	if plan.Kinds[0] != decoderVAAPI {
		t.Fatal("the plan starts with the GPU")
	}
	presenter := &countingPresenter{}
	var keyframes atomic.Int32
	stream := newVideoStream(quiet, plan, "/dev/dri/not-a-device", 1, presenter, func() { keyframes.Add(1) })
	defer stream.close()
	stream.VideoFormat(clipFormat(clip))
	push(stream, clip, 0, 12)
	waitFor(t, "pictures from the second decoder", 5*time.Second, func() bool { return presenter.count() >= 12 })
	if stats := stream.stats(); stats.Kind != decoderSoftware {
		t.Fatalf("decoding with %v", stats.Kind)
	}
	var changed bool
	for done := false; !done; {
		select {
		case event := <-stream.events:
			changed = changed || event.Kind == videoDecoderChanged
		default:
			done = true
		}
	}
	if !changed {
		t.Fatal("the session is told the first decoder failed")
	}
}

// When no decoder of the plan works, the session is told the stream cannot be used.
func TestStreamFailsWhenNoDecoderWorks(t *testing.T) {
	clip := builtinClip(t)
	plan := planVideo(videoAuto, videoCaps{HEVCVAAPI: true}, true)
	stream := newVideoStream(quiet, plan, "/dev/dri/not-a-device", 1, &countingPresenter{}, nil)
	defer stream.close()
	stream.VideoFormat(clipFormat(clip))
	var failed bool
	waitFor(t, "a failure", 5*time.Second, func() bool {
		select {
		case event := <-stream.events:
			failed = failed || event.Kind == videoFailed
		default:
		}
		return failed
	})
}

// A stream the viewer did not ask for (bands, 10 bit) is refused without opening a decoder.
func TestStreamRefusesFormatsItDidNotOffer(t *testing.T) {
	clip := builtinClip(t)
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	cases := map[string]func(*media.VideoFormat){
		"four bands": func(f *media.VideoFormat) { f.Bands = 4 },
		"ten bit":    func(f *media.VideoFormat) { f.Color.BitDepth = 10 },
		"4:4:4":      func(f *media.VideoFormat) { f.Color.ChromaFormat = 3 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			stream := newVideoStream(quiet, plan, "", 1, &countingPresenter{}, nil)
			defer stream.close()
			format := clipFormat(clip)
			change(&format)
			stream.VideoFormat(format)
			select {
			case event := <-stream.events:
				if event.Kind != videoFailed || event.Text == "" {
					t.Fatalf("%+v", event)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no failure reported")
			}
		})
	}
}

// The Mac changes the size of the picture (a virtual display, a new window): the stream sends a new
// format and the decoder starts over at the new size.
func TestStreamFollowsAResize(t *testing.T) {
	big, small := builtinClip(t), smallClip(t)
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	presenter := &countingPresenter{}
	stream := newVideoStream(quiet, plan, "", 1, presenter, nil)
	defer stream.close()
	stream.VideoFormat(clipFormat(big))
	push(stream, big, 0, 13)
	waitFor(t, "pictures at the first size", 5*time.Second, func() bool { return presenter.count() >= 13 })
	stream.VideoFormat(clipFormat(small))
	push(stream, small, 0, 13)
	waitFor(t, "pictures at the second size", 5*time.Second, func() bool { return presenter.count() >= 26 })
	presenter.access.Lock()
	defer presenter.access.Unlock()
	want := [][2]int{{320, 192}, {192, 128}}
	if len(presenter.sizes) != 2 || presenter.sizes[0] != want[0] || presenter.sizes[1] != want[1] {
		t.Fatalf("sizes %v, want %v and no going back", presenter.sizes, want)
	}
}

// A decoder or screen that cannot keep up drops the backlog instead of falling further behind, asks
// for a keyframe and carries on from it.
func TestStreamShedsLoadAndRecovers(t *testing.T) {
	clip := builtinClip(t)
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	presenter := &countingPresenter{delay: 30 * time.Millisecond}
	var keyframes atomic.Int32
	stream := newVideoStream(quiet, plan, "", 1, presenter, func() { keyframes.Add(1) })
	defer stream.close()
	stream.VideoFormat(clipFormat(clip))
	// A burst of 60 pictures is ten times what the queue holds: the backlog goes, a keyframe is asked
	// for, and what is not a keyframe is dropped until one comes.
	for index := range 60 {
		stream.VideoAccessUnit(clipUnit(clip, index))
	}
	if stream.queue.droppedCount() == 0 || keyframes.Load() == 0 {
		t.Fatalf("dropped %d, keyframes asked %d", stream.queue.droppedCount(), keyframes.Load())
	}
	droppedAfterBurst := stream.queue.droppedCount()
	// The Mac answers with a keyframe and goes on: pictures show again.
	time.Sleep(450 * time.Millisecond)
	push(stream, clip, 24, 30)
	waitFor(t, "pictures after the keyframe", 5*time.Second, func() bool { return presenter.count() >= 3 })

	// A screen that takes 30 ms a picture while pictures come every 5 ms falls behind steadily.
	before := stream.queue.droppedCount()
	for index := 30; index < 90; index++ {
		stream.VideoAccessUnit(clipUnit(clip, index))
		time.Sleep(5 * time.Millisecond)
	}
	if stream.queue.droppedCount() <= before {
		t.Fatal("a decoder that is behind must shed pictures")
	}
	if stats := stream.stats(); stats.Dropped < droppedAfterBurst {
		t.Fatal("the drops are in the stats")
	}
	// And when the load goes, the stream is back after the next keyframe.
	time.Sleep(450 * time.Millisecond)
	count := presenter.count()
	push(stream, clip, 96, 100)
	waitFor(t, "pictures after the overload", 5*time.Second, func() bool { return presenter.count() > count })
}

// A discontinuity flag from the core means units were lost: the decoder starts over at the next keyframe.
func TestStreamHandlesDiscontinuity(t *testing.T) {
	clip := builtinClip(t)
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	presenter := &countingPresenter{}
	var keyframes atomic.Int32
	stream := newVideoStream(quiet, plan, "", 1, presenter, func() { keyframes.Add(1) })
	defer stream.close()
	stream.VideoFormat(clipFormat(clip))
	push(stream, clip, 0, 4)
	waitFor(t, "4 pictures", 5*time.Second, func() bool { return presenter.count() >= 4 })
	gap := clipUnit(clip, 6)
	gap.Discontinuity = true
	stream.VideoAccessUnit(gap)
	waitFor(t, "a keyframe request", 5*time.Second, func() bool { return keyframes.Load() > 0 })
	time.Sleep(100 * time.Millisecond)
	if presenter.count() != 4 {
		t.Fatalf("%d pictures: the one after the gap cannot be decoded", presenter.count())
	}
	stream.VideoAccessUnit(clipUnit(clip, 12))
	waitFor(t, "the keyframe", 5*time.Second, func() bool { return presenter.count() == 5 })
}

// Units and events that arrive after close change nothing and do not block.
func TestStreamIgnoresTheCoreAfterClose(t *testing.T) {
	clip := builtinClip(t)
	before := videoLive()
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	stream := newVideoStream(quiet, plan, "", 1, &countingPresenter{}, nil)
	stream.VideoFormat(clipFormat(clip))
	stream.VideoAccessUnit(clipUnit(clip, 0))
	stream.close()
	done := make(chan struct{})
	go func() {
		for index := range 100 {
			stream.VideoAccessUnit(clipUnit(clip, index))
			stream.StreamStopped(nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the core blocked on a closed stream")
	}
	if after := videoLive(); after != before {
		t.Fatalf("%+v, before %+v", after, before)
	}
}

// Bad data is never the GPU decoder's fault, one I/O error may be a frame the driver choked on,
// and anything else means it cannot decode here.
func TestGPUDecoderFailureClassification(t *testing.T) {
	const (
		eio     = -5
		enomem  = -12
		einval  = -22
		invalid = -1094995529 // AVERROR_INVALIDDATA, FFERRTAG('I','N','D','A') negated
	)
	failures := 0
	for range 5 {
		if gpuFailureFatal(invalid, &failures) {
			t.Fatal("bad data is not fatal")
		}
	}
	if failures != 0 {
		t.Fatal("bad data does not count")
	}
	if gpuFailureFatal(eio, &failures) || gpuFailureFatal(eio, &failures) {
		t.Fatal("two I/O errors may be one bad frame")
	}
	if !gpuFailureFatal(eio, &failures) {
		t.Fatal("three I/O errors in a row are a broken decoder")
	}
	for _, code := range []int{enomem, einval} {
		failures = 0
		if !gpuFailureFatal(code, &failures) {
			t.Fatalf("error %d is final", code)
		}
	}
}

// An app made without a logger (as tests and tools make one) still probes for decoders.
func TestVideoCapabilitiesNeedNoLogger(t *testing.T) {
	a := &app{}
	first := a.videoCapabilities()
	if second := a.videoCapabilities(); second != first {
		t.Fatal("the probe runs once")
	}
	plan := planVideo(videoAuto, first, true)
	if plan.enabled() != first.any() {
		t.Fatalf("plan %v for capabilities %+v", plan.enabled(), first)
	}
}

// A stream that goes quiet is noticed by the time since its last unit.
func TestStreamKnowsHowLongItHasBeenQuiet(t *testing.T) {
	clip := builtinClip(t)
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	stream := newVideoStream(quiet, plan, "", 1, &countingPresenter{}, nil)
	defer stream.close()
	if stream.quietFor(time.Now()) != 0 {
		t.Fatal("a stream that has not begun is not quiet")
	}
	stream.VideoAccessUnit(clipUnit(clip, 0))
	if q := stream.quietFor(time.Now().Add(7 * time.Second)); q < 6*time.Second || q > 8*time.Second {
		t.Fatalf("quiet for %v", q)
	}
	stream.VideoAccessUnit(clipUnit(clip, 1))
	if stream.quietFor(time.Now()) > time.Second {
		t.Fatal("a unit ends the quiet")
	}
}

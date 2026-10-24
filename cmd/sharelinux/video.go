//go:build linux

package main

/*
#cgo pkg-config: libavcodec libavutil libswscale libva gtk4
#include <stdlib.h>
#include "video.h"
#include "screen.h"
#include "wayland.h"
#include "hdroutput.h"
*/
import "C"

import (
	_ "embed"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
	E "github.com/sagernet/sing/common/exceptions"
)

// hdrProbeStream is a 128x128 HEVC 4:4:4 10-bit picture, one keyframe in Annex B with its parameter
// sets: what a machine has to decode to show HDR video. The Swift apps probe the same way, with a
// tiny stream of the profile.
//
//go:embed hdr_probe.hevc
var hdrProbeStream []byte

// chroma444ProbeStream is the same for HEVC 4:4:4 8-bit, the profile of standard range video at full
// chroma resolution.
//
//go:embed chroma444_probe.hevc
var chroma444ProbeStream []byte

func init() {
	videoProbe = probeVideo
	hdrEnvironment = probeHDREnvironment
	hdrOutputProbe = probeWindowOutput
}

// probeVideo asks what decoders this machine has.
func probeVideo(device string) videoCaps {
	var probe C.OssVideoProbe
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))
	C.oss_video_probe(&probe, cDevice)
	C.oss_video_probe_hdr(&probe, cDevice, (*C.guint8)(unsafe.Pointer(&hdrProbeStream[0])), C.gsize(len(hdrProbeStream)))
	C.oss_video_probe_444(&probe, cDevice, (*C.guint8)(unsafe.Pointer(&chroma444ProbeStream[0])), C.gsize(len(chroma444ProbeStream)))
	caps := videoCaps{
		HEVCVAAPI:    probe.vaapi[C.OSS_VIDEO_HEVC] != 0,
		HEVCSoftware: probe.software[C.OSS_VIDEO_HEVC] != 0,
		H264VAAPI:    probe.vaapi[C.OSS_VIDEO_H264] != 0,
		H264Software: probe.software[C.OSS_VIDEO_H264] != 0,

		HEVC444x10VAAPI:    probe.vaapi_444_10 != 0,
		HEVC444x10Software: probe.software_444_10 != 0,
		HEVC444x8VAAPI:     probe.vaapi_444_8 != 0,
		HEVC444x8Software:  probe.software_444_8 != 0,
		Device:             C.GoString(&probe.device[0]),
		Driver:             C.GoString(&probe.driver[0]),
		Error:              C.GoString(&probe.error[0]),
	}
	return caps
}

// liveVideo is what the video code holds right now, process wide. After a clean
// teardown every field is zero; the tests check it.
type liveVideo struct {
	Decoders, Frames, AVFrames, FDs, Textures int
}

func videoLive() liveVideo {
	var live C.OssVideoLive
	C.oss_video_live(&live)
	return liveVideo{int(live.decoders), int(live.frames), int(live.av_frames), int(live.fds), int(live.textures)}
}

// gpuFailureFatal says whether an error code of libavcodec (a negative errno, or AVERROR_INVALIDDATA)
// from the GPU decoder means it cannot work. failures counts the I/O errors in a row.
func gpuFailureFatal(avError int, failures *int) bool {
	count := C.int(*failures)
	fatal := C.oss_hardware_failure_is_fatal(C.int(avError), &count) != 0
	*failures = int(count)
	return fatal
}

// dmabufNV12Supported says whether the default display imports NV12 DMA-BUFs at all.
func dmabufNV12Supported() bool {
	return C.oss_dmabuf_nv12_supported(C.gdk_display_get_default()) != 0
}

// breakDMABufForTest makes every DMA-BUF import fail inside GDK's builder, to test the failure path.
func breakDMABufForTest(broken bool) {
	C.oss_video_test_break_dmabuf(boolToC(broken))
}

// failDecodersForTest makes the decoders of these kinds answer "unusable" to every unit, so a test can
// break a decoder in the middle of a stream. Call it with no kinds to mend them.
func failDecodersForTest(kinds ...decoderKind) {
	mask := 0
	for _, kind := range kinds {
		if kind == decoderVAAPI {
			mask |= 1 << C.OSS_DECODER_VAAPI
		} else {
			mask |= 1 << C.OSS_DECODER_SOFTWARE
		}
	}
	C.oss_video_test_fail_decoders(C.int(mask))
}

// hdrGPUForTest chooses whether HDR pictures of the GPU go through its video processor to P010 DMA-BUFs.
func hdrGPUForTest(mode int) {
	C.oss_video_test_hdr_gpu(C.int(mode))
}

// videoPoolForTest is how many output surfaces of the GPU's video processor are kept for reuse.
func videoPoolForTest() int {
	return int(C.oss_video_test_pool())
}

// videoPoolOrphansForTest is how many output surfaces a texture still holds after the pool let them go.
func videoPoolOrphansForTest() int {
	return int(C.oss_video_test_pool_orphans())
}

// fullAnswersForTest makes the next sends of a packet to libavcodec answer "full" without taking it.
func fullAnswersForTest(sends int) {
	C.oss_video_test_eagain(C.int(sends))
}

// forceSwscaleForTest sends software pictures through swscale even where GTK takes planar YUV.
func forceSwscaleForTest(forced bool) {
	C.oss_video_test_force_swscale(boolToC(forced))
}

// claimVideoProcessorForTest makes the decoder own the GPU video processor slot under a made-up display,
// and videoProcessorOwnerForTest returns that display, 0 when the slot is free.
func claimVideoProcessorForTest(d *decoder, display uintptr) {
	C.oss_video_test_vpp_claim(d.c, C.gsize(display))
}

func videoProcessorOwnerForTest() uintptr {
	return uintptr(C.oss_video_test_vpp_owner())
}

// textureForTest makes the picture's texture as the screen does, cropped cropLeft and cropTop more
// at the left and top, and returns its pixel (b, g, r, a) at x, y and whether its color state is the
// one for RGB made from expect.
func (f *videoFrame) textureForTest(cropLeft, cropTop, x, y int, expect media.Color) (pixel [4]byte, matches, ok bool) {
	color := C.OssColor{
		primaries: C.int(expect.Primaries), transfer: C.int(expect.Transfer), matrix: C.int(expect.Matrix),
		full_range: boolToC(expect.FullRange), bit_depth: C.int(expect.BitDepth), chroma_format: C.int(expect.ChromaFormat),
	}
	var match C.gboolean
	ok = C.oss_frame_test_texture(f.c, C.int(cropLeft), C.int(cropTop), C.int(x), C.int(y), color, (*C.guint8)(unsafe.Pointer(&pixel[0])), &match) != 0
	return pixel, match != 0, ok
}

func cColor(color media.Color) C.OssColor {
	return C.OssColor{
		primaries: C.int(color.Primaries), transfer: C.int(color.Transfer), matrix: C.int(color.Matrix),
		full_range: boolToC(color.FullRange), bit_depth: C.int(color.BitDepth), chroma_format: C.int(color.ChromaFormat),
	}
}

// colorSpace says which state of a stream's colors a test asks for.
type colorSpace int

const (
	// spaceYUV is the state of 4:2:0 planes, where values GTK has no name for become BT.709.
	spaceYUV colorSpace = iota
	// spaceYUV444 is the state of the 4:4:4 10-bit planes of HDR, which is never guessed.
	spaceYUV444
	// spaceRGB is the state of the RGB converted from either.
	spaceRGB
)

// colorStateForTest says what GTK is told about a stream's colors: the code points (primaries, transfer,
// matrix, full range) of the state in space, and whether it is GTK's own Rec. 2100 PQ. ok is false where
// GTK has no state for the stream.
func colorStateForTest(color media.Color, space colorSpace) (cicp [4]int, pq, ok bool) {
	var state *C.GdkColorState
	switch space {
	case spaceYUV:
		state = C.oss_color_state(cColor(color))
	case spaceYUV444:
		state = C.oss_yuv_color_state(cColor(color))
	default:
		state = C.oss_rgb_color_state(cColor(color))
	}
	if state == nil {
		return cicp, false, false
	}
	var values [4]C.int
	if C.oss_color_state_cicp(state, &values[0]) == 0 {
		return cicp, false, false
	}
	for index := range cicp {
		cicp[index] = int(values[index])
	}
	return cicp, C.oss_color_state_is_rec2100_pq(state) != 0, true
}

// gpuTextureForTest makes the picture's texture as the screen does for a GPU frame, as a DMA-BUF where the display
// takes one, and returns its pixel (b, g, r, a) at x, y, the way it went, and whether its color state is the one for
// RGB made from expect.
func (f *videoFrame) gpuTextureForTest(x, y int, expect media.Color) (pixel [4]byte, output outputKind, matches, ok bool) {
	var match C.gboolean
	var out C.OssOutput
	ok = C.oss_frame_test_texture_on(f.c, 0, 0, C.int(x), C.int(y), cColor(expect), (*C.guint8)(unsafe.Pointer(&pixel[0])), &match, 1, &out) != 0
	switch out {
	case C.OSS_OUTPUT_DMABUF:
		output = outputDMABuf
	case C.OSS_OUTPUT_COPY:
		output = outputCopy
	case C.OSS_OUTPUT_MEMORY:
		output = outputMemory
	}
	return pixel, output, match != 0, ok
}

// decodeResult is what feeding a unit to the decoder came to.
type decodeResult int

const (
	decodeOK decodeResult = iota
	// decodeBadData means this unit was lost: ask for a keyframe.
	decodeBadData
	// decodeFatal means the decoder cannot work at all: use another.
	decodeFatal
)

// decoder is a libavcodec decoder.
type decoder struct {
	c    *C.OssVideoDecoder
	kind decoderKind
}

type decoderOptions struct {
	Codec   media.Codec
	Kind    decoderKind
	Device  string
	Threads int
}

// openDecoder makes a decoder for a stream format.
func openDecoder(options decoderOptions, format media.VideoFormat) (*decoder, error) {
	var sets []byte
	lengths := make([]C.int, 0, len(format.ParameterSets))
	for _, set := range format.ParameterSets {
		sets = append(sets, set...)
		lengths = append(lengths, C.int(len(set)))
	}
	if len(sets) == 0 {
		return nil, E.New("the stream came without parameter sets")
	}
	codec := C.OssVideoCodec(C.OSS_VIDEO_HEVC)
	if options.Codec == media.CodecH264 {
		codec = C.OSS_VIDEO_H264
	}
	kind := C.OssDecoderKind(C.OSS_DECODER_SOFTWARE)
	if options.Kind == decoderVAAPI {
		kind = C.OSS_DECODER_VAAPI
	}
	cDevice := C.CString(options.Device)
	defer C.free(unsafe.Pointer(cDevice))
	cSets := C.CBytes(sets)
	defer C.free(cSets)
	color := C.OssColor{
		primaries:     C.int(format.Color.Primaries),
		transfer:      C.int(format.Color.Transfer),
		matrix:        C.int(format.Color.Matrix),
		full_range:    boolToC(format.Color.FullRange),
		bit_depth:     C.int(format.Color.BitDepth),
		chroma_format: C.int(format.Color.ChromaFormat),
	}
	var message [200]C.char
	handle := C.oss_video_open(codec, kind, cDevice, C.int(options.Threads), (*C.guint8)(cSets), (*C.int)(unsafe.Pointer(&lengths[0])),
		C.int(len(lengths)), color, &message[0], C.int(len(message)))
	if handle == nil {
		return nil, E.New(C.GoString(&message[0]))
	}
	return &decoder{c: handle, kind: options.Kind}, nil
}

func boolToC(value bool) C.gboolean {
	if value {
		return 1
	}
	return 0
}

// decode feeds one access unit. It returns how many pictures are ready.
func (d *decoder) decode(data []byte, sync bool, age time.Duration) (int, decodeResult) {
	if len(data) == 0 || d.c == nil {
		return 0, decodeBadData
	}
	n := int(C.oss_video_decode(d.c, (*C.guint8)(unsafe.Pointer(&data[0])), C.gsize(len(data)), boolToC(sync), C.gint64(age.Microseconds())))
	switch {
	case n == C.OSS_ERR_FATAL:
		return 0, decodeFatal
	case n < 0:
		return 0, decodeBadData
	}
	return n, decodeOK
}

// next takes the next decoded picture.
func (d *decoder) next() *videoFrame {
	if d.c == nil {
		return nil
	}
	c := C.oss_video_next(d.c)
	if c == nil {
		return nil
	}
	frame := &videoFrame{c: c}
	var info C.OssFrameInfo
	C.oss_frame_info(c, &info)
	frame.Width, frame.Height = int(info.width), int(info.height)
	frame.TexWidth, frame.TexHeight = int(info.tex_width), int(info.tex_height)
	frame.Hardware = info.hardware != 0
	frame.DecodeTime = time.Duration(info.decode_us) * time.Microsecond
	frame.UnitBytes = int(info.unit_bytes)
	frame.info = info
	return frame
}

// flush drops the pictures in flight; the next unit has to be a keyframe.
func (d *decoder) flush() {
	if d.c == nil {
		return
	}
	C.oss_video_flush(d.c)
}

func (d *decoder) stats() (units, frames, errors uint64, total time.Duration, last, max time.Duration) {
	var stats C.OssVideoStats
	if d.c == nil {
		return 0, 0, 0, 0, 0, 0
	}
	C.oss_video_stats(d.c, &stats)
	return uint64(stats.units), uint64(stats.frames), uint64(stats.errors), time.Duration(stats.decode_us_total) * time.Microsecond,
		time.Duration(stats.decode_us_last) * time.Microsecond, time.Duration(stats.decode_us_max) * time.Microsecond
}

func (d *decoder) close() {
	if d.c != nil {
		C.oss_video_close(d.c)
		d.c = nil
	}
}

// videoFrame is a decoded picture, in memory or on the GPU.
type videoFrame struct {
	c             *C.OssFrame
	info          C.OssFrameInfo
	Width, Height int
	TexWidth      int
	TexHeight     int
	Hardware      bool
	DecodeTime    time.Duration
	// UnitBytes is the size of the access unit the picture was decoded from.
	UnitBytes int
}

// lumaMean is the average brightness of a software picture, -1 for a GPU one.
func (f *videoFrame) lumaMean() float64 {
	return float64(C.oss_frame_luma_mean(f.c))
}

func (f *videoFrame) free() {
	if f.c != nil {
		C.oss_frame_free(f.c)
		f.c = nil
	}
}

// presenter shows decoded pictures. It takes the frame and frees it.
type presenter interface {
	// present returns how the picture got to the screen, outputNone when it did not.
	present(frame *videoFrame) outputKind
}

// videoEventKind is what a stream tells its session.
type videoEventKind int

const (
	// videoFirstPicture: pictures are flowing, show them.
	videoFirstPicture videoEventKind = iota
	// videoStopped: the stream ended; Err says how.
	videoStopped
	// videoFailed: it cannot be decoded here; the stream should be stopped for good.
	videoFailed
	// videoDecoderChanged: a decoder failed and the next one took over.
	videoDecoderChanged
	// videoSoundFailed: the sound cannot be played (no sound server, or it broke); the picture goes on.
	videoSoundFailed
)

type videoEvent struct {
	Kind videoEventKind
	Err  error
	Text string
	// Without444 is set on a failure that asking for 4:2:0 instead may cure: the stream was 4:4:4
	// and no decoder took it.
	Without444 bool
}

// videoStream is one run of the media stream: the sink the core delivers into, a
// queue, and the goroutine that decodes and presents.
type videoStream struct {
	logger *slog.Logger
	plan   videoPlan
	// hdr and chroma444 are whether the offer asked for HDR and for 4:4:4 8-bit; they never change, so
	// the core's goroutine may read them.
	hdr       bool
	chroma444 bool
	// sound is whether the offer asked for the Mac's sound.
	sound   bool
	options decoderOptions
	queue   *unitQueue
	out     presenter
	// keyframe asks the Mac for a keyframe.
	keyframe func()
	events   chan videoEvent
	done     chan struct{}

	changes atomic.Uint64
	tracker changeTracker
	// lastUnit is when the core last delivered an access unit, in Unix nanoseconds.
	lastUnit atomic.Int64

	// The Mac's sound, if the offer asked for it: one player, made when the Mac announces the
	// sound and gone with the stream.
	audioAccess sync.Mutex
	audio       *audioPlayer
	audioClosed bool
	muted       atomic.Bool

	access  sync.Mutex
	gate    keyframeGate
	closed  bool
	format  *media.VideoFormat
	dec     *decoder
	started bool
	snap    videoStats
	last    time.Time
}

// queueDepth is how many access units may wait for the decoder. At 60 Hz that is about
// a tenth of a second; a decoder further behind than this sheds the backlog.
const queueDepth = 6

func newVideoStream(logger *slog.Logger, plan videoPlan, device string, threads int, out presenter, keyframe func()) *videoStream {
	s := &videoStream{
		logger:    logger,
		plan:      plan,
		hdr:       plan.HDR,
		chroma444: plan.Chroma444,
		sound:     plan.Sound,
		options:   decoderOptions{Codec: plan.Codec, Device: device, Threads: threads},
		queue:     newUnitQueue(queueDepth),
		out:       out,
		keyframe:  keyframe,
		events:    make(chan videoEvent, 16),
		done:      make(chan struct{}),
		gate:      keyframeGate{interval: 400 * time.Millisecond},
	}
	s.snap.Codec = plan.Codec
	go s.run()
	return s
}

// event never blocks the decoder: a session that stopped listening loses the event.
func (s *videoStream) event(event videoEvent) {
	select {
	case s.events <- event:
	default:
	}
}

func (s *videoStream) VideoFormat(format media.VideoFormat) {
	if format.Bands != 1 {
		s.event(videoEvent{Kind: videoFailed, Text: "the stream is cut into " + strconv.Itoa(format.Bands) + " strips"})
		return
	}
	if !streamSupported(format.Color, s.hdr, s.chroma444) {
		s.event(videoEvent{Kind: videoFailed, Text: "the stream is " + strconv.Itoa(format.Color.BitDepth) + " bit, chroma " + strconv.Itoa(format.Color.ChromaFormat)})
		return
	}
	s.logger.Info("video format", "codec", codecName(format.Codec), "width", format.Width, "height", format.Height,
		"bit-depth", format.Color.BitDepth, "chroma", format.Color.ChromaFormat,
		"primaries", format.Color.Primaries, "transfer", format.Color.Transfer, "matrix", format.Color.Matrix, "full-range", format.Color.FullRange,
		"hdr", format.Color.HDR())
	s.access.Lock()
	s.snap.Color = format.Color
	s.access.Unlock()
	copied := format
	copied.ParameterSets = make([][]byte, len(format.ParameterSets))
	for index, set := range format.ParameterSets {
		copied.ParameterSets[index] = append([]byte(nil), set...)
	}
	s.queue.push(videoItem{Format: &copied})
}

func (s *videoStream) VideoAccessUnit(unit media.VideoAccessUnit) {
	if unit.Bands != 1 || len(unit.Data) == 0 {
		return
	}
	s.lastUnit.Store(time.Now().UnixNano())
	// The core builds each unit's data afresh and does not touch it again, so it is queued as it is.
	if s.queue.push(videoItem{Unit: &unit, Queued: time.Now()}) {
		s.askKeyframe()
	}
}

// AudioFormat is called by the core when the Mac announces its sound. It starts a player, which opens
// the decoder and the sound device on its own goroutine, so the network goroutine is not held up.
func (s *videoStream) AudioFormat(format media.AudioFormat) {
	if !s.sound {
		return
	}
	s.logger.Info("sound format", "format", "AAC-ELD", "rate", format.SampleRate, "channels", format.Channels, "frames", format.FramesPerPacket)
	player := openSoundPlayer(format, func(err error) {
		s.logger.Warn("sound", "state", "stopped", "error", err)
		s.event(videoEvent{Kind: videoSoundFailed, Text: err.Error()})
	})
	s.audioAccess.Lock()
	old := s.audio
	closed := s.audioClosed
	if !closed {
		s.audio = player
	}
	s.audioAccess.Unlock()
	// Set after the player is published, so a mute that came in meanwhile is not lost: setMuted stores
	// the flag before it looks for the player.
	player.setMuted(s.muted.Load())
	// Neither is joined here: a sound server that has stopped answering must not hold the network goroutine.
	if old != nil {
		go old.close()
	}
	if closed {
		go player.close()
	}
}

// AudioPacket queues a packet of sound for the player. It never waits.
func (s *videoStream) AudioPacket(packet media.AudioPacket) {
	s.audioAccess.Lock()
	player := s.audio
	s.audioAccess.Unlock()
	if player != nil {
		player.push(audioUnit{Data: packet.Data, Lost: packet.Lost})
	}
}

// setMuted mutes or unmutes the Mac's sound. The stream keeps running: only what is played changes.
func (s *videoStream) setMuted(muted bool) {
	s.muted.Store(muted)
	s.audioAccess.Lock()
	player := s.audio
	s.audioAccess.Unlock()
	if player != nil {
		player.setMuted(muted)
	}
}

// soundStats are the player's counters, and whether there is a player at all.
func (s *videoStream) soundStats() (stats soundStats, ok bool) {
	s.audioAccess.Lock()
	player := s.audio
	s.audioAccess.Unlock()
	if player == nil {
		return soundStats{}, false
	}
	return soundStats{Counters: player.stats(), LastError: player.lastDecodeError(), Device: player.deviceLatency(), Muted: s.muted.Load()}, true
}

// closeAudio stops the player. A player announced later is closed on arrival.
func (s *videoStream) closeAudio() {
	s.audioAccess.Lock()
	s.audioClosed = true
	player := s.audio
	s.audio = nil
	s.audioAccess.Unlock()
	if player != nil {
		// A sound server that has stopped answering must not hold the window: the player is joined
		// on its own goroutine.
		go player.close()
	}
}

func (s *videoStream) StreamStopped(err error) {
	s.event(videoEvent{Kind: videoStopped, Err: err})
}

func (s *videoStream) askKeyframe() {
	s.access.Lock()
	allow := s.gate.allow(time.Now())
	s.access.Unlock()
	if allow && s.keyframe != nil {
		s.keyframe()
	}
}

// run is the decoder goroutine.
func (s *videoStream) run() {
	defer close(s.done)
	for {
		item, ok := s.queue.pop()
		if !ok {
			break
		}
		if item.Format != nil {
			s.reopen(item.Format)
			continue
		}
		s.decodeUnit(item)
	}
	s.access.Lock()
	dec := s.dec
	s.dec = nil
	s.access.Unlock()
	if dec != nil {
		dec.close()
	}
}

// reopen starts a decoder for a format, the first one of the plan that works.
func (s *videoStream) reopen(format *media.VideoFormat) {
	s.access.Lock()
	old := s.dec
	s.dec = nil
	s.format = format
	s.access.Unlock()
	if old != nil {
		old.close()
	}
	for {
		kinds := s.plan.kindsFor(format.Color)
		if len(kinds) == 0 {
			break
		}
		options := s.options
		options.Kind = kinds[0]
		dec, err := openDecoder(options, *format)
		if err == nil {
			s.access.Lock()
			s.dec = dec
			s.snap.Kind = dec.kind
			s.access.Unlock()
			s.logger.Info("video decoder", "kind", dec.kind.String(), "codec", codecName(format.Codec), "bit-depth", format.Color.BitDepth, "chroma", format.Color.ChromaFormat)
			return
		}
		s.logger.Warn("video decoder", "kind", options.Kind.String(), "error", err)
		s.plan = s.plan.withoutKind(options.Kind)
		s.event(videoEvent{Kind: videoDecoderChanged, Text: err.Error()})
	}
	reason := s.plan.Reason
	if reason == "" {
		reason = "no decoder takes the HDR stream (HEVC 4:4:4 10-bit)"
		if is444EightBit(format.Color) {
			reason = "no decoder takes the full colour stream (HEVC 4:4:4 8-bit)"
		}
	}
	s.event(videoEvent{Kind: videoFailed, Text: reason, Without444: is444EightBit(format.Color)})
}

func (s *videoStream) decodeUnit(item videoItem) {
	s.access.Lock()
	dec := s.dec
	s.access.Unlock()
	if dec == nil {
		return
	}
	unit := item.Unit
	if unit.Discontinuity {
		// Something between the Mac and here was lost: what follows cannot be decoded
		// until a keyframe starts over.
		dec.flush()
	}
	age := time.Since(unit.ReceivedAt)
	if unit.ReceivedAt.IsZero() {
		age = time.Since(item.Queued)
	}
	frames, result := dec.decode(unit.Data, unit.Sync, age)
	switch result {
	case decodeBadData:
		s.queue.waitForSync()
		s.askKeyframe()
	case decodeFatal:
		s.logger.Warn("video decoder failed", "kind", dec.kind.String())
		s.plan = s.plan.withoutKind(dec.kind)
		s.access.Lock()
		format := s.format
		s.access.Unlock()
		s.event(videoEvent{Kind: videoDecoderChanged, Text: dec.kind.String() + " decoding failed"})
		if format != nil {
			// reopen closes dec: nothing below may touch it. The next decoder starts at a keyframe.
			s.reopen(format)
			s.queue.waitForSync()
			s.askKeyframe()
		}
		return
	}
	for ; frames > 0; frames-- {
		frame := dec.next()
		if frame == nil {
			break
		}
		s.show(frame)
	}
	s.publish(dec)
}

func (s *videoStream) show(frame *videoFrame) {
	if s.tracker.changed(frame.UnitBytes) {
		s.changes.Add(1)
	}
	output := s.out.present(frame)
	s.access.Lock()
	first := !s.started && output != outputNone
	if first {
		s.started = true
	}
	if output != outputNone {
		s.snap.Output = output
		s.snap.Width, s.snap.Height = frame.Width, frame.Height
		s.last = time.Now()
	}
	s.access.Unlock()
	if first {
		s.event(videoEvent{Kind: videoFirstPicture})
	}
}

// publish copies the decoder's counters where stats() can read them.
func (s *videoStream) publish(dec *decoder) {
	units, frames, errors, total, last, max := dec.stats()
	s.access.Lock()
	defer s.access.Unlock()
	s.snap.Units, s.snap.Frames, s.snap.Errors = units, frames, errors
	s.snap.DecodeLast, s.snap.DecodeMax = last, max
	if frames > 0 {
		s.snap.DecodeAvg = total / time.Duration(frames)
	}
}

// stats are the stream's counters; the drops that happen in the widget are added by the caller.
func (s *videoStream) stats() videoStats {
	s.access.Lock()
	snap := s.snap
	s.access.Unlock()
	snap.Dropped = s.queue.droppedCount()
	snap.Sound, snap.SoundOn = s.soundStats()
	return snap
}

// quietFor reports how long the core has delivered no access unit since the stream started
// (or since the last one).
func (s *videoStream) quietFor(now time.Time) time.Duration {
	last := s.lastUnit.Load()
	if last == 0 {
		return 0
	}
	return now.Sub(time.Unix(0, last))
}

// idle reports how long no picture has been presented.
func (s *videoStream) idle(now time.Time) time.Duration {
	s.access.Lock()
	defer s.access.Unlock()
	if s.last.IsZero() {
		return 0
	}
	return now.Sub(s.last)
}

// close stops the goroutine and frees the decoder and everything it holds. It is
// safe to call twice and to call while the core still delivers.
func (s *videoStream) close() {
	s.access.Lock()
	closed := s.closed
	s.closed = true
	s.access.Unlock()
	if closed {
		return
	}
	s.queue.close()
	<-s.done
	s.closeAudio()
}

// probeHDREnvironment asks the open display what it offers for HDR. It is called on the GTK thread.
func probeHDREnvironment() hdrEnv {
	bits := int(C.oss_lock_caps())
	return hdrEnv{
		Wayland:         bits&C.OSS_CAP_WAYLAND != 0,
		ColorManager:    bits&C.OSS_CAP_COLOR_MANAGER != 0,
		ColorManagement: colorManagementActive(os.Getenv("GDK_DEBUG"), int(C.gtk_get_major_version()), int(C.gtk_get_minor_version())),
	}
}

func outputColorFrom(raw C.OssOutputColor) outputColor {
	return outputColor{
		Known:        raw.known != 0,
		Transfer:     uint32(raw.transfer),
		MaxLum:       uint32(raw.max_lum),
		ReferenceLum: uint32(raw.reference_lum),
		TargetMaxLum: uint32(raw.target_max_lum),
	}
}

// probeWindowOutput asks the compositor what the screen the window is on shows. It is called on
// the GTK thread; a screen it cannot describe is unknown, which counts as standard range.
func probeWindowOutput() outputColor {
	var raw C.OssOutputColor
	if C.oss_wl_window_output(&raw) == 0 {
		return outputColor{}
	}
	return outputColorFrom(raw)
}

// probeScreens describes every screen on a connection of its own, before GTK starts. ok is false
// when there is no Wayland display or the compositor has no color manager.
func probeScreens() (screens []outputColor, ok bool) {
	var raw [16]C.OssOutputColor
	count := int(C.oss_wl_probe_outputs(&raw[0], C.int(len(raw))))
	if count < 0 {
		return nil, false
	}
	for index := range count {
		screens = append(screens, outputColorFrom(raw[index]))
	}
	return screens, true
}

// prepareHDR turns GTK's color management and the GL renderer on before GTK starts, but only when
// HDR can really be shown: GTK reads GDK_DEBUG once, and without the flag it tells the compositor
// nothing about the colors of its surfaces. The variables go back to what they were once GTK has
// read them (see releaseGTKEnv).
func prepareHDR(a *app) {
	if !a.hdrWanted() || !waylandSession(os.Getenv) {
		return
	}
	screens, colorManager := probeScreens()
	if !hdrStartup(true, a.videoCapabilities(), a.videoMode(), true, colorManager, screens) {
		if a.logger != nil {
			a.logger.Info("hdr", "state", "gtk left as it is", "color-manager", colorManager, "screens", len(screens))
		}
		return
	}
	a.gtkEnv = hdrEnvChanges(os.LookupEnv, int(C.gtk_get_major_version()), int(C.gtk_get_minor_version()))
	applyEnv(a.gtkEnv, os.Setenv)
}

// releaseGTKEnv takes out of the environment what prepareHDR put there for GTK.
func releaseGTKEnv(a *app) {
	restoreEnv(a.gtkEnv, os.LookupEnv, os.Setenv, os.Unsetenv)
	a.gtkEnv = nil
}

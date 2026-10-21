//go:build linux

package main

/*
#cgo pkg-config: libavcodec libavutil libswscale libva gtk4
#include <stdlib.h>
#include "video.h"
#include "screen.h"
*/
import "C"

import (
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
	E "github.com/sagernet/sing/common/exceptions"
)

func init() {
	videoProbe = probeVideo
}

// probeVideo asks what decoders this machine has.
func probeVideo(device string) videoCaps {
	var probe C.OssVideoProbe
	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))
	C.oss_video_probe(&probe, cDevice)
	caps := videoCaps{
		HEVCVAAPI:    probe.vaapi[C.OSS_VIDEO_HEVC] != 0,
		HEVCSoftware: probe.software[C.OSS_VIDEO_HEVC] != 0,
		H264VAAPI:    probe.vaapi[C.OSS_VIDEO_H264] != 0,
		H264Software: probe.software[C.OSS_VIDEO_H264] != 0,
		Device:       C.GoString(&probe.device[0]),
		Driver:       C.GoString(&probe.driver[0]),
		Error:        C.GoString(&probe.error[0]),
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
	if len(data) == 0 {
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
	C.oss_video_flush(d.c)
}

func (d *decoder) stats() (units, frames, errors uint64, total time.Duration, last, max time.Duration) {
	var stats C.OssVideoStats
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
)

type videoEvent struct {
	Kind videoEventKind
	Err  error
	Text string
}

// videoStream is one run of the media stream: the sink the core delivers into, a
// queue, and the goroutine that decodes and presents.
type videoStream struct {
	logger  *slog.Logger
	plan    videoPlan
	options decoderOptions
	queue   *unitQueue
	out     presenter
	// keyframe asks the Mac for a keyframe.
	keyframe func()
	events   chan videoEvent
	done     chan struct{}

	changes atomic.Uint64
	tracker changeTracker

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
		logger:   logger,
		plan:     plan,
		options:  decoderOptions{Codec: plan.Codec, Device: device, Threads: threads},
		queue:    newUnitQueue(queueDepth),
		out:      out,
		keyframe: keyframe,
		events:   make(chan videoEvent, 16),
		done:     make(chan struct{}),
		gate:     keyframeGate{interval: 400 * time.Millisecond},
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
	if format.Color.BitDepth != 8 || format.Color.ChromaFormat != 1 {
		s.event(videoEvent{Kind: videoFailed, Text: "the stream is " + strconv.Itoa(format.Color.BitDepth) + " bit, chroma " + strconv.Itoa(format.Color.ChromaFormat)})
		return
	}
	s.logger.Info("video format", "codec", codecName(format.Codec), "width", format.Width, "height", format.Height,
		"primaries", format.Color.Primaries, "transfer", format.Color.Transfer, "matrix", format.Color.Matrix, "full-range", format.Color.FullRange)
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
	// The core builds each unit's data afresh and does not touch it again, so it is queued as it is.
	if s.queue.push(videoItem{Unit: &unit, Queued: time.Now()}) {
		s.askKeyframe()
	}
}

func (s *videoStream) AudioFormat(media.AudioFormat) {}

func (s *videoStream) AudioPacket(media.AudioPacket) {}

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
	for len(s.plan.Kinds) > 0 {
		options := s.options
		options.Kind = s.plan.Kinds[0]
		dec, err := openDecoder(options, *format)
		if err == nil {
			s.access.Lock()
			s.dec = dec
			s.snap.Kind = dec.kind
			s.access.Unlock()
			s.logger.Info("video decoder", "kind", dec.kind.String(), "codec", codecName(format.Codec))
			return
		}
		s.logger.Warn("video decoder", "kind", options.Kind.String(), "error", err)
		s.plan = s.plan.withoutKind(options.Kind)
		s.event(videoEvent{Kind: videoDecoderChanged, Text: err.Error()})
	}
	s.event(videoEvent{Kind: videoFailed, Text: s.plan.Reason})
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
			s.reopen(format)
			s.queue.waitForSync()
			s.askKeyframe()
		}
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
	return snap
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
}

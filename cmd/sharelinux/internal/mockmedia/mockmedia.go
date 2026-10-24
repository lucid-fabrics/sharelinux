// Package mockmedia plays a canned HEVC clip as the Mac's media stream, so the
// video path of a viewer can be tried and tested without a Mac. The clip is an
// Annex B file; the source cuts it into access units, packs them into RTP the
// way the Mac does and repeats it for as long as the viewer listens.
package mockmedia

import (
	"context"
	_ "embed"
	"encoding/binary"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
	E "github.com/sagernet/sing/common/exceptions"
)

//go:embed testdata/clip.hevc
var builtin []byte

//go:embed testdata/clip_small.hevc
var builtinSmall []byte

// Builtin is a 320x192 clip of 24 frames with a keyframe every 12, in BT.709 limited range.
func Builtin() (*Clip, error) {
	return ParseAnnexB(builtin)
}

//go:embed testdata/clip_cropped.hevc
var builtinCropped []byte

// BuiltinCropped is a clip whose picture is 318x190 inside a 320x192 coded picture, so the
// decoder has to crop.
func BuiltinCropped() (*Clip, error) {
	return ParseAnnexB(builtinCropped)
}

// BuiltinSmall is the same kind of clip at 192x128, for a stream that changes size.
func BuiltinSmall() (*Clip, error) {
	return ParseAnnexB(builtinSmall)
}

//go:embed testdata/clip_pq.hevc
var builtinPQ []byte

//go:embed testdata/clip_hlg.hevc
var builtinHLG []byte

// BuiltinPQ is a 320x192 HEVC 4:4:4 10-bit clip of 24 frames, a keyframe every 12, in BT.2020 full
// range with PQ transfer: the HDR the Mac sends. The left half is neutral gray, luma 500 of 1023;
// the right half has the same luma with chroma 300 (Cb) and 800 (Cr).
func BuiltinPQ() (*Clip, error) {
	return ParseAnnexB(builtinPQ)
}

// BuiltinHLG is BuiltinPQ with HLG transfer.
func BuiltinHLG() (*Clip, error) {
	return ParseAnnexB(builtinHLG)
}

//go:embed testdata/clip_p3.hevc
var builtinP3 []byte

// BuiltinP3PQ is BuiltinPQ as the Mac really sends it: Display P3 primaries (12), BT.709 matrix
// (1), full range, PQ transfer, HEVC 4:4:4 10-bit. The picture is the same, luma 500 with
// chroma 300 (Cb) and 800 (Cr) on the right.
func BuiltinP3PQ() (*Clip, error) {
	return ParseAnnexB(builtinP3)
}

//go:embed testdata/clip_444.hevc
var builtin444 []byte

// Builtin444 is a 320x192 HEVC 4:4:4 8-bit clip of 24 frames, a keyframe every 12, in BT.709 full
// range: the standard range picture the Mac sends at full chroma resolution, coded losslessly. The
// first 159 columns are neutral gray (128, 128, 128), the rest pure red (255, 0, 0): an edge on an
// odd column, which 4:2:0 would blur.
func Builtin444() (*Clip, error) {
	return ParseAnnexB(builtin444)
}

const (
	payloadTypeHEVC = 100
	rtpClockRate    = 24000
	maxPayload      = 1180

	nalAggregation = 48
	nalFragment    = 49
)

// Unit is one access unit of the clip: all the NAL units of one picture.
type Unit struct {
	NALs [][]byte
	// Sync is set for a picture that starts a decodable run (an IRAP picture).
	Sync bool
}

// Clip is a short stream cut into access units.
type Clip struct {
	Width, Height int
	Units         []Unit
}

func nalType(nal []byte) int {
	return int(nal[0]>>1) & 0x3f
}

func splitAnnexB(data []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+3 <= len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			if start >= 0 {
				end := i
				for end > start && data[end-1] == 0 {
					end--
				}
				nals = append(nals, data[start:end])
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(data) {
		nals = append(nals, data[start:])
	}
	return nals
}

// ParseAnnexB cuts an Annex B HEVC stream into pictures. A picture starts at
// its first slice (first_slice_segment_in_pic_flag); parameter sets and SEI
// in front of it belong to it. The first picture must be a keyframe.
func ParseAnnexB(data []byte) (*Clip, error) {
	clip := &Clip{}
	var pending [][]byte
	for _, nal := range splitAnnexB(data) {
		if len(nal) < 3 {
			continue
		}
		kind := nalType(nal)
		if kind == 33 && clip.Width == 0 {
			width, height, ok := media.HEVCPictureSize(nal)
			if !ok {
				return nil, E.New("mockmedia: unreadable sequence parameter set")
			}
			clip.Width, clip.Height = width, height
		}
		if kind < 32 && nal[2]&0x80 != 0 {
			clip.Units = append(clip.Units, Unit{NALs: pending, Sync: kind >= 16 && kind <= 23})
			pending = nil
		}
		if kind < 32 {
			last := &clip.Units[len(clip.Units)-1]
			last.NALs = append(last.NALs, nal)
			continue
		}
		pending = append(pending, nal)
	}
	if len(clip.Units) == 0 || !clip.Units[0].Sync || clip.Width == 0 {
		return nil, E.New("mockmedia: the clip has no leading keyframe or no sequence parameter set")
	}
	return clip, nil
}

// OpenVideo returns an opener for the server's media stream that plays the clip at the frame rate.
func (c *Clip) OpenVideo(frameRate int) media.ServerVideoOpener {
	return func(ctx context.Context, request media.ServerVideoRequest) (media.ServerVideoSource, error) {
		return c.newSource(request, frameRate), nil
	}
}

// Source is a playing clip.
type Source struct {
	clip      *Clip
	info      media.ServerVideoInfo
	interval  time.Duration
	step      uint32
	closed    chan struct{}
	closeOnce sync.Once
	keyframe  atomic.Bool

	access   sync.Mutex
	queue    [][]byte
	index    int
	sequence uint16
	stamp    uint32
	next     time.Time
	sent     atomic.Uint64
}

func (c *Clip) newSource(request media.ServerVideoRequest, frameRate int) *Source {
	if frameRate <= 0 {
		frameRate = 30
	}
	return &Source{
		clip: c,
		info: media.ServerVideoInfo{
			SSRC:          rand.Uint32() | 1,
			ReceiverSSRC:  request.ClientSSRC,
			Width:         c.Width,
			Height:        c.Height,
			FrameRate:     frameRate,
			PixelFormat:   media.PixelFormat420VideoRange,
			TilesPerFrame: 1,
		},
		interval: time.Second / time.Duration(frameRate),
		step:     uint32(rtpClockRate / frameRate),
		closed:   make(chan struct{}),
		sequence: uint16(rand.Uint32()),
		stamp:    rand.Uint32(),
	}
}

func (s *Source) Info() media.ServerVideoInfo { return s.info }

// Frames counts the pictures sent.
func (s *Source) Frames() uint64 { return s.sent.Load() }

func (s *Source) PacketArrival() time.Time { return time.Now() }

func (s *Source) WriteRTCP([]byte) error { return nil }

// RequestKeyFrame makes the next picture the clip's first, which is a keyframe.
func (s *Source) RequestKeyFrame() error {
	s.keyframe.Store(true)
	return nil
}

func (s *Source) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (s *Source) ReadPacket(ctx context.Context, buffer []byte) (int, bool, error) {
	for {
		s.access.Lock()
		if len(s.queue) > 0 {
			packet := s.queue[0]
			s.queue = s.queue[1:]
			s.access.Unlock()
			return copy(buffer, packet), false, nil
		}
		wait := time.Until(s.next)
		if wait <= 0 {
			s.enqueueLocked(time.Now())
			s.access.Unlock()
			continue
		}
		s.access.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return 0, false, ctx.Err()
		case <-s.closed:
			timer.Stop()
			return 0, false, context.Canceled
		}
	}
}

func (s *Source) enqueueLocked(now time.Time) {
	if s.keyframe.Swap(false) && !s.clip.Units[s.index].Sync {
		s.index = 0
	}
	unit := s.clip.Units[s.index]
	s.index = (s.index + 1) % len(s.clip.Units)
	for position, nal := range unit.NALs {
		last := position == len(unit.NALs)-1
		s.packetizeLocked(nal, last)
	}
	s.stamp += s.step
	s.sent.Add(1)
	if s.next.IsZero() || now.Sub(s.next) > 5*s.interval {
		s.next = now
	}
	s.next = s.next.Add(s.interval)
}

func (s *Source) header(marker bool) []byte {
	packet := make([]byte, 12, 12+maxPayload+3)
	packet[0] = 0x80
	packet[1] = payloadTypeHEVC
	if marker {
		packet[1] |= 0x80
	}
	binary.BigEndian.PutUint16(packet[2:], s.sequence)
	binary.BigEndian.PutUint32(packet[4:], s.stamp)
	binary.BigEndian.PutUint32(packet[8:], s.info.SSRC)
	s.sequence++
	return packet
}

// packetizeLocked cuts one NAL unit into RTP packets (RFC 7798): one packet when it fits, fragments when not.
func (s *Source) packetizeLocked(nal []byte, last bool) {
	if len(nal) <= maxPayload {
		s.queue = append(s.queue, append(s.header(last), nal...))
		return
	}
	kind := nalType(nal)
	body := nal[2:]
	for offset := 0; offset < len(body); {
		size := min(maxPayload-3, len(body)-offset)
		end := offset+size == len(body)
		packet := s.header(last && end)
		fragment := byte(kind)
		if offset == 0 {
			fragment |= 0x80
		}
		if end {
			fragment |= 0x40
		}
		packet = append(packet, nal[0]&0x81|nalFragment<<1, nal[1], fragment)
		packet = append(packet, body[offset:offset+size]...)
		s.queue = append(s.queue, packet)
		offset += size
	}
}

package main

import (
	"strconv"
	"sync"
	"time"
)

// The Mac sends its sound as AAC-ELD, one packet for every 480 frames (10 ms at 48 kHz), on the same
// UDP flow as the video. This file holds the part that needs neither libavcodec nor a sound server:
// the buffer that rides out the network's jitter and the clock difference between the two computers,
// and the loop that feeds a decoder and a device from it. The decoder and the device are interfaces
// so the loop is tested with fakes; audio_linux.go has the real ones.

const (
	// audioPacketDuration is the sound in one packet.
	audioPacketDuration = 10 * time.Millisecond
	// audioTargetPackets is how many packets are held back before playing starts: 40 ms. It is what
	// the jitter of a LAN needs, and with the device's own buffer (audioDeviceLatency) it keeps the
	// total delay under 100 ms.
	audioTargetPackets = 4
	// audioExtraPackets is how far past the target the queue may grow before the oldest packets are
	// thrown away. A Mac whose clock runs faster than this computer's fills the queue slowly; 50 ms
	// of slack means a drop of 10 ms every few minutes, not a delay that keeps growing.
	audioExtraPackets = 5
	// audioMaxSilent is how many silent packets are played in a row for a queue that ran dry before
	// playing stops until the queue has filled again.
	audioMaxSilent = 5
	// audioMaxLostPackets caps the silence written for packets the network lost.
	audioMaxLostPackets = 3
	// audioDeviceLatency is the delay the sound device is asked to keep.
	audioDeviceLatency = 40 * time.Millisecond
	// audioFramesPerPacket is the frames in one packet when the Mac does not say.
	audioFramesPerPacket = 480
)

// audioUnit is one received packet.
type audioUnit struct {
	Data []byte
	// Lost is how many packets before this one never arrived.
	Lost int
}

// jitterAction is what the player does next.
type jitterAction int

const (
	// jitterWait: nothing to play and playing has not started (or has stopped to refill).
	jitterWait jitterAction = iota
	// jitterPlay: play the unit.
	jitterPlay
	// jitterSilence: the queue ran dry in the middle of playing; write one packet of silence.
	jitterSilence
)

// audioCounters are the buffer's running totals.
type audioCounters struct {
	Received, Played uint64
	// Dropped packets were thrown away because the queue grew past its limit, Discarded ones because
	// the sound was muted.
	Dropped, Discarded uint64
	// Underruns counts the times the queue ran dry while playing, Silence the packets of silence
	// written for them.
	Underruns, Silence uint64
	Lost               uint64
	// DecodeErrors counts the packets the decoder refused; each was played as 10 ms of silence. ZeroPackets
	// counts the ones that decoded to nothing but zeros (a Mac with nothing to play sends those), and
	// ZeroHoles the single ones between two packets with sound: a hole the Mac or the decoder made.
	DecodeErrors, ZeroPackets, ZeroHoles uint64
	// Depth is how many packets wait now, MaxDepth the most since the start.
	Depth, MaxDepth int
}

// audioJitter is a queue of packets between the network goroutine, which must never wait, and the
// player. It starts playing when target packets are in, drops the oldest when more than target+extra
// are, and plays silence for a short gap so a late packet does not stop the sound.
type audioJitter struct {
	target, extra int

	access   sync.Mutex
	queue    []audioUnit
	playing  bool
	silent   int
	underrun bool
	muted    bool
	counters audioCounters
}

func newAudioJitter(target, extra int) *audioJitter {
	return &audioJitter{target: max(1, target), extra: max(0, extra)}
}

// push queues a packet. It never blocks.
func (j *audioJitter) push(unit audioUnit) {
	j.access.Lock()
	defer j.access.Unlock()
	if j.muted {
		j.counters.Discarded++
		return
	}
	j.counters.Received++
	j.counters.Lost += uint64(max(unit.Lost, 0))
	j.queue = append(j.queue, unit)
	if limit := j.target + j.extra; len(j.queue) > limit {
		drop := len(j.queue) - j.target
		j.counters.Dropped += uint64(drop)
		j.queue = append(j.queue[:0], j.queue[drop:]...)
	}
	j.counters.Depth = len(j.queue)
	j.counters.MaxDepth = max(j.counters.MaxDepth, len(j.queue))
}

// pull says what to play next.
func (j *audioJitter) pull() (audioUnit, jitterAction) {
	j.access.Lock()
	defer j.access.Unlock()
	if !j.playing {
		if len(j.queue) < j.target {
			return audioUnit{}, jitterWait
		}
		j.playing = true
		j.silent = 0
		j.underrun = false
	}
	if len(j.queue) == 0 {
		if !j.underrun {
			j.underrun = true
			j.counters.Underruns++
		}
		j.silent++
		if j.silent > audioMaxSilent {
			j.playing = false
			return audioUnit{}, jitterWait
		}
		j.counters.Silence++
		return audioUnit{}, jitterSilence
	}
	j.silent = 0
	j.underrun = false
	unit := j.queue[0]
	j.queue = append(j.queue[:0], j.queue[1:]...)
	j.counters.Played++
	j.counters.Depth = len(j.queue)
	return unit, jitterPlay
}

// setMuted turns the sound off or on. Muting empties the queue and discards what arrives; unmuting
// starts from an empty queue, so the sound resumes with fresh packets.
func (j *audioJitter) setMuted(muted bool) {
	j.access.Lock()
	defer j.access.Unlock()
	if j.muted == muted {
		return
	}
	j.muted = muted
	j.queue = j.queue[:0]
	j.playing = false
	j.counters.Depth = 0
}

func (j *audioJitter) isMuted() bool {
	j.access.Lock()
	defer j.access.Unlock()
	return j.muted
}

func (j *audioJitter) stats() audioCounters {
	j.access.Lock()
	defer j.access.Unlock()
	c := j.counters
	c.Depth = len(j.queue)
	return c
}

// soundStats is what the stats card and the log show about the Mac's sound.
type soundStats struct {
	Counters audioCounters
	// LastError is why the decoder refused the last packet it did.
	LastError string
	// Device is the delay of the sound device, Queue the sound waiting in the jitter buffer.
	Device time.Duration
	Muted  bool
}

// Delay is how long the sound takes from the queue to the speakers.
func (s soundStats) Delay() time.Duration {
	return s.Device + time.Duration(s.Counters.Depth)*audioPacketDuration
}

// audioDecoder turns one packet into interleaved 16 bit samples.
type audioDecoder interface {
	Decode(data []byte) ([]int16, error)
	Close()
}

// audioDevice plays interleaved 16 bit samples. Write waits until the device has room, which paces
// the player at the device's clock.
type audioDevice interface {
	Write(samples []int16) error
	// Flush throws away what the device holds but has not played.
	Flush()
	// Latency is how long a sample written now waits before it is heard.
	Latency() time.Duration
	Close()
}

// audioPlayer plays the Mac's sound. Packets go in through push, which never waits; one goroutine
// decodes them and writes them to the device.
type audioPlayer struct {
	jitter   *audioJitter
	channels int
	frames   int
	open     func() (audioDecoder, audioDevice, error)
	// failed is told once if the decoder or the device could not be opened or broke.
	failed func(error)

	wake chan struct{}
	stop chan struct{}
	done chan struct{}

	access  sync.Mutex
	latency time.Duration
	stopped bool
	// decodeErrors counts the packets the decoder refused, lastError says why the last one was.
	decodeErrors uint64
	zeroPackets  uint64
	zeroHoles    uint64
	lastError    string
}

// newAudioPlayer starts the player. open makes the decoder and the device on the player's own
// goroutine, so a slow sound server does not hold up the network.
func newAudioPlayer(channels, frames int, open func() (audioDecoder, audioDevice, error), failed func(error)) *audioPlayer {
	p := &audioPlayer{
		jitter:   newAudioJitter(audioTargetPackets, audioExtraPackets),
		channels: channels,
		frames:   frames,
		open:     open,
		failed:   failed,
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go p.run()
	return p
}

// push hands over a packet. It is called from the network goroutine and never waits.
func (p *audioPlayer) push(unit audioUnit) {
	p.jitter.push(unit)
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *audioPlayer) setMuted(muted bool) {
	p.jitter.setMuted(muted)
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// deviceLatency is the device's delay as of the last write.
func (p *audioPlayer) deviceLatency() time.Duration {
	p.access.Lock()
	defer p.access.Unlock()
	return p.latency
}

func (p *audioPlayer) stats() audioCounters {
	c := p.jitter.stats()
	p.access.Lock()
	c.DecodeErrors = p.decodeErrors
	c.ZeroPackets = p.zeroPackets
	c.ZeroHoles = p.zeroHoles
	p.access.Unlock()
	return c
}

// lastDecodeError is why the decoder refused the last packet it refused, empty if it never did.
func (p *audioPlayer) lastDecodeError() string {
	p.access.Lock()
	defer p.access.Unlock()
	return p.lastError
}

// close stops the goroutine and releases the device. It is safe to call twice.
func (p *audioPlayer) close() {
	p.access.Lock()
	already := p.stopped
	p.stopped = true
	p.access.Unlock()
	if !already {
		close(p.stop)
	}
	<-p.done
}

func (p *audioPlayer) run() {
	defer close(p.done)
	decoder, device, err := p.open()
	if err != nil {
		if p.failed != nil {
			p.failed(err)
		}
		return
	}
	defer decoder.Close()
	defer device.Close()
	silence := make([]int16, p.frames*p.channels)
	var wasMuted, heard bool
	var zeroRun int
	for {
		select {
		case <-p.stop:
			return
		default:
		}
		if muted := p.jitter.isMuted(); muted != wasMuted {
			wasMuted = muted
			if muted {
				device.Flush()
			}
		}
		unit, action := p.jitter.pull()
		switch action {
		case jitterWait:
			// A muted player has nothing to wake for but a change of the mute or the end.
			var timer *time.Timer
			var tick <-chan time.Time
			if !p.jitter.isMuted() {
				timer = time.NewTimer(audioPacketDuration * 2)
				tick = timer.C
			}
			select {
			case <-p.stop:
				if timer != nil {
					timer.Stop()
				}
				return
			case <-p.wake:
			case <-tick:
			}
			if timer != nil {
				timer.Stop()
			}
		case jitterSilence:
			if err := device.Write(silence); err != nil {
				p.fail(err)
				return
			}
			p.setLatency(device.Latency())
		case jitterPlay:
			if unit.Lost > 0 {
				lost := min(unit.Lost, audioMaxLostPackets)
				if err := device.Write(make([]int16, lost*len(silence))); err != nil {
					p.fail(err)
					return
				}
			}
			samples, err := decoder.Decode(unit.Data)
			if err != nil || len(samples) == 0 {
				// One packet that does not decode is a gap of 10 ms, not the end of the sound.
				samples = silence
				p.access.Lock()
				p.decodeErrors++
				if err != nil {
					p.lastError = err.Error() + " (" + strconv.Itoa(len(unit.Data)) + " bytes)"
				} else {
					p.lastError = "no samples (" + strconv.Itoa(len(unit.Data)) + " bytes)"
				}
				p.access.Unlock()
			}
			zero := allZero(samples)
			p.access.Lock()
			if zero {
				p.zeroPackets++
				zeroRun++
			} else {
				if zeroRun == 1 && heard {
					p.zeroHoles++
				}
				zeroRun, heard = 0, true
			}
			p.access.Unlock()
			if err := device.Write(samples); err != nil {
				p.fail(err)
				return
			}
			p.setLatency(device.Latency())
		}
	}
}

// allZero reports whether a packet holds nothing but zeros.
func allZero(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return false
		}
	}
	return true
}

func (p *audioPlayer) setLatency(latency time.Duration) {
	p.access.Lock()
	p.latency = latency
	p.access.Unlock()
}

func (p *audioPlayer) fail(err error) {
	if p.failed != nil {
		p.failed(err)
	}
}

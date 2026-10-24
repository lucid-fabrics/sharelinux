package main

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func unit(b byte) audioUnit { return audioUnit{Data: []byte{b}} }

// pullAll pulls until the buffer says wait, returning the first byte of each played unit and how many
// silent packets came out.
func pullAll(j *audioJitter) (played []byte, silence int) {
	for range 100 {
		u, action := j.pull()
		switch action {
		case jitterWait:
			return played, silence
		case jitterSilence:
			silence++
		case jitterPlay:
			played = append(played, u.Data[0])
		}
	}
	return played, silence
}

func TestJitterWaitsForTheTargetThenPlaysInOrder(t *testing.T) {
	j := newAudioJitter(3, 4)
	for i := range byte(2) {
		j.push(unit(i))
		if _, action := j.pull(); action != jitterWait {
			t.Fatalf("after %d packets: action %v, want wait", i+1, action)
		}
	}
	j.push(unit(2))
	for want := range byte(3) {
		u, action := j.pull()
		if action != jitterPlay || u.Data[0] != want {
			t.Fatalf("pull %d: %v %v", want, action, u.Data)
		}
	}
}

func TestJitterDropsTheOldestPastTheLimit(t *testing.T) {
	j := newAudioJitter(2, 3)
	for i := range byte(6) {
		j.push(unit(i))
	}
	stats := j.stats()
	if stats.Dropped != 4 || stats.Depth != 2 {
		t.Fatalf("dropped %d depth %d, want 4 and 2", stats.Dropped, stats.Depth)
	}
	played, _ := pullAll(j)
	if len(played) < 2 || played[0] != 4 || played[1] != 5 {
		t.Fatalf("kept %v, want the newest 4 5", played)
	}
	if stats := j.stats(); stats.MaxDepth != 5 {
		t.Fatalf("max depth %d, want 5", stats.MaxDepth)
	}
}

func TestJitterPlaysSilenceForAShortGapThenRefills(t *testing.T) {
	j := newAudioJitter(2, 4)
	j.push(unit(1))
	j.push(unit(2))
	played, silence := pullAll(j)
	if len(played) != 2 || silence != audioMaxSilent {
		t.Fatalf("played %v silence %d, want 2 packets and %d silent", played, silence, audioMaxSilent)
	}
	if stats := j.stats(); stats.Underruns != 1 || stats.Silence != audioMaxSilent {
		t.Fatalf("underruns %d silence %d", stats.Underruns, stats.Silence)
	}
	// Playing stopped: one packet is not enough to start again.
	j.push(unit(3))
	if _, action := j.pull(); action != jitterWait {
		t.Fatalf("action %v after refilling one packet, want wait", action)
	}
	j.push(unit(4))
	if u, action := j.pull(); action != jitterPlay || u.Data[0] != 3 {
		t.Fatalf("%v %v", action, u.Data)
	}
}

func TestJitterCountsOneUnderrunPerGap(t *testing.T) {
	j := newAudioJitter(1, 4)
	j.push(unit(1))
	j.pull()
	for range 3 {
		if _, action := j.pull(); action != jitterSilence {
			t.Fatalf("action %v, want silence", action)
		}
	}
	j.push(unit(2))
	if _, action := j.pull(); action != jitterPlay {
		t.Fatalf("action %v, want play", action)
	}
	j.pull() // a second gap
	if stats := j.stats(); stats.Underruns != 2 {
		t.Fatalf("underruns %d, want 2", stats.Underruns)
	}
}

func TestJitterMuteEmptiesAndDiscards(t *testing.T) {
	j := newAudioJitter(2, 4)
	j.push(unit(1))
	j.push(unit(2))
	j.setMuted(true)
	j.push(unit(3))
	stats := j.stats()
	if stats.Depth != 0 || stats.Discarded != 1 || stats.Received != 2 {
		t.Fatalf("%+v", stats)
	}
	if _, action := j.pull(); action != jitterWait {
		t.Fatalf("muted pull %v", action)
	}
	j.setMuted(false)
	j.push(unit(4))
	j.push(unit(5))
	if u, action := j.pull(); action != jitterPlay || u.Data[0] != 4 {
		t.Fatalf("after unmuting: %v %v", action, u.Data)
	}
}

func TestJitterCountsLostPackets(t *testing.T) {
	j := newAudioJitter(1, 4)
	j.push(audioUnit{Data: []byte{1}, Lost: 2})
	j.push(audioUnit{Data: []byte{2}, Lost: -1})
	if got := j.stats().Lost; got != 2 {
		t.Fatalf("lost %d, want 2", got)
	}
}

// fakeDecoder turns a packet into frames*channels samples of the packet's first byte.
type fakeDecoder struct {
	samples int
	fail    func(b byte) bool
	closed  bool
}

func (d *fakeDecoder) Decode(data []byte) ([]int16, error) {
	if d.fail != nil && d.fail(data[0]) {
		return nil, errors.New("bad packet")
	}
	out := make([]int16, d.samples)
	for i := range out {
		out[i] = int16(data[0])
	}
	return out, nil
}

func (d *fakeDecoder) Close() { d.closed = true }

type fakeDevice struct {
	access  sync.Mutex
	writes  [][]int16
	flushes int
	closed  bool
}

func (d *fakeDevice) Write(samples []int16) error {
	d.access.Lock()
	defer d.access.Unlock()
	d.writes = append(d.writes, append([]int16(nil), samples...))
	return nil
}
func (d *fakeDevice) Flush() {
	d.access.Lock()
	d.flushes++
	d.access.Unlock()
}
func (d *fakeDevice) Latency() time.Duration { return 40 * time.Millisecond }
func (d *fakeDevice) Close()                 { d.closed = true }

func (d *fakeDevice) written() [][]int16 {
	d.access.Lock()
	defer d.access.Unlock()
	return append([][]int16(nil), d.writes...)
}

func (d *fakeDevice) flushed() int {
	d.access.Lock()
	defer d.access.Unlock()
	return d.flushes
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func startPlayer(t *testing.T, decoder *fakeDecoder, device *fakeDevice) *audioPlayer {
	t.Helper()
	p := newAudioPlayer(2, 4, func() (audioDecoder, audioDevice, error) { return decoder, device, nil }, func(err error) { t.Errorf("failed: %v", err) })
	t.Cleanup(p.close)
	return p
}

func TestPlayerPlaysPacketsInOrder(t *testing.T) {
	decoder, device := &fakeDecoder{samples: 8}, &fakeDevice{}
	p := startPlayer(t, decoder, device)
	for i := range byte(audioTargetPackets) {
		p.push(unit(i + 10))
	}
	waitUntil(t, "the packets to be written", func() bool { return len(device.written()) >= audioTargetPackets })
	for i, write := range device.written()[:audioTargetPackets] {
		if len(write) != 8 || write[0] != int16(10+i) {
			t.Fatalf("write %d is %v", i, write)
		}
	}
	waitUntil(t, "the device latency", func() bool { return p.deviceLatency() == 40*time.Millisecond })
}

func TestPlayerWritesSilenceForLostPacketsAndBadOnes(t *testing.T) {
	decoder := &fakeDecoder{samples: 8, fail: func(b byte) bool { return b == 99 }}
	device := &fakeDevice{}
	p := startPlayer(t, decoder, device)
	p.push(audioUnit{Data: []byte{1}})
	p.push(audioUnit{Data: []byte{99}})
	p.push(audioUnit{Data: []byte{2}, Lost: 2})
	p.push(audioUnit{Data: []byte{3}})
	// 1, silence for 99, silence for the 2 lost, 2, 3; silence may follow once the queue is dry.
	waitUntil(t, "five writes", func() bool { return len(device.written()) >= 5 })
	if got := p.stats().DecodeErrors; got != 1 {
		t.Fatalf("decode errors %d, want 1", got)
	}
	if got := p.lastDecodeError(); got != "bad packet (1 bytes)" {
		t.Fatalf("last error %q", got)
	}
	writes := device.written()
	if writes[0][0] != 1 {
		t.Fatalf("first write %v", writes[0])
	}
	if writes[1][0] != 0 || len(writes[1]) != 8 {
		t.Fatalf("a packet that does not decode should write silence, got %v", writes[1])
	}
	if writes[2][0] != 0 || len(writes[2]) != 16 {
		t.Fatalf("2 lost packets should write 2 packets of silence, got %d samples", len(writes[2]))
	}
	if writes[3][0] != 2 || writes[4][0] != 3 {
		t.Fatalf("then the packets: %v %v", writes[3][:1], writes[4][:1])
	}
}

func TestPlayerCapsTheSilenceForLostPackets(t *testing.T) {
	device := &fakeDevice{}
	p := startPlayer(t, &fakeDecoder{samples: 8}, device)
	p.push(audioUnit{Data: []byte{1}})
	p.push(audioUnit{Data: []byte{1}})
	p.push(audioUnit{Data: []byte{1}})
	p.push(audioUnit{Data: []byte{1}, Lost: 1000})
	waitUntil(t, "the lost packets' silence", func() bool {
		for _, write := range device.written() {
			if len(write) == audioMaxLostPackets*8 {
				return true
			}
		}
		return false
	})
}

func TestPlayerMuteFlushesTheDevice(t *testing.T) {
	device := &fakeDevice{}
	p := startPlayer(t, &fakeDecoder{samples: 8}, device)
	p.setMuted(true)
	waitUntil(t, "the flush", func() bool { return device.flushed() == 1 })
	for i := range byte(audioTargetPackets) {
		p.push(unit(i + 1))
	}
	time.Sleep(60 * time.Millisecond)
	if n := len(device.written()); n != 0 {
		t.Fatalf("%d writes while muted", n)
	}
	p.setMuted(false)
	for i := range byte(audioTargetPackets) {
		p.push(unit(i + 1))
	}
	waitUntil(t, "sound after unmuting", func() bool { return len(device.written()) > 0 })
}

func TestPlayerCloseJoinsAndReleases(t *testing.T) {
	decoder, device := &fakeDecoder{samples: 8}, &fakeDevice{}
	p := newAudioPlayer(2, 4, func() (audioDecoder, audioDevice, error) { return decoder, device, nil }, nil)
	p.close()
	p.close()
	if !decoder.closed || !device.closed {
		t.Fatalf("decoder closed %v device closed %v", decoder.closed, device.closed)
	}
}

func TestPlayerReportsAFailedOpenOnce(t *testing.T) {
	var mu sync.Mutex
	var calls int
	p := newAudioPlayer(2, 4, func() (audioDecoder, audioDevice, error) { return nil, nil, errors.New("no sound server") }, func(err error) {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	p.close()
	p.push(unit(1)) // pushing to a player that never started must not block
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("failed called %d times", calls)
	}
}

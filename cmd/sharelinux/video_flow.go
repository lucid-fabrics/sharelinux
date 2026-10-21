package main

import (
	"errors"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// videoItem is one thing for the decoder, in the order the Mac sent it: a new
// format (the decoder starts over with its parameter sets) or an access unit.
type videoItem struct {
	Format *media.VideoFormat
	Unit   *media.VideoAccessUnit
	// Queued is when the item went into the queue.
	Queued time.Time
}

// unitQueue holds units between the network goroutine and the decoder's. It is
// short on purpose: a decoder that falls behind sheds the whole backlog and
// waits for a keyframe, because an old picture is worth less than a late one is
// harmful, and a unit cannot be skipped without breaking the ones after it.
type unitQueue struct {
	max int

	access     sync.Mutex
	ready      *sync.Cond
	items      []videoItem
	awaitSync  bool
	closed     bool
	dropped    uint64
	shedEvents uint64
}

func newUnitQueue(max int) *unitQueue {
	q := &unitQueue{max: max}
	q.ready = sync.NewCond(&q.access)
	return q
}

// push adds an item. It reports whether a keyframe is needed because units were
// lost: the backlog was shed, or a unit came that cannot be decoded without
// the keyframe the queue is waiting for.
func (q *unitQueue) push(item videoItem) (needKeyframe bool) {
	q.access.Lock()
	defer q.access.Unlock()
	if q.closed {
		return false
	}
	if item.Format != nil {
		// A new format makes everything before it useless.
		q.dropUnitsLocked()
		q.awaitSync = false
		q.items = append(q.items, item)
		q.ready.Signal()
		return false
	}
	if q.awaitSync && !item.Unit.Sync {
		q.dropped++
		return true
	}
	if q.unitCountLocked() >= q.max {
		q.dropUnitsLocked()
		q.shedEvents++
		if !item.Unit.Sync {
			q.awaitSync = true
			q.dropped++
			return true
		}
	}
	if item.Unit.Sync {
		q.awaitSync = false
	}
	q.items = append(q.items, item)
	q.ready.Signal()
	return false
}

func (q *unitQueue) unitCountLocked() int {
	count := 0
	for _, item := range q.items {
		if item.Unit != nil {
			count++
		}
	}
	return count
}

func (q *unitQueue) dropUnitsLocked() {
	kept := q.items[:0]
	for _, item := range q.items {
		if item.Unit != nil {
			q.dropped++
			continue
		}
		kept = append(kept, item)
	}
	clear(q.items[len(kept):])
	q.items = kept
}

// pop waits for the next item. It returns false once the queue is closed.
func (q *unitQueue) pop() (videoItem, bool) {
	q.access.Lock()
	defer q.access.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.ready.Wait()
	}
	if q.closed {
		return videoItem{}, false
	}
	item := q.items[0]
	q.items[0] = videoItem{}
	q.items = q.items[1:]
	return item, true
}

// close wakes the reader and drops what is left.
func (q *unitQueue) close() {
	q.access.Lock()
	q.closed = true
	q.items = nil
	q.access.Unlock()
	q.ready.Broadcast()
}

// waitForSync tells the queue the decoder lost its place: units are dropped
// until the next keyframe.
func (q *unitQueue) waitForSync() {
	q.access.Lock()
	q.awaitSync = true
	q.access.Unlock()
}

// droppedCount is how many units the queue has thrown away.
func (q *unitQueue) droppedCount() uint64 {
	q.access.Lock()
	defer q.access.Unlock()
	return q.dropped
}

// changeTracker tells which pictures show a real change. A still screen keeps coming as a
// steady stream of tiny units, so the arrival of a picture says nothing about the input that
// was sent; a unit far bigger than the usual one does.
type changeTracker struct {
	average float64
}

const (
	// changeFloor is the size below which a unit is never a change, in bytes.
	changeFloor = 1500
	// changeFactor is how many times the usual size a unit must be.
	changeFactor = 4
)

// changed reports whether a unit of this size is a change, and learns from it.
func (c *changeTracker) changed(bytes int) bool {
	size := float64(bytes)
	changed := c.average > 0 && size > max(changeFloor, c.average*changeFactor)
	if c.average == 0 {
		c.average = size
	} else {
		c.average += (size - c.average) / 16
	}
	return changed
}

// keyframeGate spaces out requests for a keyframe: the Mac answers each one
// with a big frame, and asking again before it arrives only makes more.
type keyframeGate struct {
	interval time.Duration
	last     time.Time
}

// allow reports whether a request may go out now.
func (g *keyframeGate) allow(now time.Time) bool {
	if !g.last.IsZero() && now.Sub(g.last) < g.interval {
		return false
	}
	g.last = now
	return true
}

// stopKind is why a media stream ended.
type stopKind int

const (
	// stopRequested is our own stop.
	stopRequested stopKind = iota
	// stopBusy is a Mac that is already streaming to another viewer.
	stopBusy
	// stopRefused is a Mac that does not take this offer.
	stopRefused
	// stopSilent is a stream that went quiet.
	stopSilent
	// stopNetwork is anything else: a lost connection, a failed negotiation.
	stopNetwork
)

// classifyStop reads the error a stream ended with.
func classifyStop(err error) stopKind {
	if err == nil {
		return stopRequested
	}
	if refusal, ok := errors.AsType[*media.ServerError](err); ok {
		if refusal.Type == media.ServerErrorBusy && refusal.SubCode == media.ServerErrorBusySubCode {
			return stopBusy
		}
		return stopRefused
	}
	if errors.Is(err, media.ErrStreamSilent) {
		return stopSilent
	}
	return stopNetwork
}

// restartPolicy decides whether a stream that ended is started again.
type restartPolicy struct {
	// Limit is how many restarts in a row are tried before the tiles are used for good.
	Limit int
	// Base is the first wait; each one doubles it.
	Base time.Duration
	// Cap is how many restarts are allowed within Window, whatever healthy() said in between: a stream
	// that runs a few seconds and fails again must not be able to start over for ever. Zero means no cap.
	Cap    int
	Window time.Duration

	attempts int
	history  []time.Time
	// clock is time.Now except in tests.
	clock func() time.Time
}

func (p *restartPolicy) now() time.Time {
	if p.clock != nil {
		return p.clock()
	}
	return time.Now()
}

// next says what to do after a stream stopped: wait delay and start again, or
// give up (restart is false) and stay on the tiles.
func (p *restartPolicy) next(kind stopKind) (delay time.Duration, restart bool) {
	switch kind {
	case stopRequested, stopBusy, stopRefused:
		return 0, false
	}
	if p.attempts >= p.Limit {
		return 0, false
	}
	if p.Cap > 0 {
		now := p.now()
		kept := p.history[:0]
		for _, at := range p.history {
			if now.Sub(at) < p.Window {
				kept = append(kept, at)
			}
		}
		p.history = kept
		if len(p.history) >= p.Cap {
			return 0, false
		}
		p.history = append(p.history, now)
	}
	delay = p.Base << p.attempts
	p.attempts++
	return delay, true
}

// healthy is called when pictures have flowed for a while: the next trouble starts the count again.
func (p *restartPolicy) healthy() {
	p.attempts = 0
}

// streamPulse is what a session knows of a running stream at one moment.
type streamPulse struct {
	// QuietFor is the time since the core delivered an access unit.
	QuietFor time.Duration
	// IdleFor is the time since a decoded picture was handed to the widget.
	IdleFor time.Duration
	// HeardFor is the time since the Mac's media transport showed any sign of life: a packet or a sender report.
	HeardFor time.Duration
}

var (
	errVideoStalled = errors.New("the Mac stopped sending video")
	errVideoFrozen  = errors.New("the video arrives but no picture is shown")
)

// stallKind says what is wrong with a stream.
type stallKind int

const (
	stallNone stallKind = iota
	// stallSilent: the Mac stopped sending and its transport is dead too.
	stallSilent
	// stallFrozen: units keep arriving and no picture reaches the widget.
	stallFrozen
)

// judgeStall says whether a stream that should be showing is stuck. A Mac that has nothing new
// to show may send no units, so silence alone is not a stall while its transport still answers;
// units that arrive and are not shown are one, whatever the transport says.
func judgeStall(pulse streamPulse, limit time.Duration) stallKind {
	if pulse.QuietFor >= limit && pulse.HeardFor >= limit {
		return stallSilent
	}
	if pulse.QuietFor < limit && pulse.IdleFor >= limit {
		return stallFrozen
	}
	return stallNone
}

// heardWatch turns the media transport's counters into the time since they last moved.
type heardWatch struct {
	packets, reports uint64
	changed          time.Time
}

// see takes the counters at now and returns how long it has been since either grew.
func (h *heardWatch) see(packets, reports uint64, now time.Time) time.Duration {
	if h.changed.IsZero() || packets != h.packets || reports != h.reports {
		h.packets, h.reports, h.changed = packets, reports, now
		return 0
	}
	return now.Sub(h.changed)
}

// callQueue runs calls one after the other on a goroutine of its own, in the order they were
// queued. It never blocks the caller and never reorders: a start and a stop must reach the Mac
// in the order the session made them.
type callQueue struct {
	access  sync.Mutex
	calls   []func()
	running bool
	closed  bool
}

// do queues a call. After close it does nothing.
func (q *callQueue) do(call func()) {
	q.access.Lock()
	defer q.access.Unlock()
	if q.closed {
		return
	}
	q.calls = append(q.calls, call)
	if !q.running {
		q.running = true
		go q.drain()
	}
}

func (q *callQueue) drain() {
	for {
		q.access.Lock()
		if len(q.calls) == 0 {
			q.running = false
			q.access.Unlock()
			return
		}
		call := q.calls[0]
		q.calls[0] = nil
		q.calls = q.calls[1:]
		q.access.Unlock()
		call()
	}
}

// close refuses new calls; the ones already queued still run.
func (q *callQueue) close() {
	q.access.Lock()
	q.closed = true
	q.access.Unlock()
}

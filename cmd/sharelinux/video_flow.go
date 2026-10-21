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

	attempts int
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
	delay = p.Base << p.attempts
	p.attempts++
	return delay, true
}

// healthy is called when pictures have flowed for a while: the next trouble starts the count again.
func (p *restartPolicy) healthy() {
	p.attempts = 0
}

package main

import (
	"errors"
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

func TestUnitQueuePushPop(t *testing.T) {
	q := newUnitQueue(4)

	first := videoItem{Unit: &media.VideoAccessUnit{Sync: true}}
	second := videoItem{Unit: &media.VideoAccessUnit{Sync: false}}

	if needKeyframe := q.push(first); needKeyframe {
		t.Fatal("push of the first unit requested a keyframe")
	}
	if needKeyframe := q.push(second); needKeyframe {
		t.Fatal("push of the second unit requested a keyframe")
	}

	item, ok := q.pop()
	if !ok {
		t.Fatal("pop unexpectedly reported a closed queue")
	}
	if item.Unit != first.Unit {
		t.Fatal("pop did not return the first queued unit")
	}

	item, ok = q.pop()
	if !ok {
		t.Fatal("pop unexpectedly reported a closed queue")
	}
	if item.Unit != second.Unit {
		t.Fatal("pop did not return the second queued unit")
	}

	q.close()
}

func TestUnitQueueShedsBacklog(t *testing.T) {
	tests := []struct {
		name          string
		incomingSync  bool
		wantKeyframe  bool
		wantDropped   uint64
		wantQueued    int
		wantAwaitSync bool
	}{
		{
			name:          "sync unit replaces the full backlog",
			incomingSync:  true,
			wantKeyframe:  false,
			wantDropped:   2,
			wantQueued:    1,
			wantAwaitSync: false,
		},
		{
			name:          "non-sync unit is dropped and requires a keyframe",
			incomingSync:  false,
			wantKeyframe:  true,
			wantDropped:   3,
			wantQueued:    0,
			wantAwaitSync: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newUnitQueue(2)

			q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: true}})
			q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: false}})

			needKeyframe := q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: tt.incomingSync}})
			if needKeyframe != tt.wantKeyframe {
				t.Fatalf("push returned needKeyframe = %v, want %v", needKeyframe, tt.wantKeyframe)
			}
			if got := q.droppedCount(); got != tt.wantDropped {
				t.Fatalf("droppedCount() = %d, want %d", got, tt.wantDropped)
			}

			q.access.Lock()
			queued := q.unitCountLocked()
			awaitSync := q.awaitSync
			q.access.Unlock()

			if queued != tt.wantQueued {
				t.Fatalf("queued units = %d, want %d", queued, tt.wantQueued)
			}
			if awaitSync != tt.wantAwaitSync {
				t.Fatalf("awaitSync = %v, want %v", awaitSync, tt.wantAwaitSync)
			}

			if tt.incomingSync {
				item, ok := q.pop()
				if !ok {
					t.Fatal("pop unexpectedly reported a closed queue")
				}
				if item.Unit == nil || !item.Unit.Sync {
					t.Fatal("the incoming sync unit was not queued")
				}
			}

			q.close()
		})
	}
}

func TestUnitQueueWaitForSync(t *testing.T) {
	q := newUnitQueue(4)

	q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: true}})
	if _, ok := q.pop(); !ok {
		t.Fatal("pop unexpectedly reported a closed queue")
	}

	q.waitForSync()

	if needKeyframe := q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: false}}); !needKeyframe {
		t.Fatal("non-sync unit did not report that a keyframe was needed")
	}
	if needKeyframe := q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: false}}); !needKeyframe {
		t.Fatal("second non-sync unit did not report that a keyframe was needed")
	}
	if got := q.droppedCount(); got != 2 {
		t.Fatalf("droppedCount() = %d, want 2", got)
	}

	if needKeyframe := q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: true}}); needKeyframe {
		t.Fatal("sync unit unexpectedly reported that a keyframe was needed")
	}

	item, ok := q.pop()
	if !ok {
		t.Fatal("pop unexpectedly reported a closed queue")
	}
	if item.Unit == nil || !item.Unit.Sync {
		t.Fatal("sync unit was not queued")
	}

	q.access.Lock()
	awaitSync := q.awaitSync
	q.access.Unlock()
	if awaitSync {
		t.Fatal("queue is still waiting for sync after receiving a sync unit")
	}

	q.close()
}

func TestUnitQueueNewFormatDropsQueuedUnits(t *testing.T) {
	q := newUnitQueue(4)

	q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: true}})
	q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: false}})

	format := new(media.VideoFormat)
	if needKeyframe := q.push(videoItem{Format: format}); needKeyframe {
		t.Fatal("a new format unexpectedly requested a keyframe")
	}
	if got := q.droppedCount(); got != 2 {
		t.Fatalf("droppedCount() = %d, want 2", got)
	}

	item, ok := q.pop()
	if !ok {
		t.Fatal("pop unexpectedly reported a closed queue")
	}
	if item.Format != format {
		t.Fatal("pop did not return the new format")
	}

	if needKeyframe := q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: false}}); needKeyframe {
		t.Fatal("unit after a new format unexpectedly requested a keyframe")
	}
	item, ok = q.pop()
	if !ok {
		t.Fatal("pop unexpectedly reported a closed queue")
	}
	if item.Unit == nil {
		t.Fatal("unit after the new format was not queued")
	}

	q.close()
}

func TestUnitQueueClose(t *testing.T) {
	t.Run("drops queued units", func(t *testing.T) {
		q := newUnitQueue(2)
		q.push(videoItem{Unit: &media.VideoAccessUnit{Sync: true}})
		q.close()

		if _, ok := q.pop(); ok {
			t.Fatal("pop returned an item after the queue was closed")
		}
	})

	t.Run("unblocks a blocked pop", func(t *testing.T) {
		q := newUnitQueue(1)
		started := make(chan struct{})
		result := make(chan bool, 1)

		go func() {
			close(started)
			_, ok := q.pop()
			result <- ok
		}()

		<-started
		select {
		case <-result:
			t.Fatal("pop returned before the queue was closed")
		case <-time.After(25 * time.Millisecond):
		}

		q.close()

		select {
		case ok := <-result:
			if ok {
				t.Fatal("blocked pop returned an item after the queue was closed")
			}
		case <-time.After(time.Second):
			t.Fatal("close did not unblock pop")
		}
	})
}

func TestKeyframeGateAllow(t *testing.T) {
	gate := keyframeGate{interval: time.Second}
	start := time.Unix(100, 0)

	if !gate.allow(start) {
		t.Fatal("the first request was not allowed")
	}
	if gate.allow(start.Add(999 * time.Millisecond)) {
		t.Fatal("request inside the interval was allowed")
	}
	if !gate.allow(start.Add(time.Second)) {
		t.Fatal("request at the interval boundary was not allowed")
	}
	if gate.allow(start.Add(time.Second + time.Nanosecond)) {
		t.Fatal("request inside the next interval was allowed")
	}
	if !gate.allow(start.Add(2 * time.Second)) {
		t.Fatal("request at the next interval boundary was not allowed")
	}
}

func TestClassifyStop(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want stopKind
	}{
		{
			name: "nil",
			err:  nil,
			want: stopRequested,
		},
		{
			name: "busy server error",
			err: &media.ServerError{
				Type:    media.ServerErrorBusy,
				SubCode: media.ServerErrorBusySubCode,
			},
			want: stopBusy,
		},
		{
			name: "other server error",
			err:  &media.ServerError{},
			want: stopRefused,
		},
		{
			name: "silent stream",
			err:  media.ErrStreamSilent,
			want: stopSilent,
		},
		{
			name: "plain error",
			err:  errors.New("connection lost"),
			want: stopNetwork,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyStop(tt.err); got != tt.want {
				t.Fatalf("classifyStop() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestartPolicyNext(t *testing.T) {
	policy := restartPolicy{Limit: 3, Base: time.Second}

	delay, restart := policy.next(stopNetwork)
	if !restart || delay != time.Second {
		t.Fatalf("first next() = (%v, %v), want (%v, true)", delay, restart, time.Second)
	}

	delay, restart = policy.next(stopNetwork)
	if !restart || delay != 2*time.Second {
		t.Fatalf("second next() = (%v, %v), want (%v, true)", delay, restart, 2*time.Second)
	}

	delay, restart = policy.next(stopNetwork)
	if !restart || delay != 4*time.Second {
		t.Fatalf("third next() = (%v, %v), want (%v, true)", delay, restart, 4*time.Second)
	}

	delay, restart = policy.next(stopNetwork)
	if restart || delay != 0 {
		t.Fatalf("next() after the limit = (%v, %v), want (0, false)", delay, restart)
	}
}

func TestRestartPolicyNeverRestartsForBusyOrRefused(t *testing.T) {
	for _, kind := range []stopKind{stopBusy, stopRefused} {
		policy := restartPolicy{Limit: 3, Base: time.Second}

		delay, restart := policy.next(kind)
		if restart || delay != 0 {
			t.Fatalf("next(%v) = (%v, %v), want (0, false)", kind, delay, restart)
		}
	}
}

func TestRestartPolicyHealthyResetsAttempts(t *testing.T) {
	policy := restartPolicy{Limit: 3, Base: time.Second}

	if delay, restart := policy.next(stopNetwork); !restart || delay != time.Second {
		t.Fatalf("first next() = (%v, %v), want (%v, true)", delay, restart, time.Second)
	}
	if delay, restart := policy.next(stopNetwork); !restart || delay != 2*time.Second {
		t.Fatalf("second next() = (%v, %v), want (%v, true)", delay, restart, 2*time.Second)
	}

	policy.healthy()

	delay, restart := policy.next(stopNetwork)
	if !restart || delay != time.Second {
		t.Fatalf("next() after healthy() = (%v, %v), want (%v, true)", delay, restart, time.Second)
	}
}

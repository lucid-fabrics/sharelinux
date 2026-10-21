package main

import (
	"sync"
	"testing"
	"time"
)

const testStall = 6 * time.Second

func TestJudgeStall(t *testing.T) {
	second := time.Second
	tests := []struct {
		name  string
		pulse streamPulse
		want  stallKind
	}{
		{"the Mac closed its ports and nothing came for 9 s", streamPulse{QuietFor: 9 * second, IdleFor: 9 * second, HeardFor: 9 * second}, stallSilent},
		{"silent for exactly the limit", streamPulse{QuietFor: testStall, IdleFor: testStall, HeardFor: testStall}, stallSilent},
		{"a still screen: no units but the transport answers", streamPulse{QuietFor: 30 * second, IdleFor: 30 * second, HeardFor: second}, stallNone},
		{"units arrive and nothing reaches the widget", streamPulse{QuietFor: 0, IdleFor: 7 * second, HeardFor: 0}, stallFrozen},
		{"frozen whatever the transport says", streamPulse{QuietFor: second, IdleFor: 20 * second, HeardFor: 20 * second}, stallFrozen},
		{"flowing", streamPulse{QuietFor: 20 * time.Millisecond, IdleFor: 20 * time.Millisecond, HeardFor: 0}, stallNone},
		{"a short gap", streamPulse{QuietFor: 2 * second, IdleFor: 2 * second, HeardFor: 2 * second}, stallNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := judgeStall(test.pulse, testStall); got != test.want {
				t.Fatalf("judgeStall(%+v) = %v, want %v", test.pulse, got, test.want)
			}
		})
	}
}

func TestHeardWatch(t *testing.T) {
	var watch heardWatch
	start := time.Now()
	if got := watch.see(5, 0, start); got != 0 {
		t.Fatalf("first look: %v", got)
	}
	if got := watch.see(5, 0, start.Add(3*time.Second)); got != 3*time.Second {
		t.Fatalf("nothing moved for 3 s, got %v", got)
	}
	if got := watch.see(6, 0, start.Add(4*time.Second)); got != 0 {
		t.Fatalf("a packet is a sign of life, got %v", got)
	}
	if got := watch.see(6, 1, start.Add(5*time.Second)); got != 0 {
		t.Fatalf("a sender report is a sign of life, got %v", got)
	}
	if got := watch.see(6, 1, start.Add(9*time.Second)); got != 4*time.Second {
		t.Fatalf("quiet again for 4 s, got %v", got)
	}
}

// The calls that start and stop the stream reach the Mac in the order they were made, also when
// the first one is slow and many pile up behind it.
func TestCallQueueKeepsOrder(t *testing.T) {
	var queue callQueue
	release := make(chan struct{})
	var access sync.Mutex
	var ran []int
	var done sync.WaitGroup
	const total = 200
	done.Add(total)
	for index := range total {
		queue.do(func() {
			if index == 0 {
				<-release
			}
			access.Lock()
			ran = append(ran, index)
			access.Unlock()
			done.Done()
		})
	}
	close(release)
	done.Wait()
	for index, got := range ran {
		if got != index {
			t.Fatalf("call %d ran at place %d", got, index)
		}
	}
}

func TestCallQueueClose(t *testing.T) {
	var queue callQueue
	release := make(chan struct{})
	var ran sync.WaitGroup
	var access sync.Mutex
	var count int
	ran.Add(2)
	for range 2 {
		queue.do(func() {
			<-release
			access.Lock()
			count++
			access.Unlock()
			ran.Done()
		})
	}
	queue.close()
	queue.do(func() {
		access.Lock()
		count += 100
		access.Unlock()
	})
	close(release)
	ran.Wait()
	time.Sleep(20 * time.Millisecond)
	access.Lock()
	defer access.Unlock()
	if count != 2 {
		t.Fatalf("count %d: the queued calls run, the one after close does not", count)
	}
}

// A stream that stalls is started again, and after four tries in a row the session stays on the tiles.
func TestStalledStreamRestartsFourTimesThenGivesUp(t *testing.T) {
	policy := restartPolicy{Limit: 4, Base: time.Second}
	for try := range 4 {
		delay, restart := policy.next(classifyStop(errVideoStalled))
		if !restart || delay != time.Second<<try {
			t.Fatalf("try %d: (%v, %v)", try+1, delay, restart)
		}
	}
	if delay, restart := policy.next(classifyStop(errVideoStalled)); restart || delay != 0 {
		t.Fatalf("the fifth try: (%v, %v), want the tiles", delay, restart)
	}
	frozen := restartPolicy{Limit: 4, Base: time.Second}
	if _, restart := frozen.next(classifyStop(errVideoFrozen)); !restart {
		t.Fatal("a frozen picture is restarted like a stalled stream")
	}
}

// A stream that runs for a few healthy seconds and stalls again must not be able to restart for ever.
func TestFlappingStreamIsGivenUp(t *testing.T) {
	now := time.Now()
	policy := restartPolicy{Limit: 4, Base: time.Second, Cap: 8, Window: 10 * time.Minute, clock: func() time.Time { return now }}
	for try := 1; try <= 8; try++ {
		if _, restart := policy.next(stopNetwork); !restart {
			t.Fatalf("restart %d is within the budget", try)
		}
		policy.healthy()
		now = now.Add(12 * time.Second)
	}
	if _, restart := policy.next(stopNetwork); restart {
		t.Fatal("the ninth restart in 100 seconds is endless flapping")
	}
	now = now.Add(11 * time.Minute)
	if _, restart := policy.next(stopNetwork); !restart {
		t.Fatal("a long session that had trouble long ago may try again")
	}
}

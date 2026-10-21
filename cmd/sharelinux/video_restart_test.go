//go:build linux

package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// The wait before a restart ends while the person has gone back to all displays: nothing starts, the
// retry is forgotten and the session may start the video when one display is chosen again.
func TestRetryWaitsForOneDisplay(t *testing.T) {
	tests := []struct {
		name               string
		ended, gaveUp, one bool
		want               bool
	}{
		{"one display", false, false, true, true},
		{"switched to all displays during the wait", false, false, false, false},
		{"the session ended", true, false, true, false},
		{"the session gave up", false, true, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vid := videoSession{retry: 7, gaveUp: test.gaveUp}
			if got := vid.retryDue(test.ended, test.one); got != test.want {
				t.Fatalf("retryDue = %v, want %v", got, test.want)
			}
			if vid.retry != 0 {
				t.Fatal("the pending retry is cleared, so maybeStartVideo can start the stream later")
			}
			if vid.gaveUp != test.gaveUp {
				t.Fatal("a switch of displays is not giving up")
			}
		})
	}
}

// The freeze found live: the Mac stopped sending and the last picture stayed up for good. Against a
// real stream and a mock Mac: a Mac with a still screen sends no units but its transport answers, which
// is no stall; a Mac that is gone (ports closed) is one.
func TestMockMacThatGoesQuietIsJudgedStalledOnlyWhenGone(t *testing.T) {
	clip := builtinClip(t)
	client, mac, closeAll := openMockMac(t, clip)
	defer closeAll()
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	presenter := &countingPresenter{}
	stream := newVideoStream(quiet, plan, "", 1, presenter, clientKeyframe(client))
	defer stream.close()
	if err := client.StartMediaStream(plan.Offer, stream); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pictures", 15*time.Second, func() bool { return presenter.count() >= 20 })
	var watch heardWatch
	// The mock sends a sender report about every second.
	const limit = 2 * time.Second
	judge := func() (stallKind, streamPulse) {
		var packets, reports uint64
		if media := client.MediaStream(); media != nil {
			stats := media.Stats()
			packets, reports = stats.VideoPackets, stats.SenderReports
		}
		now := time.Now()
		pulse := streamPulse{QuietFor: stream.quietFor(now), IdleFor: stream.idle(now), HeardFor: watch.see(packets, reports, now)}
		return judgeStall(pulse, limit), pulse
	}
	if kind, pulse := judge(); kind != stallNone {
		t.Fatalf("a flowing stream is judged %v (%+v)", kind, pulse)
	}
	// A still screen: no units for twice the limit, the sender reports go on.
	mac.mute.Store(true)
	quietUntil := time.Now().Add(2 * limit)
	for time.Now().Before(quietUntil) {
		if kind, pulse := judge(); kind != stallNone {
			t.Fatalf("a still screen with a live transport is judged %v (%+v)", kind, pulse)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stream.quietFor(time.Now()) < limit {
		t.Fatal("the stream was not quiet")
	}
	// The Mac is gone: nothing comes at all.
	closeAll()
	var verdict stallKind
	waitFor(t, "a stall", 10*time.Second, func() bool {
		verdict, _ = judge()
		return verdict != stallNone
	})
	if verdict != stallSilent {
		t.Fatalf("a Mac that is gone is %v, want silent", verdict)
	}
}

// Units that arrive and are never shown freeze the picture as surely as silence: the widget stops taking pictures.
func TestUnitsThatAreNeverShownAreJudgedFrozen(t *testing.T) {
	clip := builtinClip(t)
	plan := planVideo(videoSoftware, videoCaps{HEVCSoftware: true}, true)
	presenter := &refusingPresenter{}
	stream := newVideoStream(quiet, plan, "", 1, presenter, func() {})
	defer stream.close()
	stream.VideoFormat(clipFormat(clip))
	push(stream, clip, 0, 12)
	waitFor(t, "pictures", 5*time.Second, func() bool { return presenter.count() >= 12 })
	presenter.refuse.Store(true)
	const limit = 300 * time.Millisecond
	var watch heardWatch
	var verdict stallKind
	index := 12
	waitFor(t, "a frozen verdict", 10*time.Second, func() bool {
		stream.VideoAccessUnit(clipUnit(clip, index))
		index++
		time.Sleep(10 * time.Millisecond)
		verdict = judgeStall(streamPulse{QuietFor: stream.quietFor(time.Now()), IdleFor: stream.idle(time.Now()), HeardFor: watch.see(1, 0, time.Now())}, limit)
		return verdict != stallNone
	})
	if verdict != stallFrozen {
		t.Fatalf("got %v, want frozen", verdict)
	}
}

// refusingPresenter takes pictures like a widget until told to refuse them.
type refusingPresenter struct {
	countingPresenter
	refuse atomic.Bool
}

func (p *refusingPresenter) present(frame *videoFrame) outputKind {
	if p.refuse.Load() {
		frame.free()
		return outputNone
	}
	return p.countingPresenter.present(frame)
}

package main

import (
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

var oneFile = &rfb.RemoteDrag{Items: []rfb.RemoteDragItem{{Path: "/a/b.txt"}}}

// A drag the Mac announces with no request pending starts nothing: this was the
// duplicate download, a stale flag from an unanswered G fetched the next drag twice.
func TestDragAnnouncedWithoutRequestIsIgnored(t *testing.T) {
	var ask dragAsk
	if got := ask.settle(time.Unix(100, 0), oneFile, true); got != dragWaiting {
		t.Fatalf("verdict %v, want waiting", got)
	}
}

func TestDragAnswerStartsOneCopy(t *testing.T) {
	var ask dragAsk
	now := time.Unix(100, 0)
	ask.ask(now)
	if got := ask.settle(now.Add(time.Second), oneFile, true); got != dragFetch {
		t.Fatalf("verdict %v, want fetch", got)
	}
	if got := ask.settle(now.Add(2*time.Second), oneFile, true); got != dragWaiting {
		t.Fatalf("a second state after the answer gave %v, want waiting", got)
	}
}

func TestSilenceMeansNotDragging(t *testing.T) {
	var ask dragAsk
	now := time.Unix(100, 0)
	ask.ask(now)
	if got := ask.settle(now.Add(time.Second), nil, false); got != dragWaiting {
		t.Fatalf("verdict %v, want waiting", got)
	}
	if got := ask.settle(now.Add(fetchWait+time.Second), nil, false); got != dragNothing {
		t.Fatalf("verdict %v, want nothing", got)
	}
	// An answer after the wait must not start a copy nobody is watching for.
	if got := ask.settle(now.Add(fetchWait+2*time.Second), oneFile, true); got != dragWaiting {
		t.Fatalf("late answer gave %v, want waiting", got)
	}
}

func TestEmptyDragIsNotAnAnswer(t *testing.T) {
	var ask dragAsk
	now := time.Unix(100, 0)
	ask.ask(now)
	if got := ask.settle(now.Add(time.Second), &rfb.RemoteDrag{}, true); got != dragWaiting {
		t.Fatalf("verdict %v, want waiting", got)
	}
}

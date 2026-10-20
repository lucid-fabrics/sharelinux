package main

import (
	"testing"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/filecopy"
)

func TestProgressReporter(t *testing.T) {
	var states []transferState
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	report := newProgressReporter(func(s transferState) { states = append(states, s) }, func() time.Time { return current }, true)
	if report == nil {
		t.Fatal("the file copy core calls it unconditionally")
	}

	report(filecopy.Progress{Item: "/a/name.txt", Total: 0, Transferred: 5})
	if len(states) != 0 {
		t.Fatal("an unknown total reports nothing")
	}

	report(filecopy.Progress{Item: "/a/name.txt", Total: 200, Transferred: 50})
	report(filecopy.Progress{Item: "/a/name.txt", Total: 200, Transferred: 60})
	if len(states) != 1 || states[0].Percent != 25 || states[0].Name != "name.txt" || !states[0].Upload || !states[0].Active {
		t.Fatalf("first report goes out, the next one within the interval waits: %+v", states)
	}

	current = current.Add(progressInterval)
	report(filecopy.Progress{Item: "/a/name.txt", Total: 200, Transferred: 100})
	if len(states) != 2 || states[1].Percent != 50 {
		t.Fatalf("%+v", states)
	}

	report(filecopy.Progress{Item: "/a/name.txt", Total: 200, Transferred: 200})
	if len(states) != 3 || states[2].Percent != 100 {
		t.Fatalf("the final step is never throttled away: %+v", states)
	}
}

func TestTransferTitle(t *testing.T) {
	if got := (transferState{Name: "report.pdf", Upload: true}).title(); got != "Copying report.pdf to the Mac" {
		t.Fatal(got)
	}
	if got := (transferState{Name: "report.pdf"}).title(); got != "Copying report.pdf from the Mac" {
		t.Fatal(got)
	}
}

func TestCancelCopyStopsTheBatch(t *testing.T) {
	var batch transfers
	batch.beginBatch()
	if batch.wasCancelled() {
		t.Fatal("a new batch starts running")
	}
	batch.cancelCopy()
	if !batch.wasCancelled() {
		t.Fatal("cancel marks the batch")
	}
	batch.beginBatch()
	if batch.wasCancelled() {
		t.Fatal("the next batch is not cancelled")
	}
}

func TestHandlerTransferState(t *testing.T) {
	h := newHandler(nil, nil)
	if _, changed := h.takeTransfer(); changed {
		t.Fatal("nothing yet")
	}
	h.setTransfer(transferState{Name: "x", Percent: 4, Active: true})
	state, changed := h.takeTransfer()
	if !changed || state.Percent != 4 {
		t.Fatal("state is handed over once")
	}
	if _, changed = h.takeTransfer(); changed {
		t.Fatal("and only once")
	}
}

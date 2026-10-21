package machelper

import (
	"math"
	"math/rand"
	"testing"
)

func TestReplayWindowBasics(t *testing.T) {
	var w ReplayWindow
	if !w.Check(0) || !w.Commit(0) {
		t.Fatal("counter 0 must be accepted first")
	}
	if w.Check(0) || w.Commit(0) {
		t.Fatal("duplicate must be rejected")
	}
	if !w.Commit(2047) {
		t.Fatal("2047 accepted")
	}
	if w.Check(0) {
		t.Fatal("0 was already seen")
	}
	if !w.Commit(1) || w.Commit(1) {
		t.Fatal("out of order accepted exactly once")
	}
	if !w.Commit(3000) {
		t.Fatal("3000 accepted")
	}
	if w.Check(952) || w.Check(3000-2048) {
		t.Fatal("too old must be rejected")
	}
	if !w.Check(953) {
		t.Fatal("highest-2047 is still inside the window")
	}
}

func TestReplayWindowLargeJumpClears(t *testing.T) {
	var w ReplayWindow
	w.Commit(0)
	w.Commit(5)
	if !w.Commit(5 + ReplayWindowSize) {
		t.Fatal("jump accepted")
	}
	if w.Check(0) || w.Check(5) {
		t.Fatal("old counters rejected after the jump")
	}
	if !w.Check(ReplayWindowSize + 4) {
		t.Fatal("counters just below the new highest are fresh")
	}
}

func TestReplayWindowCheckDoesNotMutate(t *testing.T) {
	var w ReplayWindow
	for i := 0; i < 3; i++ {
		if !w.Check(123) {
			t.Fatal("check must not consume the counter")
		}
	}
	w.Commit(123)
	if w.Check(123) {
		t.Fatal("committed counter is rejected")
	}
}

func TestReplayWindowNearMaxUint64(t *testing.T) {
	var w ReplayWindow
	if !w.Commit(math.MaxUint64-1) || !w.Commit(math.MaxUint64-2) || w.Commit(math.MaxUint64-2) {
		t.Fatal("near-max handling")
	}
}

func TestReplayWindowAgainstReferenceModel(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var w ReplayWindow
	seen := map[uint64]bool{}
	var highest uint64
	any := false
	for step := 0; step < 20000; step++ {
		base := highest
		var c uint64
		switch {
		case rng.Intn(3) == 0:
			c = base + uint64(rng.Intn(5000))
		case base > 3000:
			c = base - uint64(rng.Intn(3000))
		default:
			c = uint64(rng.Intn(3000))
		}
		want := !seen[c] && (!any || c > highest || highest-c < ReplayWindowSize)
		if got := w.Check(c); got != want {
			t.Fatalf("step %d Check(%d)=%v want %v", step, c, got, want)
		}
		if got := w.Commit(c); got != want {
			t.Fatalf("step %d Commit(%d)=%v want %v", step, c, got, want)
		}
		if want {
			seen[c] = true
			if !any || c > highest {
				highest = c
			}
			any = true
		}
	}
}

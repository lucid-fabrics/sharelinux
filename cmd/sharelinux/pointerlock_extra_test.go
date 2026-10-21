package main

import (
	"math"
	"testing"
)

func TestPointerLockRequest(t *testing.T) {
	tests := []struct {
		name string
		held bool
	}{
		{name: "held lock", held: true},
		{name: "requested but not held", held: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lock := pointerLock{idle: true}
			lock.request(test.held)

			if !lock.on {
				t.Fatal("request did not turn the lock on")
			}
			if lock.held != test.held {
				t.Fatalf("held is %v, want %v", lock.held, test.held)
			}
			if lock.idle {
				t.Fatal("request did not clear idle")
			}
		})
	}
}

func TestPointerLockRelease(t *testing.T) {
	tests := []struct {
		name   string
		warned bool
	}{
		{name: "not warned", warned: false},
		{name: "already warned", warned: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lock := pointerLock{
				on:     true,
				held:   true,
				idle:   true,
				warned: test.warned,
			}
			lock.release()

			if lock.on || lock.held || lock.idle {
				t.Fatalf("release left lock state on=%v held=%v idle=%v", lock.on, lock.held, lock.idle)
			}
			if lock.warned != test.warned {
				t.Fatalf("release changed warned to %v, want %v", lock.warned, test.warned)
			}
		})
	}
}

func TestPointerLockState(t *testing.T) {
	tests := []struct {
		name       string
		before     pointerLock
		locked     bool
		wantNotice lockNotice
		want       pointerLock
	}{
		{
			name:       "inactive ignores a lock report",
			before:     pointerLock{},
			locked:     true,
			wantNotice: lockNoticeNone,
			want:       pointerLock{},
		},
		{
			name:       "inactive ignores an unlock report",
			before:     pointerLock{},
			locked:     false,
			wantNotice: lockNoticeNone,
			want:       pointerLock{},
		},
		{
			name:       "held lock stays held",
			before:     pointerLock{on: true, held: true},
			locked:     true,
			wantNotice: lockNoticeNone,
			want:       pointerLock{on: true, held: true},
		},
		{
			name:       "unheld lock stays unheld",
			before:     pointerLock{on: true},
			locked:     false,
			wantNotice: lockNoticeNone,
			want:       pointerLock{on: true},
		},
		{
			name:       "lock begins",
			before:     pointerLock{on: true},
			locked:     true,
			wantNotice: lockNoticeLocked,
			want:       pointerLock{on: true, held: true},
		},
		{
			name:       "compositor ends lock",
			before:     pointerLock{on: true, held: true},
			locked:     false,
			wantNotice: lockNoticeEnded,
			want:       pointerLock{on: true, idle: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lock := test.before
			got := lock.state(test.locked)

			if got != test.wantNotice {
				t.Fatalf("notice is %v, want %v", got, test.wantNotice)
			}
			if lock != test.want {
				t.Fatalf("state is %+v, want %+v", lock, test.want)
			}
		})
	}
}

func TestPointerLockWantsRelock(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*pointerLock)
		want    bool
	}{
		{
			name:    "fresh lock",
			prepare: func(*pointerLock) {},
			want:    false,
		},
		{
			name: "held lock",
			prepare: func(lock *pointerLock) {
				lock.request(true)
			},
			want: false,
		},
		{
			name: "compositor ended lock",
			prepare: func(lock *pointerLock) {
				lock.request(true)
				lock.state(false)
			},
			want: true,
		},
		{
			name: "requested again after compositor ended lock",
			prepare: func(lock *pointerLock) {
				lock.request(true)
				lock.state(false)
				lock.request(false)
			},
			want: false,
		},
		{
			name: "released after compositor ended lock",
			prepare: func(lock *pointerLock) {
				lock.request(true)
				lock.state(false)
				lock.release()
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var lock pointerLock
			test.prepare(&lock)

			if got := lock.wantsRequest(); got != test.want {
				t.Fatalf("wantsRequest is %v, want %v", got, test.want)
			}
		})
	}
}

func TestPointerLockFailedOnce(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*pointerLock)
		wantFirst  bool
		wantSecond bool
	}{
		{
			name:       "first failure",
			prepare:    func(*pointerLock) {},
			wantFirst:  true,
			wantSecond: false,
		},
		{
			name: "second failure in the same stretch",
			prepare: func(lock *pointerLock) {
				_ = lock.failedOnce()
			},
			wantFirst:  false,
			wantSecond: false,
		},
		{
			name: "failure after leaving Exclusive mode",
			prepare: func(lock *pointerLock) {
				_ = lock.failedOnce()
				lock.leaveExclusive()
			},
			wantFirst:  true,
			wantSecond: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var lock pointerLock
			test.prepare(&lock)

			if got := lock.failedOnce(); got != test.wantFirst {
				t.Fatalf("first failedOnce is %v, want %v", got, test.wantFirst)
			}
			if got := lock.failedOnce(); got != test.wantSecond {
				t.Fatalf("second failedOnce is %v, want %v", got, test.wantSecond)
			}
		})
	}
}

func TestPointerLockLeaveExclusive(t *testing.T) {
	tests := []struct {
		name   string
		warned bool
	}{
		{name: "not warned", warned: false},
		{name: "warned", warned: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lock := pointerLock{warned: test.warned}
			lock.leaveExclusive()

			if lock.warned {
				t.Fatal("leaveExclusive did not clear warned")
			}
		})
	}
}

func TestX11LockResult(t *testing.T) {
	tests := []struct {
		name          string
		status        lockStatus
		confineResult bool
		wantHeld      bool
		wantRequested bool
		wantCalls     int
	}{
		{
			name:          "raw motion and barriers",
			status:        lockStatusOK,
			wantHeld:      true,
			wantRequested: true,
		},
		{
			name:          "no XInput with confinement available",
			status:        lockStatusNoXInput,
			confineResult: true,
			wantHeld:      false,
			wantRequested: true,
			wantCalls:     1,
		},
		{
			name:          "no XInput with confinement unavailable",
			status:        lockStatusNoXInput,
			confineResult: false,
			wantHeld:      false,
			wantRequested: false,
			wantCalls:     1,
		},
		{
			name:          "no barriers",
			status:        lockStatusNoBarriers,
			confineResult: true,
			wantHeld:      false,
			wantRequested: false,
			wantCalls:     0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			confine := func() bool {
				calls++
				return test.confineResult
			}

			held, requested := x11LockResult(test.status, confine)

			if held != test.wantHeld {
				t.Fatalf("held is %v, want %v", held, test.wantHeld)
			}
			if requested != test.wantRequested {
				t.Fatalf("requested is %v, want %v", requested, test.wantRequested)
			}
			if calls != test.wantCalls {
				t.Fatalf("confine was called %d times, want %d", calls, test.wantCalls)
			}
		})
	}
}

func TestXInputUsable(t *testing.T) {
	tests := []struct {
		name  string
		major int
		minor int
		want  bool
	}{
		{name: "XInput 2.0", major: 2, minor: 0, want: false},
		{name: "XInput 2.1", major: 2, minor: 1, want: true},
		{name: "XInput 2.2", major: 2, minor: 2, want: true},
		{name: "XInput 3.0", major: 3, minor: 0, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := xinputUsable(test.major, test.minor); got != test.want {
				t.Fatalf("xinputUsable(%d, %d) is %v, want %v", test.major, test.minor, got, test.want)
			}
		})
	}
}

func TestFiniteMotion(t *testing.T) {
	tests := []struct {
		name string
		dx   float64
		dy   float64
		want bool
	}{
		{name: "zero", dx: 0, dy: 0, want: true},
		{name: "finite motion", dx: 12.5, dy: -3.25, want: true},
		{name: "NaN horizontal", dx: math.NaN(), dy: 1, want: false},
		{name: "NaN vertical", dx: 1, dy: math.NaN(), want: false},
		{name: "both NaN", dx: math.NaN(), dy: math.NaN(), want: false},
		{name: "positive infinity horizontal", dx: math.Inf(1), dy: 1, want: false},
		{name: "negative infinity horizontal", dx: math.Inf(-1), dy: 1, want: false},
		{name: "positive infinity vertical", dx: 1, dy: math.Inf(1), want: false},
		{name: "negative infinity vertical", dx: 1, dy: math.Inf(-1), want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := finiteMotion(test.dx, test.dy); got != test.want {
				t.Fatalf("finiteMotion(%v, %v) is %v, want %v", test.dx, test.dy, got, test.want)
			}
		})
	}
}

func TestRelativePointerMoveRejectsNonFiniteMotion(t *testing.T) {
	view := fitViewport(1000, 1000, 500, 500)

	tests := []struct {
		name  string
		dx    float32
		dy    float32
		wantX int
		wantY int
	}{
		{
			name:  "finite motion",
			dx:    2,
			dy:    -2,
			wantX: 101,
			wantY: 119,
		},
		{
			name:  "NaN horizontal",
			dx:    float32(math.NaN()),
			dy:    1,
			wantX: 100,
			wantY: 120,
		},
		{
			name:  "NaN vertical",
			dx:    1,
			dy:    float32(math.NaN()),
			wantX: 100,
			wantY: 120,
		},
		{
			name:  "both NaN",
			dx:    float32(math.NaN()),
			dy:    float32(math.NaN()),
			wantX: 100,
			wantY: 120,
		},
		{
			name:  "positive infinity horizontal",
			dx:    float32(math.Inf(1)),
			dy:    1,
			wantX: 100,
			wantY: 120,
		},
		{
			name:  "negative infinity horizontal",
			dx:    float32(math.Inf(-1)),
			dy:    1,
			wantX: 100,
			wantY: 120,
		},
		{
			name:  "positive infinity vertical",
			dx:    1,
			dy:    float32(math.Inf(1)),
			wantX: 100,
			wantY: 120,
		},
		{
			name:  "negative infinity vertical",
			dx:    1,
			dy:    float32(math.Inf(-1)),
			wantX: 100,
			wantY: 120,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var pointer relativePointer
			pointer.set(100, 120)

			gotX, gotY := pointer.move(test.dx, test.dy, view, 500, 500)

			if gotX != test.wantX || gotY != test.wantY {
				t.Fatalf("move is %d,%d, want %d,%d", gotX, gotY, test.wantX, test.wantY)
			}
		})
	}
}

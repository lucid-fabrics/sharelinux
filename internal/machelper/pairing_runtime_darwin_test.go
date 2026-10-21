//go:build darwin

package machelper

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// These tests run the Go pairing client against the Swift runtime (the app's
// pairing coordinator, window and channel, with the in-memory key store) over
// a real loopback UDP socket.

func pairHarness(t *testing.T) *harness {
	t.Helper()
	return startHarnessWith(t, []string{"--pairing"})
}

// pairStatus asks the harness for the pairing status as key=value pairs.
func (h *harness) pairStatus() map[string]string {
	h.t.Helper()
	h.do("!pair status")
	h.mu.Lock()
	line := h.lastPair
	h.mu.Unlock()
	out := map[string]string{}
	for _, f := range strings.Fields(line) {
		if k, v, ok := strings.Cut(f, "="); ok {
			out[k] = v
		}
	}
	return out
}

func (h *harness) addr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(h.port)) }

// pace waits until the Mac's per-source handshake budget (one a second) has a token.
func (h *harness) pace() {
	if wait := 1100*time.Millisecond - time.Since(h.lastIK); wait > 0 {
		time.Sleep(wait)
	}
	h.lastIK = time.Now()
}

func (h *harness) connectPaced(static noise.DHKey) *Session {
	h.t.Helper()
	h.pace()
	return h.connect(static)
}

// tryConnect sends an IK initiation and reports whether the Mac answered.
func (h *harness) tryConnect(static noise.DHKey) bool {
	h.t.Helper()
	h.pace()
	h.drainReplies()
	_, m1, err := NewIKInitiation(static, h.pub, time.Now())
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.conn.Write(m1); err != nil {
		h.t.Fatal(err)
	}
	select {
	case <-h.replies:
		return true
	case <-time.After(400 * time.Millisecond):
		return false
	}
}

func runOptions() PairingOptions {
	return PairingOptions{AnswerTimeout: 3 * time.Second, DecisionTimeout: 10 * time.Second}
}

func codeOf(t *testing.T, r *PairingRun) string {
	t.Helper()
	select {
	case c, ok := <-r.Code():
		if !ok {
			t.Fatalf("no code: %v", r.Err())
		}
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no code in 5 s")
	}
	return ""
}

// waitStatus polls the harness until the pairing phase is want.
func (h *harness) waitPhase(want string) map[string]string {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := h.pairStatus()
		if s["phase"] == want || time.Now().After(deadline) {
			if s["phase"] != want {
				h.t.Fatalf("phase %q, want %q (%v)", s["phase"], want, s)
			}
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestInteropPairThenConnectThenRemoveThenRePair(t *testing.T) {
	linux := mustKey(t)
	h := pairHarness(t)
	if h.tryConnect(linux) {
		t.Fatal("an unpaired key was answered")
	}
	for round := 1; round <= 2; round++ {
		h.do("!pair open")
		if got := h.pairStatus()["phase"]; got != "waiting" {
			t.Fatalf("round %d: phase %q", round, got)
		}
		run, err := StartPairing(context.Background(), h.addr(), linux, "Desk PC", runOptions())
		if err != nil {
			t.Fatal(err)
		}
		code := codeOf(t, run)
		s := h.waitPhase("confirm")
		if s["code"] != code || s["device"] != "Desk_PC" {
			t.Fatalf("round %d: Mac shows %v, Linux shows %q", round, s, code)
		}
		macKey, err := run.Confirm()
		if err != nil || !bytes.Equal(macKey, h.pub) {
			t.Fatalf("Linux pinned %x, %v", macKey, err)
		}
		h.do("!pair confirm")
		if s := h.pairStatus(); s["devices"] != "1" || s["phase"] != "closed" {
			t.Fatalf("round %d after confirm: %v", round, s)
		}
		// The pinned keys now open a session, and the Mac moves its mouse for it.
		h.do("!toggle on")
		h.settle()
		sess := h.connectPaced(linux)
		h.settle()
		h.takePosts()
		h.send(sess, InputPacket{Type: PacketInput, DX: 32, DY: 0, Buttons: ButtonLeft})
		h.settle()
		if posts := strings.Join(h.takePosts(), "|"); !strings.Contains(posts, "move 2 0") || !strings.Contains(posts, "down 0") {
			t.Fatalf("round %d: posted %q", round, posts)
		}
		// Removing the device lifts the button and ends the session at once.
		h.do("!remove %s", hex.EncodeToString(linux.Public))
		h.settle()
		if posts := strings.Join(h.takePosts(), "|"); !strings.Contains(posts, "up 0") {
			t.Fatalf("round %d: removal did not release the button: %q", round, posts)
		}
		if st := h.pairStatus(); st["devices"] != "0" {
			t.Fatalf("round %d: still paired: %v", round, st)
		}
		h.send(sess, InputPacket{Type: PacketInput, DX: 32})
		h.settle()
		if posts := h.takePosts(); len(posts) != 0 {
			t.Fatalf("round %d: a removed device still moved the mouse: %q", round, posts)
		}
		if h.tryConnect(linux) {
			t.Fatalf("round %d: a removed device was answered", round)
		}
	}
}

func TestInteropWrongCodeMacRejects(t *testing.T) {
	linux := mustKey(t)
	h := pairHarness(t)
	h.do("!pair open")
	run, err := StartPairing(context.Background(), h.addr(), linux, "Desk PC", runOptions())
	if err != nil {
		t.Fatal(err)
	}
	codeOf(t, run)
	h.waitPhase("confirm")
	h.do("!pair reject")
	if s := h.pairStatus(); s["devices"] != "0" {
		t.Fatalf("a rejected device was stored: %v", s)
	}
	run.Reject() // the Linux person gives up as well
	if h.tryConnect(linux) {
		t.Fatal("a rejected key was answered")
	}
}

func TestInteropWrongCodeLinuxRejectsAndMacStillHasToDecide(t *testing.T) {
	linux := mustKey(t)
	h := pairHarness(t)
	h.do("!pair open")
	run, err := StartPairing(context.Background(), h.addr(), linux, "Desk PC", runOptions())
	if err != nil {
		t.Fatal(err)
	}
	codeOf(t, run)
	run.Reject()
	<-run.Done()
	if s := h.waitPhase("confirm"); s["devices"] != "0" {
		t.Fatalf("%v", s)
	}
	h.do("!pair reject")
	if h.tryConnect(linux) {
		t.Fatal("answered after both sides rejected")
	}
}

func TestInteropNoWindowNoAnswerAndNothingOpens(t *testing.T) {
	h := pairHarness(t)
	run, err := StartPairing(context.Background(), h.addr(), mustKey(t), "Desk PC", PairingOptions{AnswerTimeout: 400 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-run.Done()
	if !errors.Is(run.Err(), ErrNoAnswer) {
		t.Fatalf("err = %v", run.Err())
	}
	if s := h.pairStatus(); s["phase"] != "closed" {
		t.Fatalf("a network datagram opened the window: %v", s)
	}
}

// A second device from the same address while a code waits gets no answer and
// does not disturb the first one's code. (A second address is covered by the
// Swift tests: the harness listens on one loopback address only.)
func TestInteropASecondAttemptDoesNotDisturbAWaitingCode(t *testing.T) {
	h := pairHarness(t)
	h.do("!pair open")
	first, err := StartPairing(context.Background(), h.addr(), mustKey(t), "First", runOptions())
	if err != nil {
		t.Fatal(err)
	}
	codeOf(t, first)
	h.waitPhase("confirm")
	second, err := StartPairing(context.Background(), h.addr(), mustKey(t), "Second", PairingOptions{AnswerTimeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-second.Done()
	if !errors.Is(second.Err(), ErrNoAnswer) {
		t.Fatalf("the second attempt: %v", second.Err())
	}
	if s := h.pairStatus(); s["device"] != "First" || s["phase"] != "confirm" {
		t.Fatalf("%v", s)
	}
	first.Cancel()
}

// Mallory relays between a Linux client and the Swift Mac with two separate
// pairings: the people see different codes.
func TestInteropManInTheMiddleSeesDifferentCodesAndNothingIsStored(t *testing.T) {
	h := pairHarness(t)
	h.do("!pair open")
	linux, malloryKey := mustKey(t), mustKey(t)
	towardLinux := newMacPairer(t, malloryKey, rand.Reader)
	towardMac := mustPairing(t, malloryKey, "Desk PC")
	macConn, err := net.Dial("udp", h.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer macConn.Close()
	exchange := func(dg []byte, wantReply bool) []byte {
		if _, err := macConn.Write(dg); err != nil {
			t.Error(err)
			return nil
		}
		if !wantReply {
			return nil
		}
		buf := make([]byte, 1024)
		macConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := macConn.Read(buf)
		if err != nil {
			t.Errorf("mallory: %v", err)
			return nil
		}
		return buf[:n]
	}
	var mu sync.Mutex
	started := false
	mallory := newUDPServer(t, func(d []byte) [][]byte {
		mu.Lock()
		defer mu.Unlock()
		if !started {
			started = true
			m1, _ := towardMac.Start()
			m2 := exchange(m1, true)
			out, err := towardMac.Receive(m2)
			if err != nil || len(out) != 2 {
				t.Errorf("mallory leg: %v", err)
				return nil
			}
			exchange(out[0], false)
			rm := exchange(out[1], true)
			out, err = towardMac.Receive(rm)
			if err != nil {
				t.Errorf("mallory leg: %v", err)
				return nil
			}
			exchange(out[0], false)
		}
		reply, err := towardLinux.receive(d)
		if err != nil || reply == nil {
			return nil
		}
		return [][]byte{reply}
	})
	run, err := StartPairing(context.Background(), mallory.addr(), linux, "Desk PC", runOptions())
	if err != nil {
		t.Fatal(err)
	}
	linuxCode := codeOf(t, run)
	s := h.waitPhase("confirm")
	if s["code"] == linuxCode {
		t.Fatalf("both people would see %s", linuxCode)
	}
	run.Reject()
	h.do("!pair reject")
	if st := h.pairStatus(); st["devices"] != "0" {
		t.Fatalf("%v", st)
	}
	if h.tryConnect(linux) || h.tryConnect(malloryKey) {
		t.Fatal("someone was answered")
	}
}

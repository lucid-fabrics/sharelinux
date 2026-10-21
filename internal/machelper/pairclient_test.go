package machelper

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// udpServer runs handler for every datagram it receives and writes back what
// the handler returns. It counts what arrived.
type udpServer struct {
	conn     *net.UDPConn
	received atomic.Int64
	wg       sync.WaitGroup
}

func newUDPServer(t *testing.T, handler func(datagram []byte) [][]byte) *udpServer {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := &udpServer{conn: conn}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			s.received.Add(1)
			if handler == nil {
				continue
			}
			for _, reply := range handler(append([]byte(nil), buf[:n]...)) {
				conn.WriteToUDP(reply, from)
			}
		}
	}()
	t.Cleanup(func() { conn.Close(); s.wg.Wait() })
	return s
}

func (s *udpServer) addr() string { return s.conn.LocalAddr().String() }

// lockedMac lets the test read a macPairer that the server goroutine is driving.
type lockedMac struct {
	mu  sync.Mutex
	mac *macPairer
}

func (l *lockedMac) read(f func(*macPairer)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(l.mac)
}

func macHandler(l *lockedMac) func([]byte) [][]byte {
	return func(d []byte) [][]byte {
		l.mu.Lock()
		defer l.mu.Unlock()
		mac := l.mac
		reply, err := mac.receive(d)
		if err != nil || reply == nil {
			return nil
		}
		return [][]byte{reply}
	}
}

func shortOptions() PairingOptions {
	return PairingOptions{AnswerTimeout: 200 * time.Millisecond, DecisionTimeout: 2 * time.Second}
}

func waitCode(t *testing.T, r *PairingRun) string {
	t.Helper()
	select {
	case code, ok := <-r.Code():
		if !ok {
			t.Fatalf("run ended without a code: %v", r.Err())
		}
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("no code")
	}
	return ""
}

func TestPairingRunHonestAgainstAMacAndConfirmReturnsItsKey(t *testing.T) {
	macKey, linuxKey := mustKey(t), mustKey(t)
	locked := &lockedMac{mac: newMacPairer(t, macKey, rand.Reader)}
	server := newUDPServer(t, macHandler(locked))
	run, err := StartPairing(context.Background(), server.addr(), linuxKey, "Desk PC", shortOptions())
	if err != nil {
		t.Fatal(err)
	}
	code := waitCode(t, run)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var stage int
		locked.read(func(m *macPairer) { stage = m.stage })
		if stage == 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	locked.read(func(mac *macPairer) {
		if code != mac.code || len(code) != 6 {
			t.Errorf("codes differ: linux %q mac %q", code, mac.code)
		}
		if mac.name != "Desk PC" || !bytes.Equal(mac.peerStatic, linuxKey.Public) {
			t.Error("the Mac learned the wrong name or key")
		}
	})
	key, err := run.Confirm()
	if err != nil || !bytes.Equal(key, macKey.Public) {
		t.Fatalf("Confirm = %x, %v", key, err)
	}
	<-run.Done()
	if run.Err() != nil {
		t.Fatalf("a decided run has no error: %v", run.Err())
	}
	if got := run.DatagramsSent(); got != 4 {
		t.Fatalf("sent %d datagrams, want 4 (message 1, message 3, commit, reveal)", got)
	}
	if _, err := run.Confirm(); !errors.Is(err, ErrNotAwaitingDecision) {
		t.Fatalf("a second Confirm: %v", err)
	}
}

func TestPairingRunRejectPinsNothing(t *testing.T) {
	server := newUDPServer(t, macHandler(&lockedMac{mac: newMacPairer(t, mustKey(t), rand.Reader)}))
	run, err := StartPairing(context.Background(), server.addr(), mustKey(t), "Desk PC", shortOptions())
	if err != nil {
		t.Fatal(err)
	}
	waitCode(t, run)
	run.Reject()
	<-run.Done()
	if _, err := run.Confirm(); !errors.Is(err, ErrNotAwaitingDecision) {
		t.Fatalf("Confirm after Reject: %v", err)
	}
	if run.Err() != nil {
		t.Fatalf("rejecting is not an error: %v", run.Err())
	}
}

// A silent Mac: the client sent message 1 once and stays quiet. No resend.
func TestPairingRunNeverRetriesWhenTheMacIsSilent(t *testing.T) {
	server := newUDPServer(t, nil)
	run, err := StartPairing(context.Background(), server.addr(), mustKey(t), "Desk PC", PairingOptions{AnswerTimeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-run.Done()
	if !errors.Is(run.Err(), ErrNoAnswer) {
		t.Fatalf("err = %v, want no answer", run.Err())
	}
	if _, open := <-run.Code(); open {
		t.Fatal("a code appeared from nowhere")
	}
	time.Sleep(600 * time.Millisecond) // four timeouts' worth: a retrying client would have resent by now
	if got := server.received.Load(); got != 1 {
		t.Fatalf("the Mac received %d datagrams, want exactly 1", got)
	}
	if run.DatagramsSent() != 1 {
		t.Fatalf("sent %d", run.DatagramsSent())
	}
}

func TestPairingRunStopsAtAWrongReplyAndSendsNothingMore(t *testing.T) {
	server := newUDPServer(t, func([]byte) [][]byte { return [][]byte{bytes.Repeat([]byte{0x42}, 97)} })
	run, err := StartPairing(context.Background(), server.addr(), mustKey(t), "Desk PC", shortOptions())
	if err != nil {
		t.Fatal(err)
	}
	<-run.Done()
	if run.Err() == nil {
		t.Fatal("garbage was accepted")
	}
	time.Sleep(300 * time.Millisecond)
	if server.received.Load() != 1 {
		t.Fatalf("the Mac received %d datagrams, want 1", server.received.Load())
	}
}

func TestPairingRunSaysWhenNothingListens(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	conn.Close()
	run, err := StartPairing(context.Background(), addr, mustKey(t), "Desk PC", shortOptions())
	if err != nil {
		t.Fatal(err)
	}
	<-run.Done()
	if !errors.Is(run.Err(), ErrRefused) {
		t.Fatalf("err = %v, want refused", run.Err())
	}
}

func TestPairingRunCancelEndsItAtOnce(t *testing.T) {
	server := newUDPServer(t, nil)
	run, err := StartPairing(context.Background(), server.addr(), mustKey(t), "Desk PC", PairingOptions{AnswerTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	run.Cancel()
	<-run.Done()
	if time.Since(start) > time.Second || !errors.Is(run.Err(), ErrCancelled) {
		t.Fatalf("cancel took %v, err %v", time.Since(start), run.Err())
	}
}

func TestPairingRunRefusesABadNameOrAddressBeforeSendingAnything(t *testing.T) {
	server := newUDPServer(t, nil)
	if _, err := StartPairing(context.Background(), server.addr(), mustKey(t), "bad​name", shortOptions()); !errors.Is(err, ErrBadDeviceName) {
		t.Fatalf("zero-width name: %v", err)
	}
	if _, err := StartPairing(context.Background(), "", mustKey(t), "ok", shortOptions()); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("empty address: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if server.received.Load() != 0 {
		t.Fatal("something was sent")
	}
}

func TestNormalizeHelperAddress(t *testing.T) {
	cases := map[string]string{
		"mac.local":      "mac.local:5961",
		"10.0.0.5":       "10.0.0.5:5961",
		"10.0.0.5:6000":  "10.0.0.5:6000",
		"[fe80::1]:5961": "[fe80::1]:5961",
		"fe80::1":        "[fe80::1]:5961",
		"[fe80::1]":      "[fe80::1]:5961",
	}
	for in, want := range cases {
		got, err := NormalizeHelperAddress(in)
		if err != nil && in != "fe80::1" {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if err == nil && got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", ":5961", "host:0", "host:99999", "host:abc"} {
		if _, err := NormalizeHelperAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A man in the middle relays between the Linux client and the real Mac with
// two separate pairings. The two people see different codes, so whichever of
// them rejects, nothing is pinned, and nothing a Mallory knows lets him pick
// the codes.
func TestPairingRunManInTheMiddleShowsDifferentCodes(t *testing.T) {
	realMacKey, linuxKey, malloryKey := mustKey(t), mustKey(t), mustKey(t)
	realMac := newMacPairer(t, realMacKey, rand.Reader)
	towardLinux := newMacPairer(t, malloryKey, rand.Reader)
	towardMac := mustPairing(t, malloryKey, "Desk PC")
	var mu sync.Mutex
	started := false
	mallory := newUDPServer(t, func(d []byte) [][]byte {
		mu.Lock()
		defer mu.Unlock()
		if !started {
			started = true
			// Mallory's own pairing with the real Mac, run to the end at once.
			m1, _ := towardMac.Start()
			m2, err := realMac.receive(m1)
			if err != nil {
				t.Errorf("mallory leg 1: %v", err)
				return nil
			}
			out, err := towardMac.Receive(m2)
			if err != nil || len(out) != 2 {
				t.Errorf("mallory leg 2: %v", err)
				return nil
			}
			realMac.receive(out[0])
			rm, err := realMac.receive(out[1])
			if err != nil {
				t.Errorf("mallory leg 3: %v", err)
				return nil
			}
			out, err = towardMac.Receive(rm)
			if err != nil {
				t.Errorf("mallory leg 4: %v", err)
				return nil
			}
			realMac.receive(out[0])
		}
		reply, err := towardLinux.receive(d)
		if err != nil || reply == nil {
			return nil
		}
		return [][]byte{reply}
	})
	run, err := StartPairing(context.Background(), mallory.addr(), linuxKey, "Desk PC", shortOptions())
	if err != nil {
		t.Fatal(err)
	}
	linuxCode := waitCode(t, run)
	mu.Lock()
	macCode, macSawKey := realMac.code, realMac.peerStatic
	mu.Unlock()
	if macCode == "" || macCode == linuxCode {
		t.Fatalf("the people would compare %q and %q", linuxCode, macCode)
	}
	if bytes.Equal(macSawKey, linuxKey.Public) {
		t.Fatal("the Mac was shown the Linux device's key through a man in the middle")
	}
	run.Reject()
	<-run.Done()
	if _, err := run.Confirm(); err == nil {
		t.Fatal("Confirm after Reject returned a key")
	}
	var _ noise.DHKey = linuxKey
}

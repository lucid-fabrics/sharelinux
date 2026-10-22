package machelper

import (
	"errors"
	"net"
	"testing"
	"time"
)

// restartMac brings a Mac back on the address of one that was closed, with the same key.
func restartMac(t *testing.T, old *fakeMac) *fakeMac {
	t.Helper()
	udp := old.conn.LocalAddr().(*net.UDPAddr)
	var conn *net.UDPConn
	var err error
	for range 50 {
		if conn, err = net.ListenUDP("udp", udp); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMac{t: t, conn: conn, static: old.static, stop: make(chan struct{})}
	go m.serve()
	t.Cleanup(m.close)
	return m
}

func (m *fakeMac) pressesDown() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.inputs {
		if p.Buttons != 0 {
			n++
		}
	}
	return n
}

// holdThroughOutage holds the left button, kills the Mac until Send reports it refused (the
// ENETUNREACH-style non-silent outage), then returns with the Mac gone and the button still held.
func holdThroughOutage(t *testing.T) (*Link, *statusLog, *fakeMac) {
	t.Helper()
	m := newFakeMac(t)
	timing := fastTiming()
	timing.RepeatHeld = 60 * time.Millisecond
	link, log := startTestLink(t, m, timing)
	log.await(t, 2*time.Second, isActive)
	if err := link.Send(InputPacket{Buttons: ButtonLeft}); err != nil {
		t.Fatal(err)
	}
	m.close()
	var err error
	for start := time.Now(); err == nil && time.Since(start) < time.Second; time.Sleep(2 * time.Millisecond) {
		err = link.Send(InputPacket{Buttons: ButtonLeft})
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("send error = %v, want refused", err)
	}
	return link, log, m
}

// The release that a dead link cannot carry (it went to Screen Sharing instead) must still
// clear the repeat: no press reaches the Mac after the reconnect.
func TestLinkReleaseDuringOutageNeverRepeatsAfterReconnect(t *testing.T) {
	link, log, old := holdThroughOutage(t)
	if err := link.Send(InputPacket{}); err == nil {
		t.Fatal("the dead link should refuse the release")
	}
	m2 := restartMac(t, old)
	log.await(t, 3*time.Second, isActive)
	time.Sleep(500 * time.Millisecond)
	if n := m2.pressesDown(); n != 0 {
		t.Fatalf("%d presses reached the Mac after the reconnect", n)
	}
}

// Without any fresh press, a reconnect (a Mac killed and switched on again) sends no press even
// though the Linux side still holds the button.
func TestLinkStillHeldButtonIsNotPressedAgainByANewSession(t *testing.T) {
	link, log, old := holdThroughOutage(t)
	m2 := restartMac(t, old)
	log.await(t, 3*time.Second, isActive)
	time.Sleep(500 * time.Millisecond)
	if n := m2.pressesDown(); n != 0 {
		t.Fatalf("%d presses reached the Mac without a fresh press", n)
	}
	// A press made after the session began is delivered and repeated.
	if err := link.Send(InputPacket{}); err != nil {
		t.Fatal(err)
	}
	if err := link.Send(InputPacket{Buttons: ButtonLeft}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if n := m2.pressesDown(); n < 3 {
		t.Fatalf("%d presses after a fresh press, want it repeated", n)
	}
}

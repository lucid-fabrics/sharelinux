package machelper

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// fakeMac is the Mac end of the IK session on a loopback socket: it answers
// handshakes, opens transport datagrams and replies with a status the way
// the real helper does (to keepalives and to refused input, not to accepted input).
type fakeMac struct {
	t      *testing.T
	conn   *net.UDPConn
	static noise.DHKey

	mu        sync.Mutex
	active    *Session
	pending   *Session
	state     HelperState
	mute      bool
	inputs    []InputPacket
	keepalive int
	byes      int
	handshake int
	stop      chan struct{}
	lastStat  time.Time
}

func newFakeMac(t *testing.T) *fakeMac {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMac{t: t, conn: conn, static: mustKey(t), stop: make(chan struct{})}
	go m.serve()
	t.Cleanup(m.close)
	return m
}

func (m *fakeMac) addr() string { return m.conn.LocalAddr().String() }

func (m *fakeMac) close() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
		m.conn.Close()
	}
}

func (m *fakeMac) set(state HelperState) {
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
}

func (m *fakeMac) setMute(on bool) {
	m.mu.Lock()
	m.mute = on
	m.mu.Unlock()
}

// forget drops every session, like a helper that restarted.
func (m *fakeMac) forget() {
	m.mu.Lock()
	m.active, m.pending = nil, nil
	m.mu.Unlock()
}

func (m *fakeMac) counts() (inputs, keepalives, byes, handshakes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs), m.keepalive, m.byes, m.handshake
}

func (m *fakeMac) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		datagram := append([]byte(nil), buf[:n]...)
		m.mu.Lock()
		reply := m.handle(datagram)
		mute := m.mute
		m.mu.Unlock()
		if reply != nil && !mute {
			m.conn.WriteToUDP(reply, from)
		}
	}
}

func (m *fakeMac) handle(datagram []byte) []byte {
	now := time.Now()
	kind, _, ok := ParseDatagram(datagram)
	if !ok {
		return nil
	}
	switch kind {
	case KindIKInitiation:
		res, err := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: m.static})
		if err != nil {
			return nil
		}
		if _, err := res.Read(datagram); err != nil {
			return nil
		}
		answer, err := res.Write(nil)
		if err != nil {
			return nil
		}
		session, err := res.Session(now)
		if err != nil {
			return nil
		}
		m.pending = session
		m.handshake++
		return answer
	case KindTransport:
		var plain []byte
		var err error
		if m.active != nil {
			plain, err = m.active.Open(datagram, InputPacketSize, now)
		}
		if (m.active == nil || err != nil) && m.pending != nil {
			plain, err = m.pending.Open(datagram, InputPacketSize, now)
			if err == nil {
				m.active, m.pending = m.pending, nil
			}
		}
		if err != nil || plain == nil {
			return nil
		}
		p, err := DecodeInputPacket(plain)
		if err != nil {
			return nil
		}
		switch p.Type {
		case PacketBye:
			m.byes++
			m.active = nil
			return nil
		case PacketInput:
			m.inputs = append(m.inputs, p)
			if m.state == StateActive {
				return nil // accepted input is not answered
			}
		case PacketKeepalive:
			m.keepalive++
		}
		if now.Sub(m.lastStat) < 40*time.Millisecond && m.state == StateActive {
			return nil
		}
		m.lastStat = now
		sealed, err := m.active.Seal(StatusPacket{State: m.state}.Encode(), now)
		if err != nil {
			return nil
		}
		return sealed
	}
	return nil
}

type statusLog struct {
	mu  sync.Mutex
	all []LinkStatus
	ch  chan LinkStatus
}

func newStatusLog() *statusLog { return &statusLog{ch: make(chan LinkStatus, 64)} }

func (s *statusLog) record(st LinkStatus) {
	s.mu.Lock()
	s.all = append(s.all, st)
	s.mu.Unlock()
	select {
	case s.ch <- st:
	default:
	}
}

// await waits for a status satisfying want.
func (s *statusLog) await(t *testing.T, within time.Duration, want func(LinkStatus) bool) LinkStatus {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case st := <-s.ch:
			if want(st) {
				return st
			}
		case <-deadline:
			t.Fatalf("no matching link status within %v; saw %+v", within, s.snapshot())
		}
	}
}

func (s *statusLog) snapshot() []LinkStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]LinkStatus(nil), s.all...)
}

func fastTiming() LinkTiming {
	return LinkTiming{Keepalive: 20 * time.Millisecond, StatusTimeout: 120 * time.Millisecond,
		Handshake: 200 * time.Millisecond, Retry: 100 * time.Millisecond}
}

func startTestLink(t *testing.T, m *fakeMac, timing LinkTiming) (*Link, *statusLog) {
	t.Helper()
	log := newStatusLog()
	link, err := StartLink(LinkConfig{Address: m.addr(), Static: mustKey(t), MacKey: m.static.Public,
		OnStatus: log.record, Timing: timing})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(link.Close)
	return link, log
}

func isActive(s LinkStatus) bool { return s.Active() }

func TestLinkGoesLiveAndSendsKeepalives(t *testing.T) {
	m := newFakeMac(t)
	link, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	time.Sleep(150 * time.Millisecond)
	_, keepalives, _, handshakes := m.counts()
	if keepalives < 4 {
		t.Fatalf("keepalives = %d, want a steady stream", keepalives)
	}
	if handshakes != 1 {
		t.Fatalf("handshakes = %d, want exactly one", handshakes)
	}
	if !link.Status().Active() {
		t.Fatalf("status %+v", link.Status())
	}
}

func TestLinkFirstPacketIsAKeepaliveNotInput(t *testing.T) {
	m := newFakeMac(t)
	_, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	inputs, _, _, _ := m.counts()
	if inputs != 0 {
		t.Fatalf("%d input packets before anyone moved", inputs)
	}
}

func TestLinkReportsEveryHelperState(t *testing.T) {
	for _, state := range []HelperState{StateDisabled, StateLocked, StateNoSession, StateNotConsole, StateRateLimited, StateNeedsGrant} {
		m := newFakeMac(t)
		m.set(state)
		_, log := startTestLink(t, m, fastTiming())
		st := log.await(t, 2*time.Second, func(s LinkStatus) bool { return s.State == LinkLive })
		if st.Helper != state || st.Active() {
			t.Fatalf("state %d reported as %+v", state, st)
		}
	}
}

func TestLinkFollowsTheHelperGate(t *testing.T) {
	m := newFakeMac(t)
	_, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	m.set(StateDisabled)
	log.await(t, time.Second, func(s LinkStatus) bool { return s.Helper == StateDisabled && s.State == LinkLive })
	m.set(StateActive)
	log.await(t, time.Second, isActive)
}

func TestLinkSendsInputAndClampsIt(t *testing.T) {
	m := newFakeMac(t)
	link, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	if err := link.Send(InputPacket{Buttons: ButtonLeft, DX: 100, DY: -50, SX: 30000}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		inputs, _, _, _ := m.counts()
		if inputs == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inputs) != 1 {
		t.Fatalf("inputs = %d", len(m.inputs))
	}
	got := m.inputs[0]
	if got.DX != 100 || got.DY != -50 || got.Buttons != ButtonLeft || got.SX != MaxScroll {
		t.Fatalf("packet = %+v", got)
	}
}

func TestLinkKilledHelperIsRefusedAtOnce(t *testing.T) {
	m := newFakeMac(t)
	link, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	m.close()
	// The first datagram after the port closed comes back as "refused": no
	// waiting for the status timeout.
	start := time.Now()
	var err error
	for err == nil && time.Since(start) < time.Second {
		err = link.Send(InputPacket{DX: 16})
		time.Sleep(2 * time.Millisecond)
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("send error = %v, want refused", err)
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("detected after %v", took)
	}
	st := link.Status()
	if st.State != LinkDown || !errors.Is(st.Reason, ErrRefused) {
		t.Fatalf("status = %+v", st)
	}
}

func TestLinkSilentHelperGoesDownByTimeout(t *testing.T) {
	m := newFakeMac(t)
	_, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	m.setMute(true)
	st := log.await(t, time.Second, func(s LinkStatus) bool { return s.State == LinkDown })
	if !errors.Is(st.Reason, ErrSilent) {
		t.Fatalf("reason = %v", st.Reason)
	}
	m.setMute(false)
	log.await(t, 2*time.Second, isActive)
}

func TestLinkReconnectsAfterTheHelperForgotTheSession(t *testing.T) {
	m := newFakeMac(t)
	_, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	m.forget() // a restarted helper: our keys mean nothing to it, and it says nothing
	log.await(t, time.Second, func(s LinkStatus) bool { return s.State == LinkDown })
	log.await(t, 3*time.Second, isActive)
	if _, _, _, handshakes := m.counts(); handshakes < 2 {
		t.Fatalf("handshakes = %d, want a new one", handshakes)
	}
}

func TestLinkNoAnswerToTheHandshakeStaysDownAndRetries(t *testing.T) {
	m := newFakeMac(t)
	m.setMute(true)
	_, log := startTestLink(t, m, fastTiming())
	log.await(t, time.Second, func(s LinkStatus) bool { return s.State == LinkDown && errors.Is(s.Reason, ErrNoAnswer) })
	time.Sleep(350 * time.Millisecond)
	if _, _, _, handshakes := m.counts(); handshakes < 2 {
		t.Fatalf("handshakes = %d, want the link to keep trying", handshakes)
	}
	m.setMute(false)
	log.await(t, 3*time.Second, isActive)
}

func TestLinkRekeysBeforeTheSessionExpires(t *testing.T) {
	m := newFakeMac(t)
	timing := fastTiming()
	timing.RekeyAfter = 150 * time.Millisecond
	timing.RekeyLatest = time.Second
	link, log := startTestLink(t, m, timing)
	log.await(t, 2*time.Second, isActive)
	time.Sleep(500 * time.Millisecond)
	if _, _, _, handshakes := m.counts(); handshakes < 2 {
		t.Fatalf("handshakes = %d, want a rekey", handshakes)
	}
	if !link.Status().Active() {
		t.Fatalf("status after rekey: %+v", link.Status())
	}
}

func TestLinkRekeyWaitsForButtonsUnlessItMust(t *testing.T) {
	m := newFakeMac(t)
	timing := fastTiming()
	timing.RekeyAfter = 100 * time.Millisecond
	timing.RekeyLatest = 600 * time.Millisecond
	link, log := startTestLink(t, m, timing)
	log.await(t, 2*time.Second, isActive)
	if err := link.Send(InputPacket{Buttons: ButtonLeft}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, _, _, handshakes := m.counts(); handshakes != 1 {
		t.Fatalf("rekeyed with a button held and time to spare (%d handshakes)", handshakes)
	}
	// A release lets it go ahead.
	if err := link.Send(InputPacket{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, _, _, handshakes := m.counts(); handshakes < 2 {
		t.Fatalf("handshakes = %d, want a rekey after the release", handshakes)
	}
}

func TestLinkRekeyForcedBeforeExpiryEvenWithAButtonHeld(t *testing.T) {
	m := newFakeMac(t)
	timing := fastTiming()
	timing.RekeyAfter = 100 * time.Millisecond
	timing.RekeyLatest = 300 * time.Millisecond
	link, log := startTestLink(t, m, timing)
	log.await(t, 2*time.Second, isActive)
	link.Send(InputPacket{Buttons: ButtonLeft})
	time.Sleep(700 * time.Millisecond)
	if _, _, _, handshakes := m.counts(); handshakes < 2 {
		t.Fatalf("handshakes = %d, want a forced rekey", handshakes)
	}
}

func TestLinkCloseSaysGoodbyeAndSendFails(t *testing.T) {
	m := newFakeMac(t)
	link, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	link.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, _, byes, _ := m.counts(); byes > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, _, byes, _ := m.counts(); byes == 0 {
		t.Fatal("no bye reached the Mac")
	}
	if err := link.Send(InputPacket{DX: 1}); !errors.Is(err, ErrLinkNotReady) {
		t.Fatalf("send after close: %v", err)
	}
	link.Close() // twice is fine
}

func TestLinkSendBeforeTheHandshakeIsRefused(t *testing.T) {
	m := newFakeMac(t)
	m.setMute(true)
	link, _ := startTestLink(t, m, fastTiming())
	if err := link.Send(InputPacket{DX: 1}); !errors.Is(err, ErrLinkNotReady) {
		t.Fatalf("send: %v", err)
	}
}

func TestLinkPointerLockedFlagTravels(t *testing.T) {
	m := newFakeMac(t)
	link, log := startTestLink(t, m, fastTiming())
	log.await(t, 2*time.Second, isActive)
	link.SetPointerLocked(true)
	link.Send(InputPacket{DX: 16})
	time.Sleep(60 * time.Millisecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inputs) == 0 || m.inputs[len(m.inputs)-1].Flags != FlagPointerLocked {
		t.Fatalf("inputs %+v", m.inputs)
	}
}

func TestStartLinkRejectsBadArguments(t *testing.T) {
	if _, err := StartLink(LinkConfig{Address: "", MacKey: make([]byte, 32)}); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("empty address: %v", err)
	}
	if _, err := StartLink(LinkConfig{Address: "127.0.0.1", MacKey: make([]byte, 5)}); !errors.Is(err, ErrBadRemoteStatic) {
		t.Fatalf("short key: %v", err)
	}
}

func TestLinkWrongMacKeyNeverGoesLive(t *testing.T) {
	m := newFakeMac(t)
	log := newStatusLog()
	other := mustKey(t)
	link, err := StartLink(LinkConfig{Address: m.addr(), Static: mustKey(t), MacKey: other.Public,
		OnStatus: log.record, Timing: fastTiming()})
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	time.Sleep(400 * time.Millisecond)
	for _, s := range log.snapshot() {
		if s.State == LinkLive {
			t.Fatalf("went live against a Mac with another key: %+v", s)
		}
	}
}

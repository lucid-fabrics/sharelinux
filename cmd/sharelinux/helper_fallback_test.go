package main

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/nekohasekai/open-screen-sharing/internal/machelper"
)

// macStub is the helper's end of a session on loopback: it answers the IK
// handshake, records the input it is sent and answers keepalives with its
// current state, like the real helper.
type macStub struct {
	conn   *net.UDPConn
	static noise.DHKey

	mu      sync.Mutex
	active  *machelper.Session
	pending *machelper.Session
	state   machelper.HelperState
	inputs  []machelper.InputPacket
	last    time.Time
}

func newMacStub(t *testing.T) *macStub {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	key, err := machelper.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	m := &macStub{conn: conn, static: key}
	go m.serve()
	t.Cleanup(func() { conn.Close() })
	return m
}

func (m *macStub) setState(s machelper.HelperState) {
	m.mu.Lock()
	m.state = s
	m.mu.Unlock()
}

func (m *macStub) lastInput() (machelper.InputPacket, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inputs) == 0 {
		return machelper.InputPacket{}, false
	}
	return m.inputs[len(m.inputs)-1], true
}

func (m *macStub) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		m.mu.Lock()
		reply := m.handle(append([]byte(nil), buf[:n]...))
		m.mu.Unlock()
		if reply != nil {
			m.conn.WriteToUDP(reply, from)
		}
	}
}

func (m *macStub) handle(datagram []byte) []byte {
	now := time.Now()
	kind, _, ok := machelper.ParseDatagram(datagram)
	if !ok {
		return nil
	}
	switch kind {
	case machelper.KindIKInitiation:
		hs, err := machelper.NewHandshake(machelper.HandshakeConfig{Pattern: machelper.PatternIK, Static: m.static})
		if err != nil {
			return nil
		}
		if _, err = hs.Read(datagram); err != nil {
			return nil
		}
		answer, err := hs.Write(nil)
		if err != nil {
			return nil
		}
		if m.pending, err = hs.Session(now); err != nil {
			return nil
		}
		return answer
	case machelper.KindTransport:
		var plain []byte
		var err error
		if m.active != nil {
			plain, err = m.active.Open(datagram, machelper.InputPacketSize, now)
		}
		if (m.active == nil || err != nil) && m.pending != nil {
			if plain, err = m.pending.Open(datagram, machelper.InputPacketSize, now); err == nil {
				m.active, m.pending = m.pending, nil
			}
		}
		if err != nil || plain == nil {
			return nil
		}
		p, err := machelper.DecodeInputPacket(plain)
		if err != nil {
			return nil
		}
		if p.Type == machelper.PacketInput {
			m.inputs = append(m.inputs, p)
			if m.state == machelper.StateActive {
				return nil
			}
		}
		if p.Type == machelper.PacketBye || now.Sub(m.last) < 40*time.Millisecond && m.state == machelper.StateActive {
			return nil
		}
		m.last = now
		sealed, err := m.active.Seal(machelper.StatusPacket{State: m.state}.Encode(), now)
		if err != nil {
			return nil
		}
		return sealed
	}
	return nil
}

// driver is the GUI's glue without the GUI: link status in, routing decision out.
type driver struct {
	t      *testing.T
	link   *machelper.Link
	route  helperRoute
	mu     sync.Mutex
	status machelper.LinkStatus
	events chan routeChange
}

func newDriver(t *testing.T, m *macStub) *driver {
	t.Helper()
	d := &driver{t: t, events: make(chan routeChange, 16)}
	d.route.send = func(p machelper.InputPacket) error { return d.link.Send(p) }
	client, err := machelper.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	d.link, err = machelper.StartLink(machelper.LinkConfig{
		Address: m.conn.LocalAddr().String(), Static: client, MacKey: m.static.Public,
		OnStatus: func(st machelper.LinkStatus) {
			d.mu.Lock()
			d.status = st
			d.mu.Unlock()
		},
		Timing: machelper.LinkTiming{Keepalive: 20 * time.Millisecond, StatusTimeout: 150 * time.Millisecond,
			Handshake: 200 * time.Millisecond, Retry: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.link.Close)
	return d
}

// step is one pass of the GUI's reroute.
func (d *driver) step(lockHeld bool) routeChange {
	d.mu.Lock()
	active := d.status.Active()
	d.mu.Unlock()
	return d.route.decide(routeInputs{lockHeld: lockHeld, enabled: true, linkActive: active})
}

func (d *driver) waitEngaged() {
	d.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d.step(true) == routeEngaged {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	d.t.Fatal("the helper never became active")
}

func TestFallbackWhenTheHelperIsKilledMidSession(t *testing.T) {
	m := newMacStub(t)
	d := newDriver(t, m)
	d.waitEngaged()
	d.route.button(machelper.ButtonLeft, true)
	d.route.motion(10, 0)
	if err := d.route.flush(); err != nil {
		t.Fatal(err)
	}
	m.conn.Close() // the helper is killed with the button down
	killed := time.Now()
	var dropped time.Duration
	for time.Since(killed) < time.Second {
		d.route.motion(1, 0)
		if err := d.route.flush(); err != nil {
			d.step(true)
		} else if d.step(true) == routeDropped {
			dropped = time.Since(killed)
			break
		}
		if d.route.mode == routeRFB {
			dropped = time.Since(killed)
			break
		}
		time.Sleep(4 * time.Millisecond)
	}
	if d.route.mode != routeRFB || dropped == 0 {
		t.Fatal("routing never fell back")
	}
	if dropped > 250*time.Millisecond {
		t.Fatalf("fell back after %v, want within 250 ms", dropped)
	}
	// The press never reached Screen Sharing, so its release must not either.
	if !d.route.swallowRelease(machelper.ButtonLeft) {
		t.Fatal("the release of the helper's button would reach Screen Sharing")
	}
}

func TestFallbackWhenTheMacTurnsTheHelperOff(t *testing.T) {
	m := newMacStub(t)
	d := newDriver(t, m)
	d.waitEngaged()
	d.route.button(machelper.ButtonLeft, true)
	if err := d.route.flush(); err != nil {
		t.Fatal(err)
	}
	m.setState(machelper.StateDisabled)
	turnedOff := time.Now()
	for d.route.mode == routeHelper && time.Since(turnedOff) < time.Second {
		// Input keeps flowing while the Mac refuses it; the refusal's status brings the drop.
		d.route.motion(1, 0)
		_ = d.route.flush()
		d.step(true)
		time.Sleep(4 * time.Millisecond)
	}
	if d.route.mode != routeRFB {
		t.Fatal("routing never fell back")
	}
	if took := time.Since(turnedOff); took > 250*time.Millisecond+50*time.Millisecond {
		t.Fatalf("fell back after %v", took)
	}
	// The Mac lifts the buttons itself when it refuses input; this side must not
	// press them again over Screen Sharing when the person lets go.
	if !d.route.swallowRelease(machelper.ButtonLeft) {
		t.Fatal("the release of the helper's button would reach Screen Sharing")
	}
	sent := d.link.SentInput()
	time.Sleep(60 * time.Millisecond)
	if d.link.SentInput() != sent {
		t.Fatal("input kept going to a helper that refuses it")
	}
}

func TestLockReleaseEndsHelperRoutingAndLiftsTheButton(t *testing.T) {
	m := newMacStub(t)
	d := newDriver(t, m)
	d.waitEngaged()
	d.route.button(machelper.ButtonRight, true)
	d.route.motion(3, 3)
	if err := d.route.flush(); err != nil {
		t.Fatal(err)
	}
	if d.step(false) != routeDropped {
		t.Fatal("lock release did not end routing")
	}
	time.Sleep(50 * time.Millisecond)
	last, ok := m.lastInput()
	if !ok || last.Buttons != 0 {
		t.Fatalf("last packet at the Mac: %+v, want buttons up", last)
	}
	// Taking the lock again engages again while the helper is up.
	deadline := time.Now().Add(time.Second)
	for d.step(true) != routeEngaged {
		if time.Now().After(deadline) {
			t.Fatal("no re-engage after the lock came back")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

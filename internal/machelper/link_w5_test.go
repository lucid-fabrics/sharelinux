package machelper

import (
	"errors"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"
)

// jitterConn delays every datagram it carries by base plus or minus spread (so some overtake
// others, like Wi-Fi) and drops a share of them. It stands in for tc netem.
type jitterConn struct {
	net.Conn
	base, spread time.Duration
	loss         float64

	mu   sync.Mutex
	rng  *rand.Rand
	in   chan []byte
	done chan struct{}
	once sync.Once
}

func newJitterConn(c net.Conn, base, spread time.Duration, loss float64, seed int64) *jitterConn {
	j := &jitterConn{Conn: c, base: base, spread: spread, loss: loss, rng: rand.New(rand.NewSource(seed)),
		in: make(chan []byte, 256), done: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := c.Read(buf)
			if err != nil {
				j.once.Do(func() { close(j.done) })
				return
			}
			data := append([]byte(nil), buf[:n]...)
			if lost, delay := j.roll(); !lost {
				time.AfterFunc(delay, func() {
					select {
					case j.in <- data:
					case <-j.done:
					}
				})
			}
		}
	}()
	return j
}

func (j *jitterConn) roll() (bool, time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	lost := j.rng.Float64() < j.loss
	delay := j.base + time.Duration((j.rng.Float64()*2-1)*float64(j.spread))
	return lost, max(delay, 0)
}

func (j *jitterConn) Read(b []byte) (int, error) {
	select {
	case data := <-j.in:
		return copy(b, data), nil
	case <-j.done:
		return 0, net.ErrClosed
	}
}

func (j *jitterConn) Write(b []byte) (int, error) {
	lost, delay := j.roll()
	if lost {
		return len(b), nil
	}
	data := append([]byte(nil), b...)
	time.AfterFunc(delay, func() { j.Conn.Write(data) })
	return len(b), nil
}

func (j *jitterConn) Close() error {
	j.once.Do(func() { close(j.done) })
	return j.Conn.Close()
}

func jitterDial(base, spread time.Duration, loss float64) func(string) (net.Conn, error) {
	return func(address string) (net.Conn, error) {
		c, err := net.Dial("udp", address)
		if err != nil {
			return nil, err
		}
		return newJitterConn(c, base, spread, loss, 7), nil
	}
}

// startLinkOn starts a link with the real default timing over the given dial.
func startLinkOn(t *testing.T, m *fakeMac, timing LinkTiming, dial func(string) (net.Conn, error)) (*Link, *statusLog) {
	t.Helper()
	log := newStatusLog()
	link, err := StartLink(LinkConfig{Address: m.addr(), Static: mustKey(t), MacKey: m.static.Public,
		OnStatus: log.record, Timing: timing, Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(link.Close)
	return link, log
}

// downs counts how many times the link went from live to lost after it first became active.
func downs(all []LinkStatus) int {
	seen, n := false, 0
	for _, s := range all {
		if s.Active() {
			seen = true
		}
		if seen && s.State == LinkDown {
			n++
		}
	}
	return n
}

// M3: 50 ms of delay with 40 ms of jitter, as `tc netem delay 50ms 40ms` gives, on the real timing
// with a Mac that answers every 250 ms. The link must stay live the whole time: a status that
// arrives 80 ms late is not a lost helper. (With the old 330 ms timeout this flaps within seconds.)
func TestLinkDoesNotFlapUnderWifiJitter(t *testing.T) {
	if testing.Short() {
		t.Skip("runs for seconds")
	}
	m := newFakeMac(t)
	m.setStatusGap(250 * time.Millisecond)
	_, log := startLinkOn(t, m, LinkTiming{}, jitterDial(50*time.Millisecond, 40*time.Millisecond, 0))
	log.await(t, 3*time.Second, isActive)
	time.Sleep(6 * time.Second)
	if n := downs(log.snapshot()); n != 0 {
		t.Fatalf("the link flapped %d times under jitter: %+v", n, log.snapshot())
	}
}

func TestLinkSurvivesAnOccasionalLostStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("runs for seconds")
	}
	m := newFakeMac(t)
	m.setStatusGap(250 * time.Millisecond)
	_, log := startLinkOn(t, m, LinkTiming{}, jitterDial(30*time.Millisecond, 20*time.Millisecond, 0.05))
	log.await(t, 3*time.Second, isActive)
	time.Sleep(6 * time.Second)
	if n := downs(log.snapshot()); n != 0 {
		t.Fatalf("5%% loss alone made the link flap %d times", n)
	}
}

// Recovery needs a run of statuses. One status after a gap is not the helper being back.
func TestLinkRecoveryNeedsAStableRunOfStatuses(t *testing.T) {
	m := newFakeMac(t)
	timing := LinkTiming{Keepalive: 20 * time.Millisecond, StatusTimeout: 120 * time.Millisecond, Recover: 400 * time.Millisecond,
		Handshake: 200 * time.Millisecond, Retry: 5 * time.Second}
	_, log := startTestLink(t, m, timing)
	log.await(t, 2*time.Second, isActive)
	m.setMute(true)
	log.await(t, time.Second, func(s LinkStatus) bool { return s.State == LinkDown && errors.Is(s.Reason, ErrSilent) })
	// A stray status or two, then quiet again: still down, never live.
	m.setMute(false)
	time.Sleep(60 * time.Millisecond)
	m.setMute(true)
	time.Sleep(300 * time.Millisecond)
	for _, s := range log.snapshot()[1:] {
		if s.State == LinkDown {
			break
		}
	}
	if st := log.snapshot(); st[len(st)-1].State == LinkLive {
		t.Fatalf("a stray status counted as recovery: %+v", st)
	}
	// Now it really is back.
	back := time.Now()
	m.setMute(false)
	log.await(t, 2*time.Second, isActive)
	if took := time.Since(back); took < 350*time.Millisecond {
		t.Fatalf("live again after only %v, want a stable run first", took)
	}
}

// M3: a helper that is switched off says so, and the link follows at once, without waiting for
// silence. It keeps showing "off" (not "stopped answering") and finds its way back when switched on.
func TestLinkFollowsAnExplicitStoppedAtOnceAndComesBack(t *testing.T) {
	m := newFakeMac(t)
	timing := LinkTiming{} // real timing: the answer must not wait for the status timeout
	timing.Keepalive = 50 * time.Millisecond
	timing.Retry = 300 * time.Millisecond
	_, log := startTestLink(t, m, timing)
	log.await(t, 2*time.Second, isActive)
	m.set(StateDisabled)
	start := time.Now()
	m.stopped(StateDisabled)
	log.await(t, time.Second, func(s LinkStatus) bool { return s.State == LinkLive && s.Helper == StateDisabled })
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("followed the stop after %v, want well under 250 ms", took)
	}
	// The helper ended the session and says nothing more. The link keeps saying so, rather than
	// "stopped answering", while it knocks again with a new session.
	time.Sleep(time.Second)
	for _, s := range log.snapshot() {
		if s.State == LinkDown && errors.Is(s.Reason, ErrSilent) {
			t.Fatalf("a helper that said it stopped was reported as silent: %+v", log.snapshot())
		}
	}
	if _, _, _, handshakes := m.counts(); handshakes < 2 {
		t.Fatalf("handshakes = %d: the link never knocked again", handshakes)
	}
	m.set(StateActive)
	log.await(t, 3*time.Second, isActive)
}

// M2: a held button needs a heartbeat of input packets, because the Mac no longer lets
// keepalives hold it. It stops with the release.
func TestLinkRepeatsHeldButtonsAsInputUntilReleased(t *testing.T) {
	m := newFakeMac(t)
	timing := fastTiming()
	timing.RepeatHeld = 80 * time.Millisecond
	link, log := startTestLink(t, m, timing)
	log.await(t, 2*time.Second, isActive)
	if err := link.Send(InputPacket{Buttons: ButtonLeft}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	m.mu.Lock()
	held := 0
	for _, p := range m.inputs {
		if p.Buttons == ButtonLeft && p.DX == 0 && p.DY == 0 {
			held++
		}
	}
	m.mu.Unlock()
	if held < 4 {
		t.Fatalf("%d heartbeats in 500 ms, want one about every 80 ms", held)
	}
	if err := link.Send(InputPacket{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	before, _, _, _ := m.counts()
	time.Sleep(400 * time.Millisecond)
	if after, _, _, _ := m.counts(); after != before {
		t.Fatalf("%d input packets after the release", after-before)
	}
}

// A lost rekey reply used to freeze the keepalives for the whole handshake wait.
func TestLinkLostRekeyReplyDoesNotPauseTheKeepalives(t *testing.T) {
	m := newFakeMac(t)
	timing := LinkTiming{Keepalive: 20 * time.Millisecond, StatusTimeout: 600 * time.Millisecond,
		Handshake: 800 * time.Millisecond, Retry: 100 * time.Millisecond, RekeyAfter: 150 * time.Millisecond, RekeyLatest: 5 * time.Second}
	_, log := startTestLink(t, m, timing)
	log.await(t, 2*time.Second, isActive)
	m.mu.Lock()
	m.dropHS = true
	m.mu.Unlock()
	time.Sleep(1200 * time.Millisecond) // a rekey starts, its answer never comes
	m.mu.Lock()
	times := append([]time.Time(nil), m.keepaliveAt...)
	handshakes := m.handshake
	m.mu.Unlock()
	if handshakes < 2 {
		t.Fatalf("handshakes = %d: the rekey never started", handshakes)
	}
	worst := time.Duration(0)
	for i := 1; i < len(times); i++ {
		worst = max(worst, times[i].Sub(times[i-1]))
	}
	if worst > 200*time.Millisecond {
		t.Fatalf("keepalives paused for %v while a rekey waited for its answer", worst)
	}
	if n := downs(log.snapshot()); n != 0 {
		t.Fatalf("the link went down %d times because one rekey reply was lost", n)
	}
}

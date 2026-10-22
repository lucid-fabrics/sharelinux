package machelper

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/flynn/noise"
	E "github.com/sagernet/sing/common/exceptions"
)

// The Linux end of one helper session: IK handshake to the pinned Mac key,
// a keepalive every 250 ms, the Mac's status replies, a new handshake before
// the session ages out, and a verdict on whether the helper can be reached.
//
// The Mac only answers with a status to keepalives and refused input, never
// to accepted input, so the keepalive is also the heartbeat that tells this
// side the helper is alive. A killed helper shows up faster than that: its
// port closes and the next datagram this side sends comes back as "refused".

var (
	// ErrLinkNotReady: Send before the handshake finished, or after Close.
	ErrLinkNotReady = E.New("machelper: no helper session yet")
	// ErrSilent: the Mac stopped answering. It may have gone to sleep, been
	// restarted (which forgets every session), or left the network.
	ErrSilent = E.New("machelper: the Mac stopped answering")
)

// LinkState is what this side knows about the helper.
type LinkState uint8

const (
	// LinkConnecting: handshaking, or no word from the Mac yet.
	LinkConnecting LinkState = iota
	// LinkLive: the Mac answered recently. LinkStatus.Helper says whether it acts on input.
	LinkLive
	// LinkDown: nothing listens, nobody answers or the session was lost. Reason says which.
	LinkDown
)

// LinkStatus is the whole picture, delivered whenever it changes.
type LinkStatus struct {
	State  LinkState
	Helper HelperState // meaningful while State is LinkLive
	Reason error       // meaningful while State is LinkDown
}

// Active reports whether the helper takes the input this side sends.
func (s LinkStatus) Active() bool { return s.State == LinkLive && s.Helper == StateActive }

// LinkTiming holds the intervals; zero values use the defaults.
type LinkTiming struct {
	// Keepalive is the interval between keepalives. The design asks for 250 ms,
	// which is what the Mac needs to keep a session; this side sends them every
	// 50 ms because the Mac answers a keepalive with a status at most once per
	// 250 ms, and the answer comes at the first keepalive after that, so a
	// faster keepalive makes the answers regular and shortens the time to
	// notice that they stopped.
	Keepalive time.Duration
	// StatusTimeout is how long a live link may go without a status before it
	// counts as lost: the Mac's 250 ms between statuses, one keepalive
	// interval and room for Wi-Fi jitter, which bunches and delays datagrams
	// by tens of milliseconds, so one late status must not flip the routing.
	// The helper says so itself when it stops on purpose (switch off, Stop
	// now, a refusal, a closed port), and those are noticed at once; this
	// timeout is only for a helper that vanishes without a word (the Mac
	// asleep, off the network).
	StatusTimeout time.Duration
	// Recover is how long the statuses must keep coming, each within
	// StatusTimeout of the last, before a link that went quiet counts as live
	// again. One stray status is not recovery, and routing that flaps between
	// the helper and Screen Sharing is worse than routing that waits a moment.
	// Zero: a third longer than StatusTimeout.
	Recover time.Duration
	// RepeatHeld is how often the held buttons are sent again as input while
	// any is down. The Mac lifts a button when no input packet has arrived for
	// a second (its keepalives do not count), so a button held without moving
	// the mouse needs this heartbeat; one lost datagram is covered by the next.
	RepeatHeld time.Duration
	// Handshake bounds the wait for the Mac's answer to a handshake.
	Handshake time.Duration
	// Retry spaces new handshakes while the helper is down. The Mac allows one
	// per second per address, so this stays above a second.
	Retry time.Duration
	// RekeyAfter starts a new session once this old, as soon as no button is
	// held; RekeyLatest does it even with a button held (the Mac ends a session
	// at MaxSessionAge, and a rekey lifts held buttons on the Mac).
	RekeyAfter, RekeyLatest time.Duration
}

func (t LinkTiming) withDefaults() LinkTiming {
	if t.Keepalive <= 0 {
		t.Keepalive = 50 * time.Millisecond
	}
	if t.StatusTimeout <= 0 {
		t.StatusTimeout = 600 * time.Millisecond
	}
	if t.Recover <= 0 {
		t.Recover = t.StatusTimeout + t.StatusTimeout/3
	}
	if t.RepeatHeld <= 0 {
		t.RepeatHeld = 250 * time.Millisecond
	}
	if t.Handshake <= 0 {
		t.Handshake = time.Second
	}
	if t.Retry <= 0 {
		t.Retry = 1500 * time.Millisecond
	}
	if t.RekeyAfter <= 0 {
		t.RekeyAfter = 90 * time.Second
	}
	if t.RekeyLatest <= 0 {
		t.RekeyLatest = MaxSessionAge - 8*time.Second
	}
	return t
}

// LinkConfig is what a Link needs.
type LinkConfig struct {
	// Address is host:port of the helper.
	Address string
	Static  noise.DHKey
	MacKey  []byte
	// OnStatus is called from the link's own goroutines when the status changes.
	OnStatus func(LinkStatus)
	Timing   LinkTiming
	// Now is the wall clock for session ages and handshake timestamps.
	Now func() time.Time
	// Dial opens the connected datagram socket; nil dials UDP.
	Dial func(address string) (net.Conn, error)
}

// Link is one helper session, kept alive until Close.
type Link struct {
	cfg  LinkConfig
	conn net.Conn

	// sendMu orders sends: the sequence number and button mask are taken and the datagram
	// is sealed and written under it, so a held-button repeat can never overtake a newer
	// release (the Mac would see release, then press). Taken before mu, never inside it.
	sendMu sync.Mutex

	mu        sync.Mutex
	sess      *Session
	prev      *Session
	prevUntil time.Time
	started   time.Time // when sess began
	seq       uint32
	buttons   uint8
	// seen is the mask the caller last reported, whether or not it went out;
	// held is the part of it whose press went out in the current session. Only
	// held is ever repeated, so a press that did not reach this session, or an
	// old session, can never come back after a reconnect.
	seen      uint8
	held      uint8
	locked    bool
	status    LinkStatus
	lastHeard time.Time // last status, or the session start before the first
	// stableSince is when the current run of statuses began while recovering
	// from silence (zero when not recovering).
	stableSince time.Time
	lastInput   time.Time // when input last went out, for the held-button heartbeat
	sentPkts    uint64
	closed      bool

	// quietRetry: the next handshake follows a helper that stopped on purpose, so the
	// status stays as it is instead of going back to "connecting".
	quietRetry bool

	hsResp  chan []byte
	refused chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

// StartLink dials the helper and runs the session in the background. It
// returns at once; progress arrives through OnStatus.
func StartLink(cfg LinkConfig) (*Link, error) {
	hostport, err := NormalizeHelperAddress(cfg.Address)
	if err != nil {
		return nil, err
	}
	if len(cfg.MacKey) != 32 {
		return nil, ErrBadRemoteStatic
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Timing = cfg.Timing.withDefaults()
	if cfg.Dial == nil {
		cfg.Dial = func(address string) (net.Conn, error) {
			return net.DialTimeout("udp", address, 5*time.Second)
		}
	}
	conn, err := cfg.Dial(hostport)
	if err != nil {
		return nil, E.Cause(err, "reach the Mac")
	}
	l := &Link{
		cfg: cfg, conn: conn,
		hsResp: make(chan []byte, 1), refused: make(chan struct{}, 1),
		stop: make(chan struct{}), done: make(chan struct{}),
		status: LinkStatus{State: LinkConnecting},
	}
	go l.read()
	go l.run()
	return l, nil
}

// Status is the latest status.
func (l *Link) Status() LinkStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// SentInput counts the input packets sent (tests, statistics).
func (l *Link) SentInput() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sentPkts
}

// SetPointerLocked sets the "pointer locked" flag the packets carry.
func (l *Link) SetPointerLocked(locked bool) {
	l.mu.Lock()
	l.locked = locked
	l.mu.Unlock()
}

func (l *Link) flags() uint8 {
	if l.locked {
		return FlagPointerLocked
	}
	return 0
}

// Send seals and sends one input packet. A refused datagram (the helper's port
// is closed) marks the link down at once and is returned as an error.
func (l *Link) Send(p InputPacket) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	l.mu.Lock()
	if l.closed || l.sess == nil {
		l.mu.Unlock()
		return ErrLinkNotReady
	}
	// Record what the caller holds before any early return, so a release that
	// the dead link cannot carry still clears the repeat.
	rising := p.Buttons &^ l.seen
	l.seen = p.Buttons
	l.held &= p.Buttons
	if l.status.State == LinkDown && !errors.Is(l.status.Reason, ErrSilent) {
		l.buttons = l.held
		// The read side already learned the helper is gone (a closed port
		// reports once, to whichever call comes first). A helper that is only
		// quiet may still hear us: the caller's last packet, the one that
		// lifts a held button, must go out.
		reason := l.status.Reason
		l.mu.Unlock()
		return reason
	}
	p.Type = PacketInput
	p.Seq = l.seq
	l.seq++
	p.Flags = l.flags()
	l.held |= rising
	l.buttons = l.held
	l.lastInput = time.Now()
	sess := l.sess
	l.sentPkts++
	l.mu.Unlock()
	return l.write(sess, p.Clamped())
}

func (l *Link) write(sess *Session, p InputPacket) error {
	datagram, err := sess.Seal(p.Encode(), l.cfg.Now())
	if err != nil {
		l.markDown(err)
		return err
	}
	if _, err := l.conn.Write(datagram); err != nil {
		reason := mapNetError(err)
		l.markDown(reason)
		return reason
	}
	return nil
}

// Close says goodbye (the Mac lifts any held button at once) and stops.
func (l *Link) Close() {
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		sess := l.sess
		l.sess = nil
		l.mu.Unlock()
		if sess != nil {
			bye := InputPacket{Type: PacketBye}.Encode()
			for range 2 {
				if datagram, err := sess.Seal(bye, l.cfg.Now()); err == nil {
					l.conn.Write(datagram)
				}
			}
		}
		close(l.stop)
		l.conn.Close()
		select {
		case <-l.done:
		case <-time.After(500 * time.Millisecond):
		}
	})
}

func (l *Link) setStatus(s LinkStatus) {
	l.mu.Lock()
	if l.closed || (l.status.State == s.State && l.status.Helper == s.Helper && errors.Is(l.status.Reason, s.Reason)) {
		l.mu.Unlock()
		return
	}
	l.status = s
	l.mu.Unlock()
	if l.cfg.OnStatus != nil {
		l.cfg.OnStatus(s)
	}
}

func (l *Link) markDown(reason error) {
	l.mu.Lock()
	l.buttons, l.held = 0, 0
	l.mu.Unlock()
	l.setStatus(LinkStatus{State: LinkDown, Reason: reason})
}

// read is the only reader of the socket: handshake answers go to hsResp,
// transport datagrams are opened here and their status recorded.
func (l *Link) read() {
	buf := make([]byte, MaxDatagram+1)
	for {
		n, err := l.conn.Read(buf)
		select {
		case <-l.stop:
			return
		default:
		}
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				select {
				case l.refused <- struct{}{}:
				default:
				}
				l.markDown(ErrRefused)
				continue
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			l.markDown(mapNetError(err))
			return
		}
		if n > MaxDatagram {
			continue
		}
		datagram := append([]byte(nil), buf[:n]...)
		kind, _, ok := ParseDatagram(datagram)
		switch {
		case !ok:
		case kind == KindIKResponse:
			select {
			case l.hsResp <- datagram:
			default:
			}
		case kind == KindTransport:
			l.onTransport(datagram)
		}
	}
}

func (l *Link) onTransport(datagram []byte) {
	now := l.cfg.Now()
	l.mu.Lock()
	sess, prev := l.sess, l.prev
	if prev != nil && now.After(l.prevUntil) {
		l.prev = nil
		prev = nil
	}
	l.mu.Unlock()
	var plain []byte
	var err error
	for _, s := range []*Session{sess, prev} {
		if s == nil {
			continue
		}
		if plain, err = s.Open(datagram, StatusPacketSize, now); err == nil {
			break
		}
	}
	if err != nil || plain == nil {
		return
	}
	status, err := DecodeStatusPacket(plain)
	if err != nil {
		return
	}
	heard := time.Now()
	l.mu.Lock()
	gap := heard.Sub(l.lastHeard)
	l.lastHeard = heard
	if l.status.State == LinkDown && errors.Is(l.status.Reason, ErrSilent) {
		// Coming back from silence: a run of statuses, none more than StatusTimeout after
		// the one before, for Recover. A single answer after a long gap starts the run.
		if l.stableSince.IsZero() || gap > l.cfg.Timing.StatusTimeout {
			l.stableSince = heard
		}
		if heard.Sub(l.stableSince) < l.cfg.Timing.Recover {
			l.mu.Unlock()
			return
		}
	}
	l.stableSince = time.Time{}
	l.mu.Unlock()
	l.setStatus(LinkStatus{State: LinkLive, Helper: status.State})
}

// handshake runs one IK exchange and returns the new session. One datagram out,
// one answer in; any failure is returned and the caller decides when to retry.
func (l *Link) handshake() (*Session, error) {
	select { // an old answer must not satisfy this attempt
	case <-l.hsResp:
	default:
	}
	select {
	case <-l.refused:
	default:
	}
	now := l.cfg.Now()
	hs, init, err := NewIKInitiation(l.cfg.Static, l.cfg.MacKey, now)
	if err != nil {
		return nil, err
	}
	if _, err := l.conn.Write(init); err != nil {
		return nil, mapNetError(err)
	}
	timer := time.NewTimer(l.cfg.Timing.Handshake)
	defer timer.Stop()
	select {
	case answer := <-l.hsResp:
		if _, err := hs.Read(answer); err != nil {
			return nil, err
		}
		return hs.Session(l.cfg.Now())
	case <-l.refused:
		return nil, ErrRefused
	case <-timer.C:
		return nil, ErrNoAnswer
	case <-l.stop:
		return nil, ErrCancelled
	}
}

func (l *Link) install(sess *Session) {
	l.mu.Lock()
	l.prev, l.prevUntil = l.sess, l.cfg.Now().Add(time.Second)
	l.sess = sess
	l.buttons, l.held = 0, 0 // a new session repeats only presses made after it began
	l.started = l.cfg.Now()
	l.lastHeard = time.Now()
	l.mu.Unlock()
}

func (l *Link) keepalive() {
	l.mu.Lock()
	sess := l.sess
	p := InputPacket{Type: PacketKeepalive, Seq: l.seq, Flags: l.flags()}
	l.seq++
	l.mu.Unlock()
	if sess != nil {
		l.write(sess, p)
	}
}

func (l *Link) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-l.stop:
		return false
	}
}

func (l *Link) run() {
	defer close(l.done)
	quiet := false
	for {
		sess, err := l.handshake()
		if err != nil {
			select {
			case <-l.stop:
				return
			default:
			}
			l.markDown(err)
			if !l.sleep(l.cfg.Timing.Retry) {
				return
			}
			continue
		}
		l.install(sess)
		if !quiet {
			l.setStatus(LinkStatus{State: LinkConnecting})
		}
		quiet = false
		l.keepalive() // the first authenticated packet makes the session live on the Mac
		if !l.serve() {
			return
		}
		quiet, l.quietRetry = l.quietRetry, false
	}
}

// rekeyAttempt is a new handshake in flight while the old session carries on.
type rekeyAttempt struct {
	hs       *Handshake
	deadline time.Time
}

// repeatHeld sends the held buttons again as input (see LinkTiming.RepeatHeld).
func (l *Link) repeatHeld() {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	l.mu.Lock()
	sess := l.sess
	if sess == nil || l.buttons == 0 || time.Since(l.lastInput) < l.cfg.Timing.RepeatHeld ||
		(l.status.State == LinkDown && !errors.Is(l.status.Reason, ErrSilent)) {
		l.mu.Unlock()
		return
	}
	p := InputPacket{Type: PacketInput, Seq: l.seq, Flags: l.flags(), Buttons: l.buttons}
	l.seq++
	l.lastInput = time.Now()
	l.mu.Unlock()
	l.write(sess, p)
}

// startRekey sends a new initiation without waiting for the answer: the
// keepalives carry on meanwhile, so a lost answer costs nothing.
func (l *Link) startRekey() *rekeyAttempt {
	select { // an old answer must not satisfy this attempt
	case <-l.hsResp:
	default:
	}
	hs, init, err := NewIKInitiation(l.cfg.Static, l.cfg.MacKey, l.cfg.Now())
	if err != nil {
		return nil
	}
	if _, err := l.conn.Write(init); err != nil {
		return nil
	}
	return &rekeyAttempt{hs: hs, deadline: time.Now().Add(l.cfg.Timing.Handshake)}
}

func (l *Link) finishRekey(rk *rekeyAttempt, answer []byte) {
	if _, err := rk.hs.Read(answer); err != nil {
		return
	}
	sess, err := rk.hs.Session(l.cfg.Now())
	if err != nil {
		return
	}
	l.install(sess)
	l.keepalive()
}

// serve keeps one session going. It returns true when a new handshake is
// needed from scratch (the helper went silent or refused), false on Close.
func (l *Link) serve() bool {
	ticker := time.NewTicker(l.cfg.Timing.Keepalive)
	defer ticker.Stop()
	lastTry := time.Now()
	var rk *rekeyAttempt
	for {
		select {
		case <-l.stop:
			return false
		case answer := <-l.hsResp:
			if rk != nil {
				l.finishRekey(rk, answer)
				rk = nil
			}
			continue
		case <-ticker.C:
		}
		l.mu.Lock()
		st := l.status
		silent := time.Since(l.lastHeard)
		age := l.cfg.Now().Sub(l.started)
		held := l.buttons != 0
		l.mu.Unlock()

		if st.State == LinkDown {
			// Refused or lost: keep knocking with a fresh handshake, slowly.
			// A helper that merely went quiet may still hear the old session.
			if time.Since(lastTry) >= l.cfg.Timing.Retry {
				return true
			}
			if errors.Is(st.Reason, ErrSilent) {
				l.keepalive()
			}
			continue
		}
		if silent >= l.cfg.Timing.StatusTimeout {
			if st.State == LinkLive && st.Helper == StateDisabled {
				// The helper said it stopped, and ends the session when it does, so it has
				// nothing more to say. Knock again with a new session, keeping what is shown,
				// and learn when it is switched back on.
				if time.Since(lastTry) >= l.cfg.Timing.Retry {
					l.quietRetry = true
					return true
				}
				l.keepalive()
				continue
			}
			l.markDown(ErrSilent)
			lastTry = time.Now()
			continue
		}
		l.keepalive()
		l.repeatHeld()
		if rk != nil && time.Now().After(rk.deadline) {
			rk = nil
		}
		if rk == nil && age >= l.cfg.Timing.RekeyAfter && (!held || age >= l.cfg.Timing.RekeyLatest) && time.Since(lastTry) >= l.cfg.Timing.Retry {
			lastTry = time.Now()
			rk = l.startRekey()
		}
	}
}

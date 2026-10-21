package machelper

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/flynn/noise"
	E "github.com/sagernet/sing/common/exceptions"
)

// DefaultPort is the UDP port the Mac helper listens on.
const DefaultPort = 5961

var (
	// ErrNoAnswer: the Mac said nothing in time. The usual reasons are a wrong
	// address, a helper that is not running, or a pairing window that is not
	// open (the Mac does not answer strangers, by design).
	ErrNoAnswer = E.New("machelper: no answer from the Mac")
	// ErrRefused: the host answered that nothing listens on the port.
	ErrRefused = E.New("machelper: nothing is listening on the helper port")
	// ErrCancelled: the person (or the program) ended the attempt.
	ErrCancelled  = E.New("machelper: pairing was cancelled")
	ErrBadAddress = E.New("machelper: not a usable Mac address")
	// ErrNotAwaitingDecision is returned by Confirm before a code exists or after the run ended.
	ErrNotAwaitingDecision = E.New("machelper: no code is waiting for a decision")
)

// PairingOptions are the timings of one attempt; zero values use the defaults.
type PairingOptions struct {
	// AnswerTimeout bounds each wait for a datagram from the Mac before the
	// code exists. The Mac drops a half-finished attempt after a few seconds
	// anyway, so a longer wait only delays the error message.
	AnswerTimeout time.Duration
	// DecisionTimeout bounds how long the code waits for the person. The Mac's
	// pairing window is 2 minutes.
	DecisionTimeout time.Duration
}

func (o PairingOptions) withDefaults() PairingOptions {
	if o.AnswerTimeout <= 0 {
		o.AnswerTimeout = 8 * time.Second
	}
	if o.DecisionTimeout <= 0 {
		o.DecisionTimeout = 2 * time.Minute
	}
	return o
}

// NormalizeHelperAddress turns "mac.local", "10.0.0.5" or "[fe80::1]:5961"
// into host:port, defaulting the port to DefaultPort.
func NormalizeHelperAddress(address string) (string, error) {
	if address == "" {
		return "", ErrBadAddress
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host, port = address, strconv.Itoa(DefaultPort)
		if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
			host = host[1 : len(host)-1]
		}
	}
	n, perr := strconv.Atoi(port)
	if host == "" || perr != nil || n < 1 || n > 65535 {
		return "", ErrBadAddress
	}
	return net.JoinHostPort(host, port), nil
}

// PairingRun is one pairing attempt over UDP. It sends message 1 exactly once
// and never again: no resend, no retry, no reconnect, whatever the network
// does. A lost datagram, a silent Mac or a wrong reply ends the run with an
// error, and trying again is a new StartPairing call made by the application
// on behalf of the person, because the Mac charges every attempt that got as
// far as its nonce (three per window).
type PairingRun struct {
	conn    net.Conn
	ini     *PairingInitiator
	opts    PairingOptions
	code    chan string
	done    chan struct{}
	decide  chan decision
	cancel  context.CancelFunc
	mu      sync.Mutex
	err     error
	waiting bool // a code is shown and the person has not decided
	sent    int
}

type decision int

const (
	decideConfirm decision = iota + 1
	decideReject
)

// StartPairing resolves address, sends message 1 once and runs the attempt in
// the background. deviceName is what the Mac shows next to the code.
func StartPairing(ctx context.Context, address string, static noise.DHKey, deviceName string, opts PairingOptions) (*PairingRun, error) {
	hostport, err := NormalizeHelperAddress(address)
	if err != nil {
		return nil, err
	}
	ini, err := NewPairingInitiator(static, deviceName)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	dialer.Timeout = 5 * time.Second
	conn, err := dialer.DialContext(ctx, "udp", hostport)
	if err != nil {
		return nil, E.Cause(err, "reach the Mac")
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &PairingRun{
		conn: conn, ini: ini, opts: opts.withDefaults(),
		code: make(chan string, 1), done: make(chan struct{}), decide: make(chan decision, 1), cancel: cancel,
	}
	m1, err := ini.Start()
	if err != nil {
		conn.Close()
		cancel()
		return nil, err
	}
	go r.run(ctx, m1)
	return r, nil
}

// Code delivers the six digits once the Mac's nonce has arrived. It is closed
// without a value when the run fails first.
func (r *PairingRun) Code() <-chan string { return r.code }

// Done is closed when the run is over (decided, failed or cancelled).
func (r *PairingRun) Done() <-chan struct{} { return r.done }

// Err is why the run ended abnormally; nil after a Confirm or Reject.
func (r *PairingRun) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// DatagramsSent counts what this run sent (tests: proves nothing is resent).
func (r *PairingRun) DatagramsSent() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent
}

// PeerKey is the Mac's static key as the handshake produced it. It is only
// shown (as a fingerprint) to the person; pinning still happens through
// Confirm, after the codes were compared. Valid once Code has delivered.
func (r *PairingRun) PeerKey() []byte { return r.ini.PeerStatic() }

// Confirm is the person saying the codes match. It returns the Mac's static
// key, which the caller pins. Only valid while a code is waiting.
func (r *PairingRun) Confirm() ([]byte, error) {
	if !r.takeDecision(decideConfirm) {
		return nil, ErrNotAwaitingDecision
	}
	<-r.done
	if err := r.Err(); err != nil {
		return nil, err
	}
	return r.ini.PeerStatic(), nil
}

// Reject is the person saying the codes differ (or declining). Nothing is
// pinned; the run ends.
func (r *PairingRun) Reject() {
	if !r.takeDecision(decideReject) {
		r.Cancel()
		return
	}
	<-r.done
}

// Cancel ends the run at once without a decision.
func (r *PairingRun) Cancel() {
	r.fail(ErrCancelled)
	r.cancel()
	r.conn.Close()
}

func (r *PairingRun) takeDecision(d decision) bool {
	r.mu.Lock()
	ok := r.waiting
	r.waiting = false
	r.mu.Unlock()
	if ok {
		r.decide <- d
	}
	return ok
}

func (r *PairingRun) fail(err error) {
	r.mu.Lock()
	if r.err == nil {
		select {
		case <-r.done:
		default:
			r.err = err
		}
	}
	r.mu.Unlock()
}

func (r *PairingRun) write(datagram []byte) error {
	r.mu.Lock()
	r.sent++
	r.mu.Unlock()
	_, err := r.conn.Write(datagram)
	return err
}

func (r *PairingRun) run(ctx context.Context, m1 []byte) {
	defer close(r.done)
	defer close(r.code)
	defer r.conn.Close()
	defer r.cancel()
	if err := r.write(m1); err != nil {
		r.fail(mapNetError(err))
		return
	}
	go func() { // unblocks a pending Read when the context ends
		<-ctx.Done()
		r.conn.SetReadDeadline(time.Unix(1, 0))
	}()
	buf := make([]byte, MaxDatagram+1)
	for {
		r.conn.SetReadDeadline(time.Now().Add(r.opts.AnswerTimeout))
		n, err := r.conn.Read(buf)
		if ctx.Err() != nil {
			r.fail(ErrCancelled)
			return
		}
		if err != nil {
			r.fail(mapNetError(err))
			return
		}
		if n > MaxDatagram {
			r.fail(ErrPairingMessage)
			return
		}
		replies, err := r.ini.Receive(append([]byte(nil), buf[:n]...))
		if err != nil {
			r.fail(err)
			return
		}
		for _, reply := range replies {
			if err := r.write(reply); err != nil {
				r.fail(mapNetError(err))
				return
			}
		}
		if code := r.ini.Code(); code != "" {
			r.mu.Lock()
			r.waiting = true
			r.mu.Unlock()
			r.code <- code
			select {
			case <-r.decide:
			case <-ctx.Done():
				r.fail(ErrCancelled)
			case <-time.After(r.opts.DecisionTimeout):
				r.fail(ErrNoAnswer)
			}
			r.mu.Lock()
			r.waiting = false
			r.mu.Unlock()
			return
		}
	}
}

func mapNetError(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrNoAnswer
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ErrRefused
	}
	return E.Cause(err, "talk to the Mac")
}

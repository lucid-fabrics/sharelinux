package machelper

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/flynn/noise"
	E "github.com/sagernet/sing/common/exceptions"
	"golang.org/x/crypto/chacha20poly1305"
)

// Limits shared with the Swift side (design section 3.4).
const (
	MaxMessages       = uint64(1) << 20
	MaxSessionAge     = 120 * time.Second
	KeepaliveInterval = 250 * time.Millisecond
	SilenceTimeout    = time.Second
)

var (
	ErrHandshakeState  = E.New("machelper: handshake out of order or finished")
	ErrSessionExpired  = E.New("machelper: session expired, new handshake needed")
	ErrReplay          = E.New("machelper: replayed or too old")
	ErrAuth            = E.New("machelper: authentication failed")
	ErrMalformed       = E.New("machelper: malformed datagram")
	ErrUnexpectedKind  = E.New("machelper: unexpected datagram kind")
	ErrBadRemoteStatic = E.New("machelper: remote static key must be 32 bytes")
)

var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

type Pattern uint8

const (
	// PatternXX is used once, for pairing.
	PatternXX Pattern = iota + 1
	// PatternIK is used for every session, with both static keys pinned.
	PatternIK
)

func (p Pattern) noise() noise.HandshakePattern {
	if p == PatternXX {
		return noise.HandshakeXX
	}
	return noise.HandshakeIK
}

func (p Pattern) kinds() []DatagramKind {
	if p == PatternXX {
		return []DatagramKind{KindXXMessage1, KindXXMessage2, KindXXMessage3}
	}
	return []DatagramKind{KindIKInitiation, KindIKResponse}
}

func GenerateKeyPair() (noise.DHKey, error) { return suite.GenerateKeypair(nil) }

type HandshakeConfig struct {
	Pattern   Pattern
	Initiator bool
	Static    noise.DHKey
	// RemoteStatic is required for the IK initiator and must be empty otherwise.
	RemoteStatic []byte
	Prologue     []byte

	random io.Reader // tests only: fixed ephemeral key bytes for vectors
}

// Handshake wraps a flynn/noise state with datagram framing and in-order,
// poison-on-failure behaviour.
type Handshake struct {
	state     *noise.HandshakeState
	pattern   Pattern
	initiator bool
	index     int
	failed    bool
	send      *noise.CipherState
	recv      *noise.CipherState
}

func NewHandshake(config HandshakeConfig) (*Handshake, error) {
	if config.Pattern != PatternXX && config.Pattern != PatternIK {
		return nil, ErrHandshakeState
	}
	needRemote := config.Pattern == PatternIK && config.Initiator
	if needRemote && len(config.RemoteStatic) != 32 {
		return nil, ErrBadRemoteStatic
	}
	if !needRemote && len(config.RemoteStatic) != 0 {
		return nil, ErrHandshakeState
	}
	state, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   suite,
		Pattern:       config.Pattern.noise(),
		Initiator:     config.Initiator,
		Prologue:      config.Prologue,
		StaticKeypair: config.Static,
		Random:        config.random,
		PeerStatic:    config.RemoteStatic,
	})
	if err != nil {
		return nil, E.Cause(err, "new handshake")
	}
	return &Handshake{state: state, pattern: config.Pattern, initiator: config.Initiator}, nil
}

func (h *Handshake) myTurn() bool { return (h.index%2 == 0) == h.initiator }

// Write produces the next handshake datagram (kind byte included).
func (h *Handshake) Write(payload []byte) ([]byte, error) {
	kinds := h.pattern.kinds()
	if h.failed || h.index >= len(kinds) || !h.myTurn() {
		return nil, ErrHandshakeState
	}
	out, cs1, cs2, err := h.state.WriteMessage([]byte{byte(kinds[h.index])}, payload)
	if err != nil {
		h.failed = true
		return nil, E.Cause(err, "write handshake")
	}
	h.advance(cs1, cs2)
	return out, nil
}

// Read consumes one handshake datagram and returns its payload. Any failure
// poisons the handshake.
func (h *Handshake) Read(datagram []byte) ([]byte, error) {
	kinds := h.pattern.kinds()
	if h.failed || h.index >= len(kinds) || h.myTurn() {
		return nil, ErrHandshakeState
	}
	if len(datagram) < 1 || len(datagram) > MaxDatagram {
		h.failed = true
		return nil, ErrMalformed
	}
	if DatagramKind(datagram[0]) != kinds[h.index] {
		h.failed = true
		return nil, ErrUnexpectedKind
	}
	payload, cs1, cs2, err := h.state.ReadMessage(nil, datagram[1:])
	if err != nil {
		h.failed = true
		return nil, E.Cause(err, "read handshake")
	}
	h.advance(cs1, cs2)
	return payload, nil
}

func (h *Handshake) advance(cs1, cs2 *noise.CipherState) {
	h.index++
	if cs1 == nil {
		return
	}
	if h.initiator {
		h.send, h.recv = cs1, cs2
	} else {
		h.send, h.recv = cs2, cs1
	}
}

func (h *Handshake) Complete() bool { return !h.failed && h.index == len(h.pattern.kinds()) }

// Hash is the handshake hash (channel binding); identical on both sides.
func (h *Handshake) Hash() []byte { return append([]byte(nil), h.state.ChannelBinding()...) }

// PeerStatic is the remote static public key once known (nil before).
func (h *Handshake) PeerStatic() []byte { return append([]byte(nil), h.state.PeerStatic()...) }

// Session derives the transport session. Only valid once Complete.
func (h *Handshake) Session(now time.Time) (*Session, error) {
	if !h.Complete() || h.send == nil {
		return nil, ErrHandshakeState
	}
	sendKey, recvKey := h.send.UnsafeKey(), h.recv.UnsafeKey()
	send, err := chacha20poly1305.New(sendKey[:])
	if err != nil {
		return nil, err
	}
	recv, err := chacha20poly1305.New(recvKey[:])
	if err != nil {
		return nil, err
	}
	// The Noise states hold the keys no longer needed once the AEADs exist.
	h.send, h.recv = nil, nil
	return &Session{send: send, recv: recv, started: now}, nil
}

// NewIKInitiation starts a session handshake. The first message carries a
// TAI64N timestamp so a replayed initiation is rejected by the Mac.
func NewIKInitiation(static noise.DHKey, macStatic []byte, now time.Time) (*Handshake, []byte, error) {
	h, err := NewHandshake(HandshakeConfig{Pattern: PatternIK, Initiator: true, Static: static, RemoteStatic: macStatic})
	if err != nil {
		return nil, nil, err
	}
	ts := TAI64N(now)
	datagram, err := h.Write(ts[:])
	if err != nil {
		return nil, nil, err
	}
	return h, datagram, nil
}

// Session is the Noise transport with explicit 64-bit counters, safe for
// concurrent Seal/Open.
type Session struct {
	mu      sync.Mutex
	send    cipher.AEAD
	recv    cipher.AEAD
	counter uint64
	window  ReplayWindow
	started time.Time
}

func (s *Session) expired(now time.Time) bool {
	age := now.Sub(s.started)
	return age < 0 || age >= MaxSessionAge
}

func nonce(counter uint64) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	binary.LittleEndian.PutUint64(n[4:], counter)
	return n
}

// Seal encrypts one payload into a transport datagram.
func (s *Session) Seal(plaintext []byte, now time.Time) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expired(now) || s.counter >= MaxMessages {
		return nil, ErrSessionExpired
	}
	if len(plaintext)+TransportOverhead > MaxDatagram {
		return nil, ErrMalformed
	}
	n := s.counter
	s.counter++
	out := make([]byte, 9, 9+len(plaintext)+16)
	out[0] = byte(KindTransport)
	binary.LittleEndian.PutUint64(out[1:], n)
	return s.send.Seal(out, nonce(n), plaintext, nil), nil
}

// Open authenticates and decrypts one transport datagram. The replay window
// only advances after the tag verified.
func (s *Session) Open(datagram []byte, maxPlaintext int, now time.Time) ([]byte, error) {
	if len(datagram) < TransportOverhead || len(datagram) > TransportOverhead+maxPlaintext ||
		DatagramKind(datagram[0]) != KindTransport {
		return nil, ErrMalformed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expired(now) {
		return nil, ErrSessionExpired
	}
	counter := binary.LittleEndian.Uint64(datagram[1:9])
	if counter >= MaxMessages {
		return nil, ErrSessionExpired
	}
	if !s.window.Check(counter) {
		return nil, ErrReplay
	}
	plaintext, err := s.recv.Open(nil, nonce(counter), datagram[9:], nil)
	if err != nil {
		return nil, ErrAuth
	}
	if !s.window.Commit(counter) {
		return nil, ErrReplay
	}
	return plaintext, nil
}

// TAI64N returns the 12-byte timestamp (big endian) used in IK initiations.
func TAI64N(t time.Time) [12]byte {
	var out [12]byte
	secs := t.Unix()
	if secs < 0 {
		secs = 0
	}
	binary.BigEndian.PutUint64(out[:8], uint64(1)<<62+uint64(secs))
	binary.BigEndian.PutUint32(out[8:], uint32(t.Nanosecond()))
	return out
}

// SAS derives the six-digit short authentication string from the XX handshake
// hash. Both people compare it before pairing is accepted.
func SAS(handshakeHash []byte) string {
	mac := hmac.New(sha256.New, []byte("OSS-mousehelper-SAS-v1"))
	mac.Write(handshakeHash)
	sum := mac.Sum(nil)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1_000_000)
}

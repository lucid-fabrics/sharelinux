package machelper

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flynn/noise"
	E "github.com/sagernet/sing/common/exceptions"
	"golang.org/x/text/unicode/norm"
)

// Pairing: Noise XX, then commit-before-reveal inside the XX transport.
//
//	1. L -> M  XX message 1
//	2. M -> L  XX message 2
//	3. L -> M  XX message 3 (payload: device name)
//	4. L -> M  commit  H(r_L)                (kind 0x13)
//	5. M -> L  r_M, fresh random, only after the commit
//	6. L -> M  r_L; the Mac checks it against the commit
//
// code = HMAC(handshake hash, label || r_L || r_M), six digits by rejection
// sampling. A man in the middle fixes both handshakes, his commitment and his
// r_M before r_L is known to him, so he cannot grind his last message to make
// the two codes agree: each attempt matches with probability 10^-6 whatever
// he tries.

const (
	NonceSize = 32

	PairingTagCommit         byte = 0x01
	PairingTagResponderNonce byte = 0x02
	PairingTagReveal         byte = 0x03
	pairingMessageSize            = 1 + NonceSize
)

var (
	ErrPairingState    = E.New("machelper: pairing message out of order")
	ErrPairingMessage  = E.New("machelper: malformed pairing message")
	ErrBadDeviceName   = E.New("machelper: device name not allowed")
	pairingCommitLabel = []byte("OSS-mousehelper v2 pairing commit")
	pairingCodeLabel   = []byte("OSS-mousehelper v2 pairing code")
)

// Commitment is H(r_L), what the Linux side sends first.
func Commitment(initiatorNonce []byte) []byte {
	h := sha256.New()
	h.Write(pairingCommitLabel)
	h.Write(initiatorNonce)
	return h.Sum(nil)
}

// PairingCode derives the six digits both people compare.
func PairingCode(handshakeHash, initiatorNonce, responderNonce []byte) string {
	const modulus = 1_000_000
	limit := (^uint64(0) / modulus) * modulus // values below limit are uniform mod 10^6
	var last uint64
	for counter := 0; counter < 255; counter++ {
		m := hmac.New(sha256.New, handshakeHash)
		m.Write(pairingCodeLabel)
		m.Write(initiatorNonce)
		m.Write(responderNonce)
		m.Write([]byte{byte(counter)})
		block := m.Sum(nil)
		for w := 0; w < 4; w++ {
			last = binary.BigEndian.Uint64(block[w*8:])
			if last < limit {
				return fmt.Sprintf("%06d", last%modulus)
			}
		}
	}
	// Unreachable in practice (5e-14 per word, 1020 words in a row).
	return fmt.Sprintf("%06d", last%modulus)
}

// invisible lists characters that render as nothing (or nearly) and are not
// caught by the category checks. Same set as the Swift validator.
var invisible = map[rune]bool{
	0x034F: true, 0x115F: true, 0x1160: true, 0x17B4: true, 0x17B5: true,
	0x2800: true, 0x3164: true, 0xFFA0: true, 0xFFFC: true, 0xFFFD: true,
}

// NormalizeDeviceName returns the NFC form of name if it is allowed: valid
// UTF-8, at most MaxDeviceName bytes, visible characters only (no control,
// format such as zero-width or bidi marks, separators other than single ASCII
// spaces between words, private-use, unassigned or known invisible fillers).
// The Mac applies the same rules and is authoritative; this lets Linux fail
// early. Unicode tables of the two platforms may differ for very new
// characters, which can only make one side refuse, never accept.
func NormalizeDeviceName(name string) (string, error) {
	if name == "" || len(name) > MaxDeviceName || !utf8.ValidString(name) {
		return "", ErrBadDeviceName
	}
	s := norm.NFC.String(name)
	if len(s) > MaxDeviceName {
		return "", ErrBadDeviceName
	}
	previousSpace := true // also refuses a leading space
	for _, r := range s {
		switch {
		case r == ' ':
			if previousSpace {
				return "", ErrBadDeviceName
			}
			previousSpace = true
			continue
		case unicode.Is(unicode.Zs, r), unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co, unicode.Cs):
			return "", ErrBadDeviceName
		case !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S):
			return "", ErrBadDeviceName // unassigned
		case invisible[r]:
			return "", ErrBadDeviceName
		}
		previousSpace = false
	}
	if previousSpace {
		return "", ErrBadDeviceName
	}
	return s, nil
}

// PairingInitiator is the Linux side of one pairing attempt.
//
// It never retries. An initiator is single use: Start works once, and any
// error, an unexpected datagram or a finished run leaves it dead (every later
// call returns ErrPairingState). There is no timer, no resend and no loop in
// this package. A new attempt is a new initiator, created by an explicit call
// of the application on behalf of the person, because the Mac charges every
// attempt that got as far as its nonce (3 per window) and a client that retried
// by itself would burn the person's window, or hand a spoofer free tickets.
type PairingInitiator struct {
	hs     *Handshake
	name   string
	random io.Reader
	now    func() time.Time

	session    *Session
	nonce      []byte
	transcript []byte
	stage      int // 0 new, 1 m1 sent, 2 waiting for r_M, 3 done, -1 dead
	code       string
}

// NewPairingInitiator validates the device name (NFC, visible characters)
// before anything is sent.
func NewPairingInitiator(static noise.DHKey, deviceName string) (*PairingInitiator, error) {
	return newPairingInitiator(static, deviceName, rand.Reader, time.Now, nil)
}

func newPairingInitiator(static noise.DHKey, deviceName string, random io.Reader, now func() time.Time, ephemeral io.Reader) (*PairingInitiator, error) {
	name, err := NormalizeDeviceName(deviceName)
	if err != nil {
		return nil, err
	}
	hs, err := NewHandshake(HandshakeConfig{Pattern: PatternXX, Initiator: true, Static: static, random: ephemeral})
	if err != nil {
		return nil, err
	}
	return &PairingInitiator{hs: hs, name: name, random: random, now: now}, nil
}

// Start returns XX message 1.
func (p *PairingInitiator) Start() ([]byte, error) {
	if p.stage != 0 {
		return nil, ErrPairingState
	}
	m1, err := p.hs.Write(nil)
	if err != nil {
		p.stage = -1
		return nil, err
	}
	p.stage = 1
	return m1, nil
}

// Receive consumes one datagram from the Mac and returns the datagrams to
// send back. Any error ends the attempt.
func (p *PairingInitiator) Receive(datagram []byte) (replies [][]byte, err error) {
	defer func() {
		if err != nil {
			p.stage = -1
			replies = nil
		}
	}()
	switch p.stage {
	case 1:
		if _, err := p.hs.Read(datagram); err != nil {
			return nil, err
		}
		m3, err := p.hs.Write([]byte(p.name))
		if err != nil {
			return nil, err
		}
		p.transcript = p.hs.Hash()
		if p.session, err = p.hs.Session(p.now()); err != nil {
			return nil, err
		}
		p.nonce = make([]byte, NonceSize)
		if _, err := io.ReadFull(p.random, p.nonce); err != nil {
			return nil, err
		}
		commit, err := p.session.Seal(append([]byte{PairingTagCommit}, Commitment(p.nonce)...), p.now())
		if err != nil {
			return nil, err
		}
		p.stage = 2
		return [][]byte{m3, commit}, nil
	case 2:
		pt, err := p.session.Open(datagram, pairingMessageSize, p.now())
		if err != nil || len(pt) != pairingMessageSize || pt[0] != PairingTagResponderNonce {
			return nil, ErrPairingMessage
		}
		reveal, err := p.session.Seal(append([]byte{PairingTagReveal}, p.nonce...), p.now())
		if err != nil {
			return nil, err
		}
		p.code = PairingCode(p.transcript, p.nonce, pt[1:])
		p.stage = 3
		return [][]byte{reveal}, nil
	}
	return nil, ErrPairingState
}

// Code is the six digits to show, empty until the Mac's nonce arrived.
func (p *PairingInitiator) Code() string { return p.code }

// PeerStatic is the Mac's static key. Pin it only after the person confirmed
// that the codes match.
func (p *PairingInitiator) PeerStatic() []byte { return p.hs.PeerStatic() }

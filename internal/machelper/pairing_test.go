package machelper

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
)

// macPairer is the Mac role of pairing, written from the protocol description
// for tests (the shipped Mac side is Swift; interop_darwin_test.go runs this
// package against it). It records when its random value is drawn.
type macPairer struct {
	hs          *Handshake
	session     *Session
	random      io.Reader
	transcript  []byte
	commitment  []byte
	nonce       []byte
	name        string
	peerStatic  []byte
	code        string
	stage       int // 0 m1, 1 m3, 2 commit, 3 reveal, 4 done, -1 dead
	nonceDrawn  bool
	revealedEar bool
}

func newMacPairer(t testing.TB, static noise.DHKey, random io.Reader) *macPairer {
	t.Helper()
	hs, err := NewHandshake(HandshakeConfig{Pattern: PatternXX, Static: static})
	if err != nil {
		t.Fatal(err)
	}
	return &macPairer{hs: hs, random: random}
}

func (m *macPairer) receive(datagram []byte) (reply []byte, err error) {
	defer func() {
		if err != nil {
			m.stage = -1
		}
	}()
	now := time.Unix(1_800_000_000, 0)
	switch m.stage {
	case 0:
		if _, err := m.hs.Read(datagram); err != nil {
			return nil, err
		}
		m.stage = 1
		return m.hs.Write(nil)
	case 1:
		payload, err := m.hs.Read(datagram)
		if err != nil {
			return nil, err
		}
		name, err := NormalizeDeviceName(string(payload))
		if err != nil {
			return nil, err
		}
		m.name, m.transcript, m.peerStatic = name, m.hs.Hash(), m.hs.PeerStatic()
		if m.session, err = m.hs.Session(now); err != nil {
			return nil, err
		}
		m.stage = 2
		return nil, nil
	case 2:
		pt, err := m.session.Open(datagram, pairingMessageSize, now)
		if err != nil || len(pt) != pairingMessageSize || pt[0] != PairingTagCommit {
			return nil, ErrPairingMessage
		}
		m.commitment = pt[1:]
		m.nonce = make([]byte, NonceSize)
		m.nonceDrawn = true
		if _, err := io.ReadFull(m.random, m.nonce); err != nil {
			return nil, err
		}
		m.stage = 3
		return m.session.Seal(append([]byte{PairingTagResponderNonce}, m.nonce...), now)
	case 3:
		pt, err := m.session.Open(datagram, pairingMessageSize, now)
		if err != nil || len(pt) != pairingMessageSize || pt[0] != PairingTagReveal {
			return nil, ErrPairingMessage
		}
		if !bytes.Equal(Commitment(pt[1:]), m.commitment) {
			return nil, ErrPairingMessage
		}
		m.code = PairingCode(m.transcript, pt[1:], m.nonce)
		m.stage = 4
		return nil, nil
	}
	return nil, ErrPairingState
}

func mustPairing(t testing.TB, static noise.DHKey, name string) *PairingInitiator {
	t.Helper()
	p, err := NewPairingInitiator(static, name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// runPairing plays an honest pairing and returns both ends.
func runPairing(t testing.TB, linux noise.DHKey, mac *macPairer, name string) *PairingInitiator {
	t.Helper()
	ini := mustPairing(t, linux, name)
	m1, err := ini.Start()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := mac.receive(m1)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ini.Receive(m2)
	if err != nil || len(out) != 2 {
		t.Fatalf("after message 2: %d datagrams, %v", len(out), err)
	}
	if _, err := mac.receive(out[0]); err != nil {
		t.Fatal(err)
	}
	rm, err := mac.receive(out[1])
	if err != nil {
		t.Fatal(err)
	}
	out, err = ini.Receive(rm)
	if err != nil || len(out) != 1 {
		t.Fatalf("after nonce: %d datagrams, %v", len(out), err)
	}
	if _, err := mac.receive(out[0]); err != nil {
		t.Fatal(err)
	}
	return ini
}

func TestPairingHonestRunAgreesOnCodeNameAndKeys(t *testing.T) {
	linux, macKey := mustKey(t), mustKey(t)
	mac := newMacPairer(t, macKey, rand.Reader)
	ini := runPairing(t, linux, mac, "Linux box")
	if ini.Code() == "" || ini.Code() != mac.code || len(ini.Code()) != 6 {
		t.Fatalf("codes %q %q", ini.Code(), mac.code)
	}
	if mac.name != "Linux box" || !bytes.Equal(mac.peerStatic, linux.Public) || !bytes.Equal(ini.PeerStatic(), macKey.Public) {
		t.Fatal("name or static keys not exchanged")
	}
}

func TestPairingCodeKnownAnswers(t *testing.T) {
	// Computed independently with python hmac/hashlib; same values in Swift.
	one, two := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	if got := hex.EncodeToString(Commitment(one)); got != "dd391c60e44cd686917444d96dfbf2cd9782bc0ed157a1c4d3b963d598c41b95" {
		t.Fatalf("commitment %s", got)
	}
	if got := PairingCode(make([]byte, 32), one, two); got != "401886" {
		t.Fatalf("code %s", got)
	}
}

func TestPairingCodeDependsOnEveryInput(t *testing.T) {
	h, a, b := make([]byte, 32), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	base := PairingCode(h, a, b)
	flip := func(x []byte, i int) []byte { y := bytes.Clone(x); y[i] ^= 1; return y }
	for name, got := range map[string]string{
		"hash": PairingCode(flip(h, 31), a, b), "r_L": PairingCode(h, flip(a, 0), b),
		"r_M": PairingCode(h, a, flip(b, 15)), "swapped": PairingCode(h, b, a),
	} {
		if got == base {
			t.Errorf("code ignores %s", name)
		}
	}
	// Uniform over the first digit (rejection sampling, no modulo bias to see here, but no skew either).
	var first [10]int
	for i := 0; i < 20000; i++ {
		x := make([]byte, 32)
		_, _ = rand.Read(x)
		first[PairingCode(h, x, b)[0]-'0']++
	}
	for d, n := range first {
		if n < 1700 || n > 2300 {
			t.Fatalf("digit %d appears %d times of 20000: %v", d, n, first)
		}
	}
}

// Commit before reveal: the initiator sends only a commitment after message 3
// and reveals r_L only once the Mac's nonce arrived.
func TestPairingCommitsBeforeRevealing(t *testing.T) {
	linux, macKey := mustKey(t), mustKey(t)
	nonce := bytes.Repeat([]byte{0x5a}, 32)
	ini, err := newPairingInitiator(linux, "Linux box", bytes.NewReader(nonce), time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	mac := newMacPairer(t, macKey, rand.Reader)
	m1, _ := ini.Start()
	m2, _ := mac.receive(m1)
	out, err := ini.Receive(m2)
	if err != nil || len(out) != 2 {
		t.Fatal(out, err)
	}
	if bytes.Contains(out[0], nonce) || bytes.Contains(out[1], nonce) {
		t.Fatal("r_L in the clear")
	}
	_, _ = mac.receive(out[0])
	if mac.nonceDrawn {
		t.Fatal("Mac drew its nonce before the commit")
	}
	// What the second datagram carries is a commitment, not r_L.
	pt, err := mac.session.Open(out[1], pairingMessageSize, time.Unix(1_800_000_000, 0))
	if err != nil || pt[0] != PairingTagCommit || !bytes.Equal(pt[1:], Commitment(nonce)) {
		t.Fatalf("second datagram is not the commitment: %x %v", pt, err)
	}
	if ini.Code() != "" {
		t.Fatal("code shown before the Mac's nonce arrived")
	}
	// A reveal in place of the nonce, or anything out of order, is refused.
	ini2 := mustPairing(t, linux, "x")
	if _, err := ini2.Receive([]byte{1, 2, 3}); err != ErrPairingState {
		t.Fatalf("receive before start: %v", err)
	}
}

func TestPairingRefusesWrongRevealAndStageErrors(t *testing.T) {
	linux, macKey := mustKey(t), mustKey(t)
	mac := newMacPairer(t, macKey, rand.Reader)
	ini := mustPairing(t, linux, "Linux box")
	m1, _ := ini.Start()
	m2, _ := mac.receive(m1)
	out, _ := ini.Receive(m2)
	_, _ = mac.receive(out[0])
	rm, _ := mac.receive(out[1])
	if _, err := ini.Receive(rm); err != nil {
		t.Fatal(err)
	}
	lie, _ := ini.session.Seal(append([]byte{PairingTagReveal}, bytes.Repeat([]byte{9}, 32)...), time.Now())
	if _, err := mac.receive(lie); err == nil || mac.code != "" {
		t.Fatal("a reveal that does not match the commitment was accepted")
	}
	// Dead after the error, and the initiator is dead after a bad message too.
	if _, err := ini.Receive(rm); err != ErrPairingState {
		t.Fatalf("replayed nonce: %v", err)
	}
	ini3 := mustPairing(t, linux, "Linux box")
	m1, _ = ini3.Start()
	if _, err := ini3.Start(); err != ErrPairingState {
		t.Fatal("started twice")
	}
	if _, err := ini3.Receive(append([]byte{byte(KindXXMessage2)}, make([]byte, 96)...)); err == nil {
		t.Fatal("garbage message 2 accepted")
	}
	if _, err := ini3.Receive(m1); err == nil {
		t.Fatal("dead attempt kept going")
	}
}

// The strongest strategy against commit-before-reveal: run the Mac-side
// handshake only after learning r_L from the honest Linux side, then grind the
// last handshake message (fresh static key, device-name variants) for the best
// guess. The Mac's r_M is drawn after the attacker's final choice, so the codes
// agree by luck only: 10^-6 per attempt however many variants are ground.
func TestGrindingMITMCannotSteerTheCode(t *testing.T) {
	macKey := mustKey(t)
	attempt := func(tries int) bool {
		// Side A: attacker plays the Mac towards honest Linux with a fixed r_M'.
		aliceKey, attackerKey := mustKey(t), mustKey(t)
		alice := mustPairing(t, aliceKey, "Linux box")
		fakeMac := newMacPairer(t, attackerKey, bytes.NewReader(bytes.Repeat([]byte{0xaa}, 32)))
		m1, _ := alice.Start()
		m2, _ := fakeMac.receive(m1)
		out, _ := alice.Receive(m2)
		_, _ = fakeMac.receive(out[0])
		rm, _ := fakeMac.receive(out[1])
		out, _ = alice.Receive(rm)
		_, _ = fakeMac.receive(out[0])
		rL := alice.nonce // what the attacker learned from the reveal
		target := alice.Code()

		// Side B: grind variants against the real Mac's code for a guessed r_M.
		var bestKey noise.DHKey
		var bestName string
		for i := 0; i < tries; i++ {
			key := mustKey(t)
			name := "Linux box" + strings.Repeat("\u00b7", i%3) + string(rune('A'+i%26))
			guess := pairAsAttacker(t, key, name, rL, macKey, bytes.Repeat([]byte{0}, 32))
			if i == 0 || guess == target {
				bestKey, bestName = key, name
			}
			if guess == target {
				break
			}
		}
		realMacNonce := make([]byte, 32)
		_, _ = rand.Read(realMacNonce)
		return pairAsAttacker(t, bestKey, bestName, rL, macKey, realMacNonce) == target
	}
	for _, tries := range []int{1, 40} {
		hits := 0
		for i := 0; i < 150; i++ {
			if attempt(tries) {
				hits++
			}
		}
		if hits != 0 { // expected 150 * 10^-6
			t.Fatalf("grinding %d variants matched %d of 150 attempts", tries, hits)
		}
	}
}

// pairAsAttacker runs an initiator with chosen key, name and r_L against a Mac
// whose nonce is macNonce and returns the code the Mac shows.
func pairAsAttacker(t testing.TB, key noise.DHKey, name string, rL []byte, macKey noise.DHKey, macNonce []byte) string {
	t.Helper()
	mac := newMacPairer(t, macKey, bytes.NewReader(macNonce))
	ini, err := newPairingInitiator(key, name, bytes.NewReader(rL), time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := ini.Start()
	m2, _ := mac.receive(m1)
	out, _ := ini.Receive(m2)
	_, _ = mac.receive(out[0])
	rm, _ := mac.receive(out[1])
	out, _ = ini.Receive(rm)
	_, _ = mac.receive(out[0])
	return mac.code
}

// The codes cannot be predicted from anything the attacker fixes: with both
// handshakes and the commitment held constant, each fresh honest random value
// gives a fresh code.
// chargedWindow is the pairing window rule (the shipped window is Swift; the
// interop tests run the same attack against it) as a model over macPairer: an
// attempt is charged the moment r_M is sent, only a confirm refunds, three
// charges close the window.
type chargedWindow struct {
	failures, nonces int
	open             bool
}

// TestAbortGrindingNeedsTheMacsNonceAndIsCappedByCharging pins the premise of
// the charging rule with the real Go initiator: the code on screen is known to
// the man in the middle as soon as r_M arrives, before any reveal, so every
// r_M handed out is one comparison. Under the rule he gets three.
func TestAbortGrindingNeedsTheMacsNonceAndIsCappedByCharging(t *testing.T) {
	w := chargedWindow{open: true}
	seen := 0
	for i := 0; i < 200 && w.open; i++ {
		mac := newMacPairer(t, mustKey(t), rand.Reader)
		ini := mustPairing(t, mustKey(t), "Linux box")
		m1, _ := ini.Start()
		m2, _ := mac.receive(m1)
		out, _ := ini.Receive(m2)
		_, _ = mac.receive(out[0])
		rm, err := mac.receive(out[1])
		if err != nil {
			t.Fatal(err)
		}
		w.nonces++ // r_M leaves the Mac: this attempt is charged
		if _, err := ini.Receive(rm); err != nil {
			t.Fatal(err)
		}
		if ini.Code() == "" {
			t.Fatal("the attacker must be able to read the code before revealing")
		}
		if mac.code != "" {
			t.Fatal("the Mac shows nothing before the reveal")
		}
		seen++
		// He aborts (codes differ): charged, never refunded.
		w.failures++
		if w.failures >= 3 {
			w.open = false
		}
	}
	if seen != 3 || w.nonces != 3 {
		t.Fatalf("attacker saw %d codes (%d nonces); want 3", seen, w.nonces)
	}
}

func TestCodesAreUnpredictableFromAttackerControlledState(t *testing.T) {
	h, fixed := make([]byte, 32), bytes.Repeat([]byte{3}, 32)
	_, _ = rand.Read(h)
	macSide, linuxSide := map[string]bool{}, map[string]bool{}
	for i := 0; i < 2000; i++ {
		x := make([]byte, 32)
		_, _ = rand.Read(x)
		macSide[PairingCode(h, fixed, x)] = true
		linuxSide[PairingCode(h, x, fixed)] = true
	}
	if len(macSide) < 1990 || len(linuxSide) < 1990 {
		t.Fatalf("only %d and %d distinct codes in 2000", len(macSide), len(linuxSide))
	}
}

func TestNormalizeDeviceName(t *testing.T) {
	ok := map[string]string{
		"Wassim's PC":        "Wassim's PC",
		"B\u00fcro 2":        "B\u00fcro 2",
		"Rechner \U0001F4BB": "Rechner \U0001F4BB",
		"Cafe\u0301":         "Caf\u00e9", // NFC
	}
	for in, want := range ok {
		got, err := NormalizeDeviceName(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	refused := []string{
		"", " ", "a\nb", "a\tb", "a\x00b",
		"a\u200bb", "a\u200db", "a\ufeffb", "a\u2060b",
		"a\u202eb", "a\u202ab", "a\u2066b", "a\u200eb", "a\u061cb",
		"a\U000e0041b",
		"a\u3164b", "a\u115fb", "a\u2800b", "a\u034fb",
		"a\u00a0b", "a\u3000b", "a\u2003b", "a\u2028b", "a\u2029b",
		"a\ue000b", "a\u0378b",
		" a", "a ", "a  b",
		strings.Repeat("x", 65), "a\xc0\x80", "\xff\xfe",
	}
	for _, in := range refused {
		if got, err := NormalizeDeviceName(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	if _, err := NormalizeDeviceName(strings.Repeat("x", 64)); err != nil {
		t.Errorf("64 bytes refused: %v", err)
	}
	if _, err := NewPairingInitiator(mustKey(t), "evil\u200bname"); err == nil {
		t.Error("initiator accepted an invisible character")
	}
}

func TestPrologueBindsNameVersionAndRoles(t *testing.T) {
	if got := string(PatternXX.StandardPrologue()); got != "OSS-mousehelper v2 pairing Noise_XX_25519_ChaChaPoly_SHA256 initiator=linux responder=mac" {
		t.Fatal(got)
	}
	if got := string(PatternIK.StandardPrologue()); got != "OSS-mousehelper v2 session Noise_IK_25519_ChaChaPoly_SHA256 initiator=linux responder=mac" {
		t.Fatal(got)
	}
	a, b := mustKey(t), mustKey(t)
	if _, err := NewHandshake(HandshakeConfig{Pattern: PatternXX, Initiator: true, Static: a, Prologue: []byte{}}); err != ErrEmptyPrologue {
		t.Fatalf("empty prologue: %v", err)
	}
	for _, other := range []string{
		"OSS-mousehelper v1 session Noise_IK_25519_ChaChaPoly_SHA256 initiator=linux responder=mac",
		"OSS-mousehelper v2 session Noise_IK_25519_ChaChaPoly_SHA256 initiator=mac responder=linux",
		"x",
	} {
		ini, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Initiator: true, Static: a, RemoteStatic: b.Public, Prologue: []byte(other)})
		res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
		m1, err := ini.Write(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := res.Read(m1); err == nil {
			t.Fatalf("handshake with prologue %q was accepted", other)
		}
	}
}

func TestMAC1KnownAnswerAndTamper(t *testing.T) {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(i)
	}
	msg := make([]byte, 109)
	msg[0] = 1
	for i := 1; i < len(msg); i++ {
		msg[i] = byte(i - 1)
	}
	dg := AppendMAC1(msg, pub)
	if got := hex.EncodeToString(dg[len(msg):]); got != "477b0dbee0664e5ef3e66d626ca9bd41" {
		t.Fatalf("mac1 %s", got)
	}
	if !VerifyMAC1(dg, pub) || VerifyMAC1(dg, make([]byte, 32)) || VerifyMAC1(dg[:MAC1Size], pub) {
		t.Fatal("verify")
	}
	for bit := 0; bit < len(dg)*8; bit++ {
		bad := bytes.Clone(dg)
		bad[bit/8] ^= 1 << (bit % 8)
		if VerifyMAC1(bad, pub) {
			t.Fatalf("bit %d accepted", bit)
		}
	}
}

func TestIKInitiationCarriesMAC1AndResponderChecksIt(t *testing.T) {
	a, b := mustKey(t), mustKey(t)
	_, m1, err := NewIKInitiation(a, b.Public, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(m1) != 1+108+MAC1Size || !VerifyMAC1(m1, b.Public) {
		t.Fatalf("initiation is %d bytes / mac1 invalid", len(m1))
	}
	res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	if _, err := res.Read(m1[:len(m1)-MAC1Size]); err == nil {
		t.Fatal("initiation without mac1 accepted")
	}
	res, _ = NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	bad := bytes.Clone(m1)
	bad[len(bad)-1] ^= 1
	if _, err := res.Read(bad); err != ErrBadMAC {
		t.Fatalf("bad mac1: %v", err)
	}
	res, _ = NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	if _, err := res.Read(m1); err != nil {
		t.Fatalf("good initiation: %v", err)
	}
}

// FuzzPairing throws arbitrary bytes at every pairing state of both roles.
// Nothing random may produce a code, and the device-name validator must keep
// its promises on any input.
func FuzzPairing(f *testing.F) {
	seedMac := mustKey(f)
	{
		mac := newMacPairer(f, seedMac, rand.Reader)
		ini := mustPairing(f, mustKey(f), "Linux box")
		m1, _ := ini.Start()
		m2, _ := mac.receive(m1)
		out, _ := ini.Receive(m2)
		_, _ = mac.receive(out[0])
		rm, _ := mac.receive(out[1])
		out2, _ := ini.Receive(rm)
		f.Add(m1)
		f.Add(m2)
		f.Add(out[0])
		f.Add(out[1])
		f.Add(rm)
		f.Add(out2[0])
	}
	f.Add([]byte{byte(KindPairingTransport), 1, 2, 3})
	f.Add([]byte("Linux box"))
	f.Add([]byte("a\u200bb"))
	f.Fuzz(func(t *testing.T, data []byte) {
		// Initiator, state "waiting for message 2".
		a, mk := mustKey(t), mustKey(t)
		ini := mustPairing(t, a, "Linux box")
		_, _ = ini.Start()
		if _, err := ini.Receive(data); err == nil {
			t.Fatalf("initiator accepted foreign message 2 %x", data)
		}
		// Initiator, state "waiting for the Mac's nonce".
		mac := newMacPairer(t, mk, rand.Reader)
		ini = mustPairing(t, a, "Linux box")
		m1, _ := ini.Start()
		m2, _ := mac.receive(m1)
		out, _ := ini.Receive(m2)
		if _, err := ini.Receive(data); err == nil || ini.Code() != "" {
			t.Fatalf("initiator accepted foreign nonce message %x", data)
		}
		// Mac role at each stage.
		for stage := 0; stage < 4; stage++ {
			m := newMacPairer(t, mk, rand.Reader)
			ini := mustPairing(t, a, "Linux box")
			m1, _ := ini.Start()
			var m2 []byte
			if stage >= 1 {
				m2, _ = m.receive(m1)
			}
			var o [][]byte
			if stage >= 2 {
				o, _ = ini.Receive(m2)
				_, _ = m.receive(o[0])
			}
			if stage >= 3 {
				_, _ = m.receive(o[1])
			}
			_, _ = m.receive(data)
			if m.code != "" {
				t.Fatalf("Mac produced a code from foreign input at stage %d: %x", stage, data)
			}
		}
		_ = out
		// Device names: whatever passes is NFC, short, visible and stable.
		if s, err := NormalizeDeviceName(string(data)); err == nil {
			if len(s) == 0 || len(s) > MaxDeviceName {
				t.Fatalf("bad length %d", len(s))
			}
			if again, err := NormalizeDeviceName(s); err != nil || again != s {
				t.Fatalf("not idempotent: %q -> %q (%v)", s, again, err)
			}
			for _, r := range s {
				if r < 0x20 || r == 0x7f || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
					t.Fatalf("invisible rune %U in %q", r, s)
				}
			}
		}
	})
}

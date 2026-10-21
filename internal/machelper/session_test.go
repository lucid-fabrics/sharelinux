package machelper

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/flynn/noise"
)

func mustKey(t *testing.T) noise.DHKey {
	t.Helper()
	k, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// ikPair runs a complete IK handshake and returns both sessions.
func ikPair(t *testing.T, now time.Time) (initiator, responder *Session) {
	t.Helper()
	a, b := mustKey(t), mustKey(t)
	ini, m1, err := NewIKInitiation(a, b.Public, now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := res.Read(m1)
	if err != nil {
		t.Fatal(err)
	}
	want := TAI64N(now)
	if !bytes.Equal(ts, want[:]) {
		t.Fatal("timestamp payload")
	}
	if !bytes.Equal(res.PeerStatic(), a.Public) {
		t.Fatal("responder learned the wrong static key")
	}
	m2, err := res.Write(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ini.Read(m2); err != nil {
		t.Fatal(err)
	}
	si, err := ini.Session(now)
	if err != nil {
		t.Fatal(err)
	}
	sr, err := res.Session(now)
	if err != nil {
		t.Fatal(err)
	}
	return si, sr
}

func TestIKSessionBothDirections(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	si, sr := ikPair(t, now)
	dg, err := si.Seal([]byte("hello"), now)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := sr.Open(dg, 20, now)
	if err != nil || string(pt) != "hello" {
		t.Fatalf("open: %q %v", pt, err)
	}
	back, _ := sr.Seal([]byte{1, 2}, now)
	pt, err = si.Open(back, 20, now)
	if err != nil || !bytes.Equal(pt, []byte{1, 2}) {
		t.Fatalf("reverse: %v %v", pt, err)
	}
	// Directions use different keys: a datagram never opens on its own sender.
	if _, err := si.Open(dg, 20, now); err == nil {
		t.Fatal("own datagram opened by sender")
	}
}

func TestSessionRejectsReplayAndTamper(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	si, sr := ikPair(t, now)
	dg, _ := si.Seal([]byte("x"), now)
	for i := range dg {
		bad := append([]byte(nil), dg...)
		bad[i] ^= 1
		if _, err := sr.Open(bad, 20, now); err == nil {
			t.Fatalf("tampered byte %d accepted", i)
		}
	}
	if _, err := sr.Open(dg, 20, now); err != nil {
		t.Fatalf("failed attempts must not burn the counter: %v", err)
	}
	if _, err := sr.Open(dg, 20, now); err != ErrReplay {
		t.Fatalf("replay: %v", err)
	}
}

func TestSessionLengthAndKindChecks(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	si, sr := ikPair(t, now)
	dg, _ := si.Seal(make([]byte, 20), now)
	if _, err := sr.Open(dg[:TransportOverhead-1], 20, now); err != ErrMalformed {
		t.Fatalf("short: %v", err)
	}
	if _, err := sr.Open(append(dg, 0), 20, now); err != ErrMalformed {
		t.Fatalf("long: %v", err)
	}
	bad := append([]byte(nil), dg...)
	bad[0] = byte(KindIKResponse)
	if _, err := sr.Open(bad, 20, now); err != ErrMalformed {
		t.Fatalf("kind: %v", err)
	}
	if _, err := si.Seal(make([]byte, MaxDatagram), now); err == nil {
		t.Fatal("oversized plaintext sealed")
	}
}

func TestSessionExpiryAndMessageLimit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	si, sr := ikPair(t, now)
	late := now.Add(MaxSessionAge)
	if _, err := si.Seal([]byte{1}, late); err != ErrSessionExpired {
		t.Fatalf("age: %v", err)
	}
	if _, err := si.Seal([]byte{1}, now.Add(-time.Second)); err != ErrSessionExpired {
		t.Fatalf("clock went backwards: %v", err)
	}
	dg, _ := si.Seal([]byte{1}, now)
	if _, err := sr.Open(dg, 20, late); err != ErrSessionExpired {
		t.Fatalf("open age: %v", err)
	}
	si.counter = MaxMessages
	if _, err := si.Seal([]byte{1}, now); err != ErrSessionExpired {
		t.Fatalf("count: %v", err)
	}
	si.counter = MaxMessages - 1
	if _, err := si.Seal([]byte{1}, now); err != nil {
		t.Fatalf("last allowed message: %v", err)
	}
}

func TestIKWrongPinnedKeyFails(t *testing.T) {
	a, b, wrong := mustKey(t), mustKey(t), mustKey(t)
	ini, m1, err := NewIKInitiation(a, wrong.Public, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	if _, err := res.Read(m1); err == nil {
		t.Fatal("responder accepted a message pinned to a different key")
	}
	if _, err := res.Write(nil); err == nil {
		t.Fatal("poisoned handshake still writes")
	}
	_ = ini
}

func TestHandshakeBitFlipsAndTruncationRejected(t *testing.T) {
	a, b := mustKey(t), mustKey(t)
	_, m1, _ := NewIKInitiation(a, b.Public, time.Now())
	for bit := 8; bit < len(m1)*8; bit++ { // byte 0 is the kind, checked separately
		bad := append([]byte(nil), m1...)
		bad[bit/8] ^= 1 << (bit % 8)
		res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
		if _, err := res.Read(bad); err == nil {
			t.Fatalf("bit %d accepted", bit)
		}
	}
	for n := 0; n < len(m1); n++ {
		res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
		if _, err := res.Read(m1[:n]); err == nil {
			t.Fatalf("truncated to %d accepted", n)
		}
	}
	bad := append([]byte(nil), m1...)
	bad[0] = byte(KindXXMessage1)
	res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	if _, err := res.Read(bad); err != ErrUnexpectedKind {
		t.Fatalf("kind: %v", err)
	}
}

func TestLowOrderEphemeralRejected(t *testing.T) {
	b := mustKey(t)
	res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	dg := append([]byte{byte(KindIKInitiation)}, make([]byte, 108)...)
	if _, err := res.Read(dg); err == nil {
		t.Fatal("all-zero ephemeral accepted")
	}
}

func TestHandshakeOrderEnforced(t *testing.T) {
	a, b := mustKey(t), mustKey(t)
	res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	if _, err := res.Write(nil); err != ErrHandshakeState {
		t.Fatal("responder spoke first")
	}
	ini, _, _ := NewIKInitiation(a, b.Public, time.Now())
	if _, err := ini.Write(nil); err != ErrHandshakeState {
		t.Fatal("initiator wrote twice")
	}
	if _, err := ini.Session(time.Now()); err == nil {
		t.Fatal("session before completion")
	}
	if _, err := NewHandshake(HandshakeConfig{Pattern: PatternIK, Initiator: true, Static: a}); err == nil {
		t.Fatal("IK initiator without a pinned key")
	}
	if _, err := NewHandshake(HandshakeConfig{Pattern: PatternIK, Initiator: true, Static: a, RemoteStatic: []byte{1}}); err == nil {
		t.Fatal("short pinned key")
	}
	if _, err := NewHandshake(HandshakeConfig{Pattern: PatternXX, Initiator: true, Static: a, RemoteStatic: b.Public}); err == nil {
		t.Fatal("XX with a pinned key")
	}
}

func xx(t *testing.T, a, b noise.DHKey, name string) (sasA, sasB string) {
	t.Helper()
	ini, _ := NewHandshake(HandshakeConfig{Pattern: PatternXX, Initiator: true, Static: a})
	res, _ := NewHandshake(HandshakeConfig{Pattern: PatternXX, Static: b})
	m1, _ := ini.Write(nil)
	if _, err := res.Read(m1); err != nil {
		t.Fatal(err)
	}
	m2, _ := res.Write(nil)
	if _, err := ini.Read(m2); err != nil {
		t.Fatal(err)
	}
	m3, _ := ini.Write([]byte(name))
	pt, err := res.Read(m3)
	if err != nil || string(pt) != name {
		t.Fatalf("m3: %q %v", pt, err)
	}
	if !ini.Complete() || !res.Complete() {
		t.Fatal("incomplete")
	}
	if !bytes.Equal(res.PeerStatic(), a.Public) || !bytes.Equal(ini.PeerStatic(), b.Public) {
		t.Fatal("static keys not exchanged")
	}
	return SAS(ini.Hash()), SAS(res.Hash())
}

func TestXXPairingSASMatches(t *testing.T) {
	sa, sb := xx(t, mustKey(t), mustKey(t), "Linux box")
	if sa != sb || len(sa) != 6 {
		t.Fatalf("SAS %q %q", sa, sb)
	}
}

func TestSASDiffersUnderMITM(t *testing.T) {
	alice, bob, mallory := mustKey(t), mustKey(t), mustKey(t)
	aliceSide, _ := xx(t, alice, mallory, "x")
	_, bobSide := xx(t, mallory, bob, "x")
	if aliceSide == bobSide {
		t.Fatalf("both sides show %s under a MITM", aliceSide)
	}
}

func TestSASKnownAnswer(t *testing.T) {
	// HMAC-SHA256(key "OSS-mousehelper-SAS-v1", 32 zero bytes), first 4 bytes
	// big endian mod 10^6, computed with python's hmac; same value in Swift.
	if got := SAS(make([]byte, 32)); got != "487222" {
		t.Fatalf("SAS = %s", got)
	}
}

func TestTAI64N(t *testing.T) {
	ts := TAI64N(time.Unix(1_800_000_000, 250_000_000))
	if hex.EncodeToString(ts[:8]) != "40000000"+"6b49d200" {
		t.Fatalf("seconds %x", ts[:8])
	}
	if hex.EncodeToString(ts[8:]) != "0ee6b280" {
		t.Fatalf("nanos %x", ts[8:])
	}
	later := TAI64N(time.Unix(1_800_000_000, 250_000_001))
	if bytes.Compare(later[:], ts[:]) <= 0 {
		t.Fatal("byte order must follow time")
	}
}

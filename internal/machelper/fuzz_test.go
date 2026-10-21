package machelper

import (
	"bytes"
	"testing"
	"time"
)

// FuzzWire feeds arbitrary bytes to every wire decoder. Decoders must never
// panic, and anything they accept must re-encode to exactly the same bytes
// (no lossy or ambiguous accept).
func FuzzWire(f *testing.F) {
	f.Add(InputPacket{Type: PacketInput, Buttons: ButtonLeft, DX: 5, DY: -5, Seq: 9, Flags: FlagPointerLocked}.Encode())
	f.Add(InputPacket{Type: PacketKeepalive}.Encode())
	f.Add(StatusPacket{State: StateLocked}.Encode())
	f.Add([]byte{byte(KindTransport), 1, 2, 3})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if p, err := DecodeInputPacket(data); err == nil {
			if !bytes.Equal(p.Encode(), data) {
				t.Fatalf("input packet re-encodes differently: %x", data)
			}
			c := p.Clamped()
			if c.DX < -MaxMotion || c.DY < -MaxMotion || c.SX < -MaxScroll || c.SX > MaxScroll ||
				c.SY < -MaxScroll || c.SY > MaxScroll {
				t.Fatalf("clamp let %+v through", c)
			}
		}
		if p, err := DecodeStatusPacket(data); err == nil && !bytes.Equal(p.Encode(), data) {
			t.Fatalf("status packet re-encodes differently: %x", data)
		}
		if kind, body, ok := ParseDatagram(data); ok {
			if len(body) != len(data)-1 || data[0] != byte(kind) {
				t.Fatalf("ParseDatagram inconsistent for %x", data)
			}
		}
	})
}

// FuzzSecure throws arbitrary bytes at the responder handshake and at an
// established session. Nothing random may authenticate.
func FuzzSecure(f *testing.F) {
	now := time.Unix(1_800_000_000, 0)
	a, _ := GenerateKeyPair()
	b, _ := GenerateKeyPair()
	_, m1, _ := NewIKInitiation(a, b.Public, now)
	f.Add(m1)
	f.Add(append([]byte{byte(KindTransport)}, make([]byte, 40)...))
	si, sr := fuzzSessions(f, now)
	valid, _ := si.Seal(make([]byte, InputPacketSize), now)
	f.Add(valid)
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
		if err != nil {
			t.Fatal(err)
		}
		if payload, err := res.Read(data); err == nil && !bytes.Equal(data, m1) {
			t.Fatalf("handshake accepted foreign input %x (payload %x)", data, payload)
		}
		if pt, err := sr.Open(data, InputPacketSize, now); err == nil && !bytes.Equal(data, valid) {
			t.Fatalf("session accepted foreign datagram %x (%x)", data, pt)
		}
	})
}

func fuzzSessions(f *testing.F, now time.Time) (*Session, *Session) {
	a, _ := GenerateKeyPair()
	b, _ := GenerateKeyPair()
	ini, m1, err := NewIKInitiation(a, b.Public, now)
	if err != nil {
		f.Fatal(err)
	}
	res, _ := NewHandshake(HandshakeConfig{Pattern: PatternIK, Static: b})
	if _, err := res.Read(m1); err != nil {
		f.Fatal(err)
	}
	m2, _ := res.Write(nil)
	if _, err := ini.Read(m2); err != nil {
		f.Fatal(err)
	}
	si, _ := ini.Session(now)
	sr, _ := res.Session(now)
	return si, sr
}

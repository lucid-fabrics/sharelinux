package machelper

import (
	"bytes"
	"errors"
	"testing"
)

func TestInputPacketEncodeLayout(t *testing.T) {
	packet := InputPacket{
		Type:    PacketInput,
		Buttons: ButtonLeft | ButtonRight | ButtonMiddle | ButtonBack | ButtonForward,
		DX:      -2,
		DY:      0x0102,
		SX:      -3,
		SY:      0x0405,
		Seq:     0x01020304,
		Flags:   FlagPointerLocked,
	}
	want := []byte{
		0x01, 0x1f,
		0xfe, 0xff,
		0x02, 0x01,
		0xfd, 0xff,
		0x05, 0x04,
		0x04, 0x03, 0x02, 0x01,
		0x01,
		0x00, 0x00, 0x00, 0x00, 0x00,
	}

	got := packet.Encode()
	if len(got) != InputPacketSize {
		t.Fatalf("Encode() length = %d, want %d", len(got), InputPacketSize)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode() = %v, want %v", got, want)
	}
}

func TestInputPacketRoundTripExtremeValues(t *testing.T) {
	tests := []InputPacket{
		{
			Type:    PacketInput,
			Buttons: buttonsValid,
			DX:      -32768,
			DY:      -32768,
			SX:      -32768,
			SY:      -32768,
			Seq:     ^uint32(0),
			Flags:   FlagPointerLocked,
		},
		{
			Type:    PacketInput,
			Buttons: buttonsValid,
			DX:      32767,
			DY:      32767,
			SX:      32767,
			SY:      32767,
			Seq:     0,
			Flags:   FlagPointerLocked,
		},
	}

	for i, want := range tests {
		got, err := DecodeInputPacket(want.Encode())
		if err != nil {
			t.Fatalf("case %d: DecodeInputPacket() error = %v", i, err)
		}
		if got != want {
			t.Fatalf("case %d: DecodeInputPacket() = %+v, want %+v", i, got, want)
		}
	}
}

func TestDecodeInputPacketRejectsWrongLengths(t *testing.T) {
	for length := 0; length <= 64; length++ {
		if length == InputPacketSize {
			continue
		}

		b := make([]byte, length)
		if length > 0 {
			b[0] = byte(PacketInput)
		}
		_, err := DecodeInputPacket(b)
		if !errors.Is(err, ErrBadLength) {
			t.Errorf("length %d: DecodeInputPacket() error = %v, want %v", length, err, ErrBadLength)
		}
	}
}

func TestDecodeInputPacketRejectsUnknownTypes(t *testing.T) {
	valid := InputPacket{Type: PacketInput}.Encode()
	for _, packetType := range []byte{0, 4, 255} {
		b := append([]byte(nil), valid...)
		b[0] = packetType

		_, err := DecodeInputPacket(b)
		if !errors.Is(err, ErrBadType) {
			t.Errorf("type %d: DecodeInputPacket() error = %v, want %v", packetType, err, ErrBadType)
		}
	}
}

func TestDecodeInputPacketRejectsReservedButtonBits(t *testing.T) {
	valid := InputPacket{Type: PacketInput}.Encode()
	for bit := 5; bit <= 7; bit++ {
		b := append([]byte(nil), valid...)
		b[1] = byte(1 << bit)

		_, err := DecodeInputPacket(b)
		if !errors.Is(err, ErrReservedBits) {
			t.Errorf("button bit %d: DecodeInputPacket() error = %v, want %v", bit, err, ErrReservedBits)
		}
	}
}

func TestDecodeInputPacketRejectsReservedFlagBits(t *testing.T) {
	valid := InputPacket{Type: PacketInput}.Encode()
	for bit := 1; bit <= 7; bit++ {
		b := append([]byte(nil), valid...)
		b[14] = byte(1 << bit)

		_, err := DecodeInputPacket(b)
		if !errors.Is(err, ErrReservedBits) {
			t.Errorf("flag bit %d: DecodeInputPacket() error = %v, want %v", bit, err, ErrReservedBits)
		}
	}
}

func TestDecodeInputPacketRejectsNonZeroPadding(t *testing.T) {
	valid := InputPacket{Type: PacketInput}.Encode()
	for offset := 15; offset < InputPacketSize; offset++ {
		for value := 1; value <= 255; value++ {
			b := append([]byte(nil), valid...)
			b[offset] = byte(value)

			_, err := DecodeInputPacket(b)
			if !errors.Is(err, ErrPadding) {
				t.Errorf(
					"padding offset %d value %d: DecodeInputPacket() error = %v, want %v",
					offset, value, err, ErrPadding,
				)
			}
		}
	}
}

func TestDecodeInputPacketRejectsPayloadOnKeepaliveAndBye(t *testing.T) {
	packetTypes := []PacketType{PacketKeepalive, PacketBye}
	motionFields := []struct {
		name   string
		offset int
	}{
		{name: "dx", offset: 2},
		{name: "dy", offset: 4},
		{name: "sx", offset: 6},
		{name: "sy", offset: 8},
	}

	for _, packetType := range packetTypes {
		base := InputPacket{Type: packetType}.Encode()
		if _, err := DecodeInputPacket(base); err != nil {
			t.Errorf("type %d: empty packet error = %v", packetType, err)
		}

		withButtons := append([]byte(nil), base...)
		withButtons[1] = ButtonLeft
		_, err := DecodeInputPacket(withButtons)
		if !errors.Is(err, ErrUnexpectedData) {
			t.Errorf("type %d with buttons: error = %v, want %v", packetType, err, ErrUnexpectedData)
		}

		for _, field := range motionFields {
			for _, value := range []byte{1, 0xff} {
				b := append([]byte(nil), base...)
				b[field.offset] = value

				_, err := DecodeInputPacket(b)
				if !errors.Is(err, ErrUnexpectedData) {
					t.Errorf(
						"type %d with %s=%d: error = %v, want %v",
						packetType, field.name, value, err, ErrUnexpectedData,
					)
				}
			}
		}
	}
}

func TestDecodeInputPacketAcceptsKeepaliveWithSeqAndPointerLock(t *testing.T) {
	want := InputPacket{
		Type:  PacketKeepalive,
		Seq:   ^uint32(0),
		Flags: FlagPointerLocked,
	}

	got, err := DecodeInputPacket(want.Encode())
	if err != nil {
		t.Fatalf("DecodeInputPacket() error = %v", err)
	}
	if got != want {
		t.Fatalf("DecodeInputPacket() = %+v, want %+v", got, want)
	}
}

func TestInputPacketClamped(t *testing.T) {
	tests := []struct {
		name string
		in   InputPacket
		want InputPacket
	}{
		{
			name: "motion maximum",
			in:   InputPacket{Type: PacketInput, DX: 32767, DY: 32767},
			want: InputPacket{Type: PacketInput, DX: 32767, DY: 32767},
		},
		{
			name: "motion minimum",
			in:   InputPacket{Type: PacketInput, DX: -32768, DY: -32768},
			want: InputPacket{Type: PacketInput, DX: -32767, DY: -32767},
		},
		{
			name: "scroll above maximum",
			in:   InputPacket{Type: PacketInput, SX: 2001, SY: 2001},
			want: InputPacket{Type: PacketInput, SX: 2000, SY: 2000},
		},
		{
			name: "scroll below minimum",
			in:   InputPacket{Type: PacketInput, SX: -2001, SY: -2001},
			want: InputPacket{Type: PacketInput, SX: -2000, SY: -2000},
		},
		{
			name: "scroll boundaries",
			in:   InputPacket{Type: PacketInput, SX: 2000, SY: -2000},
			want: InputPacket{Type: PacketInput, SX: 2000, SY: -2000},
		},
		{
			name: "in range",
			in:   InputPacket{Type: PacketInput, DX: 1234, DY: -1234, SX: 1999, SY: -1999},
			want: InputPacket{Type: PacketInput, DX: 1234, DY: -1234, SX: 1999, SY: -1999},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.in.Clamped()
			if got != test.want {
				t.Fatalf("Clamped() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestStatusPacketEncodeDecode(t *testing.T) {
	for state := StateActive; state <= StateNeedsGrant; state++ {
		packet := StatusPacket{State: state}
		encoded := packet.Encode()
		want := []byte{statusType, byte(state)}

		if !bytes.Equal(encoded, want) {
			t.Errorf("state %d: Encode() = %v, want %v", state, encoded, want)
		}

		decoded, err := DecodeStatusPacket(encoded)
		if err != nil {
			t.Errorf("state %d: DecodeStatusPacket() error = %v", state, err)
			continue
		}
		if decoded != packet {
			t.Errorf("state %d: DecodeStatusPacket() = %+v, want %+v", state, decoded, packet)
		}
	}
}

func TestDecodeStatusPacketRejectsWrongLengths(t *testing.T) {
	for length := 0; length <= 64; length++ {
		if length == StatusPacketSize {
			continue
		}

		b := make([]byte, length)
		if length > 0 {
			b[0] = statusType
		}
		_, err := DecodeStatusPacket(b)
		if !errors.Is(err, ErrBadLength) {
			t.Errorf("length %d: DecodeStatusPacket() error = %v, want %v", length, err, ErrBadLength)
		}
	}
}

func TestDecodeStatusPacketRejectsInvalidState(t *testing.T) {
	_, err := DecodeStatusPacket([]byte{statusType, byte(StateNeedsGrant + 1)})
	if !errors.Is(err, ErrBadState) {
		t.Fatalf("DecodeStatusPacket() error = %v, want %v", err, ErrBadState)
	}
}

func TestDecodeStatusPacketRejectsInvalidType(t *testing.T) {
	for _, packetType := range []byte{0, 9, 11, 255} {
		_, err := DecodeStatusPacket([]byte{packetType, byte(StateActive)})
		if !errors.Is(err, ErrBadType) {
			t.Errorf("type %d: DecodeStatusPacket() error = %v, want %v", packetType, err, ErrBadType)
		}
	}
}

func TestParseDatagramRejectsInvalidDatagrams(t *testing.T) {
	if _, _, ok := ParseDatagram([]byte{}); ok {
		t.Error("ParseDatagram() accepted an empty datagram")
	}

	for _, kind := range []byte{0, 4, 0x0f, 0x13, 0xff} {
		if _, _, ok := ParseDatagram([]byte{kind, 0xaa}); ok {
			t.Errorf("ParseDatagram() accepted unknown kind %d", kind)
		}
	}

	oversized := make([]byte, MaxDatagram+1)
	oversized[0] = byte(KindTransport)
	if _, _, ok := ParseDatagram(oversized); ok {
		t.Errorf("ParseDatagram() accepted %d bytes, want maximum %d", len(oversized), MaxDatagram)
	}
}

func TestParseDatagramAcceptsKnownKinds(t *testing.T) {
	kinds := []DatagramKind{
		KindIKInitiation,
		KindIKResponse,
		KindTransport,
		KindXXMessage1,
		KindXXMessage2,
		KindXXMessage3,
	}

	for _, wantKind := range kinds {
		b := []byte{byte(wantKind), 0xaa}
		kind, body, ok := ParseDatagram(b)
		if !ok {
			t.Errorf("ParseDatagram() rejected kind %d", wantKind)
			continue
		}
		if kind != wantKind {
			t.Errorf("ParseDatagram() kind = %d, want %d", kind, wantKind)
		}
		if len(body) != 1 || body[0] != 0xaa {
			t.Errorf("ParseDatagram() body = %v, want [170]", body)
		}
	}
}

func TestParseDatagramAcceptsMaximumTransportDatagram(t *testing.T) {
	b := make([]byte, MaxDatagram)
	for i := 1; i < len(b); i++ {
		b[i] = byte(i)
	}
	b[0] = byte(KindTransport)

	kind, body, ok := ParseDatagram(b)
	if !ok {
		t.Fatal("ParseDatagram() rejected a maximum-sized transport datagram")
	}
	if kind != KindTransport {
		t.Fatalf("ParseDatagram() kind = %d, want %d", kind, KindTransport)
	}
	if len(body) != MaxDatagram-1 {
		t.Fatalf("ParseDatagram() body length = %d, want %d", len(body), MaxDatagram-1)
	}
	if !bytes.Equal(body, b[1:]) {
		t.Fatalf("ParseDatagram() body = %v, want %v", body, b[1:])
	}
}

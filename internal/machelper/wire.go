// Package machelper is the Linux side of the Mac raw-mouse helper channel:
// Noise (XX for pairing, IK for sessions) over UDP, the fixed-size wire
// format, the replay window and the limits. The Swift counterpart lives in
// machelper/Sources/HelperCore and both must stay byte compatible.
//
// UDP datagram:
//
//	handshake  kind(1) || noise message     kinds 0x01/0x02 (IK), 0x10/0x11/0x12 (XX)
//	           the IK initiation ends in mac1(16), see mac1.go
//	transport  0x03 || counter(8, LE) || AEAD(plaintext) || tag(16)
//	pairing    0x13, same layout, under the XX transport keys
//
// The 64-bit counter is the Noise nonce and the replay-window sequence number.
// All integers are little endian.
package machelper

import (
	"encoding/binary"

	E "github.com/sagernet/sing/common/exceptions"
)

type DatagramKind uint8

const (
	KindIKInitiation DatagramKind = 0x01
	KindIKResponse   DatagramKind = 0x02
	KindTransport    DatagramKind = 0x03
	KindXXMessage1   DatagramKind = 0x10
	KindXXMessage2   DatagramKind = 0x11
	KindXXMessage3   DatagramKind = 0x12
	// KindPairingTransport carries the commit, responder nonce and reveal
	// that follow the XX handshake, under the XX transport keys.
	KindPairingTransport DatagramKind = 0x13
)

const (
	// MaxDatagram is the largest datagram of any kind we look at.
	MaxDatagram = 512
	// TransportOverhead is kind + counter + AEAD tag.
	TransportOverhead = 1 + 8 + 16

	// MaxMotion is the clamp for dx/dy per packet: 2047 pt in points*16, so it
	// is never -32768 and negating it cannot overflow. The real limit is the
	// per-second budget below, enforced by the Mac.
	MaxMotion int16 = 2047 * 16
	// MotionUnitsPerSecond is the Mac's budget for |dx|+|dy| per second
	// (20 000 pt/s). A client should pace itself below it.
	MotionUnitsPerSecond       = 20_000 * 16
	MaxScroll            int16 = 2000

	InputPacketSize  = 20
	StatusPacketSize = 2
	statusType       = 10

	FlagPointerLocked uint8 = 1
	MaxDeviceName           = 64
)

var (
	ErrBadLength      = E.New("machelper: bad length")
	ErrBadType        = E.New("machelper: bad type")
	ErrReservedBits   = E.New("machelper: reserved bits set")
	ErrPadding        = E.New("machelper: non-zero padding")
	ErrUnexpectedData = E.New("machelper: unexpected fields for packet type")
	ErrBadState       = E.New("machelper: bad state")
)

type PacketType uint8

const (
	PacketInput     PacketType = 1
	PacketKeepalive PacketType = 2
	PacketBye       PacketType = 3
)

const (
	ButtonLeft    uint8 = 1 << 0
	ButtonRight   uint8 = 1 << 1
	ButtonMiddle  uint8 = 1 << 2
	ButtonBack    uint8 = 1 << 3
	ButtonForward uint8 = 1 << 4
	buttonsValid  uint8 = 0x1f
)

// InputPacket is Linux -> Mac, 20 bytes inside the Noise transport:
// 0 type, 1 buttons, 2 dx, 4 dy, 6 sx, 8 sy (int16), 10 seq (uint32), 14 flags, 15..19 zero.
type InputPacket struct {
	Type    PacketType
	Buttons uint8
	DX, DY  int16
	SX, SY  int16
	Seq     uint32
	Flags   uint8
}

func (p InputPacket) Encode() []byte {
	b := make([]byte, InputPacketSize)
	b[0] = byte(p.Type)
	b[1] = p.Buttons
	binary.LittleEndian.PutUint16(b[2:], uint16(p.DX))
	binary.LittleEndian.PutUint16(b[4:], uint16(p.DY))
	binary.LittleEndian.PutUint16(b[6:], uint16(p.SX))
	binary.LittleEndian.PutUint16(b[8:], uint16(p.SY))
	binary.LittleEndian.PutUint32(b[10:], p.Seq)
	b[14] = p.Flags
	return b
}

// DecodeInputPacket is strict: wrong length, unknown type, reserved bits,
// non-zero padding and motion on keepalive/bye packets are all errors.
func DecodeInputPacket(b []byte) (InputPacket, error) {
	var p InputPacket
	if len(b) != InputPacketSize {
		return p, ErrBadLength
	}
	p.Type = PacketType(b[0])
	if p.Type < PacketInput || p.Type > PacketBye {
		return p, ErrBadType
	}
	if b[1]&^buttonsValid != 0 || b[14]&^FlagPointerLocked != 0 {
		return p, ErrReservedBits
	}
	for _, v := range b[15:] {
		if v != 0 {
			return p, ErrPadding
		}
	}
	p.Buttons = b[1]
	p.DX = int16(binary.LittleEndian.Uint16(b[2:]))
	p.DY = int16(binary.LittleEndian.Uint16(b[4:]))
	p.SX = int16(binary.LittleEndian.Uint16(b[6:]))
	p.SY = int16(binary.LittleEndian.Uint16(b[8:]))
	p.Seq = binary.LittleEndian.Uint32(b[10:])
	p.Flags = b[14]
	if p.Type != PacketInput && (p.Buttons != 0 || p.DX != 0 || p.DY != 0 || p.SX != 0 || p.SY != 0) {
		return p, ErrUnexpectedData
	}
	return p, nil
}

// Clamped applies the per-packet clamps of the design.
func (p InputPacket) Clamped() InputPacket {
	p.DX = clamp16(p.DX, MaxMotion)
	p.DY = clamp16(p.DY, MaxMotion)
	p.SX = clamp16(p.SX, MaxScroll)
	p.SY = clamp16(p.SY, MaxScroll)
	return p
}

func clamp16(v, limit int16) int16 {
	if v > limit {
		return limit
	}
	if v < -limit {
		return -limit
	}
	return v
}

// HelperState is the reason the helper does or does not act on input.
type HelperState uint8

const (
	StateActive      HelperState = 0
	StateDisabled    HelperState = 1
	StateLocked      HelperState = 2
	StateNoSession   HelperState = 3
	StateNotConsole  HelperState = 4
	StateRateLimited HelperState = 5
	StateNeedsGrant  HelperState = 6
)

// StatusPacket is Mac -> Linux, 2 bytes inside the Noise transport.
type StatusPacket struct {
	State HelperState
}

func (p StatusPacket) Encode() []byte { return []byte{statusType, byte(p.State)} }

func DecodeStatusPacket(b []byte) (StatusPacket, error) {
	if len(b) != StatusPacketSize {
		return StatusPacket{}, ErrBadLength
	}
	if b[0] != statusType {
		return StatusPacket{}, ErrBadType
	}
	if b[1] > byte(StateNeedsGrant) {
		return StatusPacket{}, ErrBadState
	}
	return StatusPacket{State: HelperState(b[1])}, nil
}

// ParseDatagram splits kind and body after length checks. ok is false for
// anything that should be dropped silently.
func ParseDatagram(b []byte) (kind DatagramKind, body []byte, ok bool) {
	if len(b) < 1 || len(b) > MaxDatagram {
		return 0, nil, false
	}
	switch k := DatagramKind(b[0]); k {
	case KindIKInitiation, KindIKResponse, KindTransport, KindXXMessage1, KindXXMessage2, KindXXMessage3,
		KindPairingTransport:
		return k, b[1:], true
	}
	return 0, nil, false
}

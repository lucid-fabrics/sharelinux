package machelper

import (
	"crypto/hmac"
	"crypto/sha256"
)

// MAC1Size is the length of the keyed MAC that ends every IK initiation.
const MAC1Size = 16

var mac1Label = []byte("OSS-mousehelper v2 mac1")

func mac1Key(macPublic []byte) []byte {
	h := sha256.New()
	h.Write(mac1Label)
	h.Write(macPublic)
	return h.Sum(nil)
}

func mac1Tag(message, macPublic []byte) []byte {
	m := hmac.New(sha256.New, mac1Key(macPublic))
	m.Write(message)
	return m.Sum(nil)[:MAC1Size]
}

// AppendMAC1 adds the WireGuard-style cheap filter to an IK initiation
// (kind byte included). The key is derived from the Mac's static public key,
// so only a sender who knows it gets past the Mac's first check, which costs
// one HMAC and runs before any rate limit or Curve25519 work.
func AppendMAC1(message, macPublic []byte) []byte {
	out := append([]byte(nil), message...)
	return append(out, mac1Tag(message, macPublic)...)
}

// VerifyMAC1 checks, in constant time, a full initiation datagram that ends in
// mac1.
func VerifyMAC1(datagram, macPublic []byte) bool {
	if len(datagram) <= MAC1Size {
		return false
	}
	split := len(datagram) - MAC1Size
	return hmac.Equal(mac1Tag(datagram[:split], macPublic), datagram[split:])
}

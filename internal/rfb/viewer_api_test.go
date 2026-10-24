package rfb

import (
	"encoding/binary"
	"testing"
)

func TestStatisticsBeforeEncryptionIsZero(t *testing.T) {
	c := &Client{stream: &stream{}}
	if got := c.Statistics(); got.WireBytes != 0 {
		t.Fatalf("got %d", got.WireBytes)
	}
}

func TestRecordReaderCountsWireBytes(t *testing.T) {
	var r recordReader
	r.wireBytes.Add(10)
	c := &Client{stream: &stream{records: &r}}
	if got := c.Statistics().WireBytes; got != 10 {
		t.Fatalf("got %d", got)
	}
}

func TestServerInitAdvertisesTheGivenFlags(t *testing.T) {
	message := serverInitMessage(4, 4, ServerFlagObserveOnly|ServerFlagExtended, "x")
	// width, height, 16 byte pixel format, name length, then 2 bytes and the flags.
	flags := binary.BigEndian.Uint32(message[4+16+4+2:])
	if flags != ServerFlagObserveOnly|ServerFlagExtended {
		t.Fatalf("flags %#x", flags)
	}
}

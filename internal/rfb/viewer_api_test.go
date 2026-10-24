package rfb

import "testing"

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

package rfb

import (
	"bytes"
	"strings"
	"testing"
)

// The resize can come in a later update. The rectangle waits across the
// update boundary and later rectangles queue behind it.
func TestMVSHeldAcrossUpdates(t *testing.T) {
	client := smallClient(concat(
		mvsRectangle(24, 0, mvsFullRefine(false, 3)),
		rectangleBytes(0, 0, 1, 1, EncodingRaw, 1, 2, 3, 255),
		mvsRectangle(0, 0, mvsFullRefine(true, 3)),
		resize32,
		mvsRectangle(0, 8, mvsFullCache(1)),
	))
	if _, err := client.receiveRectangles(2); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if len(client.decoders.deferred) != 1 {
		t.Fatalf("%d rectangles wait, want 1", len(client.decoders.deferred))
	}
	if _, err := client.receiveRectangles(3); err != nil {
		t.Fatalf("second update: %v", err)
	}
	first := drawn(t, client, 24, 0)
	if got := drawn(t, client, 0, 8); !bytes.Equal(got, first) {
		t.Fatalf("cache entry 1 drew %v, want the held tile %v", got, first)
	}
}

// With no fitting resize the session fails after the update limit, and the
// error says what was waiting.
func TestMVSHeldFailsAfterUpdateLimit(t *testing.T) {
	var data []byte
	data = append(data, mvsRectangle(24, 0, mvsFullRefine(false, 3))...)
	for i := 0; i < 3; i++ {
		data = append(data, rectangleBytes(0, 0, 1, 1, EncodingRaw, 1, 2, 3, 255)...)
	}
	client := smallClient(data)
	client.decoders.deferredUpdateLimit = 2
	for i := 0; i < 2; i++ {
		count := 1
		if i == 0 {
			count = 2
		}
		if _, err := client.receiveRectangles(count); err != nil {
			t.Fatalf("update %d: %v", i+1, err)
		}
	}
	_, err := client.receiveRectangles(1)
	if err == nil || !strings.Contains(err.Error(), "8x8 at 24,0") || !strings.Contains(err.Error(), "no resize that fits") {
		t.Fatalf("error %v, want the waiting rectangle named and no resize reported", err)
	}
	if len(client.decoders.deferred) != 0 || client.decoders.deferredBytes != 0 {
		t.Fatal("the queue was not dropped after the failure")
	}
}

// Two resizes in one update: a waiting rectangle is decoded against the final
// geometry, after the last resize reset the decoder, so the cache entry it
// inserts survives for the cache hit that follows.
func TestMVSHeldDecodesAgainstFinalGeometry(t *testing.T) {
	client := smallClient(concat(
		mvsRectangle(24, 0, mvsFullRefine(false, 3)),
		resize32,
		rectangleBytes(0, 0, 64, 64, EncodingDesktopSize),
		mvsRectangle(0, 8, mvsFullCache(1)),
	))
	if _, err := client.receiveRectangles(4); err != nil {
		t.Fatal(err)
	}
	if client.framebuffer.Width != 64 {
		t.Fatalf("framebuffer %dx%d, want 64x64", client.framebuffer.Width, client.framebuffer.Height)
	}
	first := drawn(t, client, 24, 0)
	if got := drawn(t, client, 0, 8); !bytes.Equal(got, first) {
		t.Fatalf("cache entry 1 drew %v, want the held tile %v", got, first)
	}
}

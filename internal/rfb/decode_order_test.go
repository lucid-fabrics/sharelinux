package rfb

import (
	"bytes"
	"testing"
)

var resize32 = rectangleBytes(0, 0, 32, 32, EncodingDesktopSize)

func concat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

func drawn(t *testing.T, client *Client, x, y int) []byte {
	t.Helper()
	pixel := pixelAt(client, x, y)
	if bytes.Equal(pixel, make([]byte, 4)) {
		t.Fatalf("nothing was drawn at %d,%d", x, y)
	}
	return pixel
}

// An in-bounds rectangle that arrives behind a waiting one waits too, so the
// cache entries are numbered in stream order and the cache hits that follow
// the resize resolve to the right tile.
func TestMVSInBoundsBehindHeldRectangle(t *testing.T) {
	client := smallClient(concat(
		mvsRectangle(24, 0, mvsFullRefine(false, 3)),
		mvsRectangle(0, 0, mvsFullRefine(true, 3)),
		resize32,
		mvsRectangle(0, 8, mvsFullCache(1)),
		mvsRectangle(8, 8, mvsFullCache(2)),
	))
	update, err := client.receiveRectangles(5)
	if err != nil {
		t.Fatal(err)
	}
	first, second := drawn(t, client, 24, 0), drawn(t, client, 0, 0)
	if bytes.Equal(first, second) {
		t.Fatal("the two refined tiles look the same, the test cannot tell the order")
	}
	if got := drawn(t, client, 0, 8); !bytes.Equal(got, first) {
		t.Fatalf("cache entry 1 drew %v, want the held tile %v", got, first)
	}
	if got := drawn(t, client, 8, 8); !bytes.Equal(got, second) {
		t.Fatalf("cache entry 2 drew %v, want the queued tile %v", got, second)
	}
	if len(update.Rectangles) != 4 {
		t.Fatalf("%d rectangles reported, want 4", len(update.Rectangles))
	}
}

// A quantisation update behind a waiting rectangle applies in stream order:
// the frame after the resize decodes with the new tables.
func TestMVSQuantizationBehindHeldRectangle(t *testing.T) {
	refine := mvsRectangle(0, 8, mvsFullRefine(false, 3))
	plain := smallClient(concat(resize32, refine))
	if _, err := plain.receiveRectangles(2); err != nil {
		t.Fatal(err)
	}
	reference := smallClient(concat(resize32, mvsRectangle(0, 0, mvsQuantization(120)), refine))
	if _, err := reference.receiveRectangles(3); err != nil {
		t.Fatal(err)
	}
	want := pixelAt(reference, 0, 8)
	if bytes.Equal(want, pixelAt(plain, 0, 8)) {
		t.Fatal("the quantisation update changes nothing, the test cannot tell the order")
	}
	client := smallClient(concat(
		mvsRectangle(24, 0, mvsFullRefine(false, 3)),
		mvsRectangle(0, 0, mvsQuantization(120)),
		resize32,
		refine,
	))
	if _, err := client.receiveRectangles(4); err != nil {
		t.Fatal(err)
	}
	if got := pixelAt(client, 0, 8); !bytes.Equal(got, want) {
		t.Fatalf("frame after the resize drew %v, want %v with the new tables", got, want)
	}
}

// LastRect ends the update early. The waiting rectangle is still decoded when
// the resize came before it.
func TestMVSHeldEndsWithLastRect(t *testing.T) {
	client := smallClient(concat(
		mvsRectangle(24, 0, mvsFullRefine(false, 3)),
		resize32,
		rectangleBytes(0, 0, 0, 0, EncodingLastRect),
		[]byte("never read"),
	))
	if _, err := client.receiveRectangles(4); err != nil {
		t.Fatal(err)
	}
	drawn(t, client, 24, 0)
	if len(client.decoders.deferred) != 0 {
		t.Fatal("a rectangle still waits")
	}
}

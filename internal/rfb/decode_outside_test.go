package rfb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"log/slog"
	"strings"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
)

type resizeHandler struct {
	Handler
}

func (resizeHandler) ResizeFramebuffer(width, height int) (*bgra.Image, error) {
	return nil, nil
}

func rectangleBytes(x, y, width, height int, encoding Encoding, body ...byte) []byte {
	header := make([]byte, 12)
	binary.BigEndian.PutUint16(header[0:], uint16(x))
	binary.BigEndian.PutUint16(header[2:], uint16(y))
	binary.BigEndian.PutUint16(header[4:], uint16(width))
	binary.BigEndian.PutUint16(header[6:], uint16(height))
	binary.BigEndian.PutUint32(header[8:], uint32(encoding))
	return append(header, body...)
}

func newTestClient(data []byte, log *bytes.Buffer) *Client {
	reader := bufio.NewReader(bytes.NewReader(data))
	return &Client{
		handler:     resizeHandler{},
		logger:      slog.New(slog.NewTextHandler(log, nil)),
		stream:      &stream{raw: reader, source: reader},
		framebuffer: bgra.New(6522, 4154),
		width:       6522,
		height:      4154,
	}
}

type bitWriter struct {
	data      []byte
	bitOffset int
}

func (w *bitWriter) bits(value uint32, n int) {
	if n < 0 || n > 32 {
		panic("bitWriter: invalid bit count")
	}

	for i := n - 1; i >= 0; i-- {
		bit := (value >> uint(i)) & 1
		byteIndex := w.bitOffset / 8

		if byteIndex == len(w.data) {
			w.data = append(w.data, 0)
		}

		bitIndex := 7 - (w.bitOffset % 8)
		w.data[byteIndex] |= byte(bit << uint(bitIndex))
		w.bitOffset++
	}
}

func (w *bitWriter) bytes() []byte {
	return append([]byte(nil), w.data...)
}

func mvsRectangle(x, y int, payload []byte) []byte {
	body := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	return rectangleBytes(x, y, 8, 8, EncodingMVS, append(body, payload...)...)
}

const mvsEndMarker = 0x6d7673

// mvsFullRefine is a full update of one tile that stores a flat tile with the
// given luma DC (rice coded, magnitude 2 or 3) and inserts it into the cache.
func mvsFullRefine(negative bool, magnitude uint32) []byte {
	var w bitWriter
	w.bits(1, 2)
	w.bits(0, 6)
	w.bits(1, 1)
	w.bits(0, 1)
	w.bits((magnitude-2)<<1|b2u(negative), 2)
	w.bits(0, 1)
	w.bits(0, 1)
	w.bits(mvsEndMarker, 24)
	return append([]byte{1, 0, 0}, w.bytes()...)
}

// mvsFullCache is a full update of one tile that reads cache entry index.
func mvsFullCache(index uint32) []byte {
	var w bitWriter
	w.bits(3, 2)
	w.bits(0, 1)
	w.bits(index, 16)
	w.bits(mvsEndMarker, 24)
	return append([]byte{1, 0, 0}, w.bytes()...)
}

// mvsQuantization is a quantisation update with every luma step set to step.
func mvsQuantization(step byte) []byte {
	payload := make([]byte, 129)
	payload[0] = 2
	for i := 1; i < 65; i++ {
		payload[i] = step
	}
	for i := 65; i < 129; i++ {
		payload[i] = 16
	}
	return payload
}

func b2u(value bool) uint32 {
	if value {
		return 1
	}
	return 0
}

func smallClient(data []byte) *Client {
	client := newTestClient(data, &bytes.Buffer{})
	client.framebuffer = bgra.New(16, 16)
	client.width, client.height = 16, 16
	return client
}

func pixelAt(client *Client, x, y int) []byte {
	offset := y*client.framebuffer.Stride + x*4
	return append([]byte(nil), client.framebuffer.Pix[offset:offset+4]...)
}

// The Mac draws for a new geometry before it announces the resize in the same
// update. The rectangle is decoded once the resize makes it fit.
func TestMVSRectangleOutsideBeforeResize(t *testing.T) {
	var data []byte
	data = append(data, mvsRectangle(24, 0, mvsFullRefine(false, 3))...)
	data = append(data, rectangleBytes(0, 0, 32, 32, EncodingDesktopSize)...)
	client := smallClient(data)
	update, err := client.receiveRectangles(2)
	if err != nil {
		t.Fatal(err)
	}
	if client.framebuffer.Width != 32 || client.framebuffer.Height != 32 {
		t.Fatalf("framebuffer %dx%d, want 32x32", client.framebuffer.Width, client.framebuffer.Height)
	}
	if len(update.Rectangles) != 1 || update.Rectangles[0].X != 24 {
		t.Fatalf("rectangles %v, want the deferred one at x=24", update.Rectangles)
	}
	if bytes.Equal(pixelAt(client, 24, 0), make([]byte, 4)) {
		t.Fatal("the deferred rectangle was never drawn")
	}
}

// The deferred rectangle inserts its cache entry in arrival order, so a cache
// reference after the resize resolves to it.
func TestMVSCacheReferenceAfterDeferredRectangle(t *testing.T) {
	var data []byte
	data = append(data, mvsRectangle(24, 0, mvsFullRefine(true, 3))...)
	data = append(data, rectangleBytes(0, 0, 32, 32, EncodingDesktopSize)...)
	data = append(data, mvsRectangle(0, 8, mvsFullCache(1))...)
	client := smallClient(data)
	_, err := client.receiveRectangles(3)
	if err != nil {
		t.Fatal(err)
	}
	drawn := pixelAt(client, 24, 0)
	if bytes.Equal(drawn, make([]byte, 4)) {
		t.Fatal("the deferred rectangle was never drawn")
	}
	if got := pixelAt(client, 0, 8); !bytes.Equal(got, drawn) {
		t.Fatalf("cache hit drew %v, want entry 1 %v", got, drawn)
	}
}

// A quantisation update in a rectangle outside the framebuffer still applies.
func TestMVSQuantizationOutsideFramebufferApplies(t *testing.T) {
	tile := mvsRectangle(0, 0, mvsFullRefine(false, 3))
	plain := smallClient(tile)
	_, err := plain.receiveRectangles(1)
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	data = append(data, mvsRectangle(6000, 6000, mvsQuantization(120))...)
	data = append(data, tile...)
	client := smallClient(data)
	_, err = client.receiveRectangles(2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(pixelAt(client, 0, 0), pixelAt(plain, 0, 0)) {
		t.Fatal("the quantisation update was lost")
	}
}

// A rectangle that still does not fit when the update ends is an error.
func TestMVSRectangleStillOutsideAtEndOfUpdate(t *testing.T) {
	var data []byte
	data = append(data, mvsRectangle(24, 0, mvsFullRefine(false, 3))...)
	data = append(data, rectangleBytes(0, 0, 1, 1, EncodingRaw, 1, 2, 3, 255)...)
	client := smallClient(data)
	_, err := client.receiveRectangles(2)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("error %v, want a rectangle outside the framebuffer", err)
	}
}

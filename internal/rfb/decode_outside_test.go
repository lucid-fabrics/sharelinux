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

func mvsBytes(x, y, width, height int) []byte {
	payload := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07}
	body := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	return rectangleBytes(x, y, width, height, EncodingMVS, append(body, payload...)...)
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

// The Mac draws for a new geometry before the client has heard of it. The
// rectangle is dropped and the resize that follows in the same update applies.
func TestMVSRectangleOutsideBeforeResize(t *testing.T) {
	var data []byte
	data = append(data, mvsBytes(6084, 1408, 752, 80)...)
	data = append(data, rectangleBytes(0, 0, 6900, 4200, EncodingDesktopSize)...)
	var log bytes.Buffer
	client := newTestClient(data, &log)
	update, err := client.receiveRectangles(2)
	if err != nil {
		t.Fatal(err)
	}
	if client.framebuffer.Width != 6900 || client.framebuffer.Height != 4200 {
		t.Fatalf("framebuffer %dx%d, want 6900x4200", client.framebuffer.Width, client.framebuffer.Height)
	}
	if len(update.Rectangles) != 0 {
		t.Fatalf("the dropped rectangle was reported: %v", update.Rectangles)
	}
	if !strings.Contains(log.String(), "outside the framebuffer") {
		t.Fatalf("the drop left no log line: %q", log.String())
	}
}

// The dropped rectangle's payload is consumed, so the next rectangle reads cleanly.
func TestMVSRectangleOutsideKeepsStreamInSync(t *testing.T) {
	var data []byte
	data = append(data, mvsBytes(6084, 1408, 752, 80)...)
	data = append(data, rectangleBytes(0, 0, 1, 1, EncodingRaw, 1, 2, 3, 255)...)
	var log bytes.Buffer
	client := newTestClient(data, &log)
	update, err := client.receiveRectangles(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Rectangles) != 1 || update.Rectangles[0].Encoding != EncodingRaw {
		t.Fatalf("rectangles %v, want the raw one only", update.Rectangles)
	}
	if got := client.framebuffer.Pix[:4]; !bytes.Equal(got, []byte{1, 2, 3, 255}) {
		t.Fatalf("pixel %v", got)
	}
}

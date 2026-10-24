package rfb

import (
	"encoding/binary"

	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
)

// This file holds the few Client methods the Linux viewer (cmd/sharelinux) needs
// that upstream removed. They live apart so merging upstream never conflicts here.

// Framebuffer calls fn with the picture while the decoder is held off.
func (c *Client) Framebuffer(fn func(*bgra.Image)) {
	c.framebufferAccess.Lock()
	defer c.framebufferAccess.Unlock()
	fn(c.framebuffer)
}

// Statistics counts what the connection has carried.
type Statistics struct {
	// WireBytes is the size of every encrypted record read so far, headers included.
	WireBytes uint64
}

func (c *Client) Statistics() Statistics {
	if c.stream.records == nil {
		return Statistics{}
	}
	return Statistics{WireBytes: c.stream.records.wireBytes.Load()}
}

// RequestDrag asks the Mac to start offering the files it is dragging.
// ScreensharingAgent ignores it when no drag is in progress, so nothing answers.
func (c *Client) RequestDrag() error {
	err := c.checkDrag(0x0e)
	if err != nil {
		return err
	}
	message := []byte{0x0e, 0, 0, 0}
	return c.send(binary.BigEndian.AppendUint32(message, newDragID()))
}

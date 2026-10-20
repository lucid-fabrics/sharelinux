package main

import (
	"sync"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

const (
	typePNG  = "public.png"
	mimePNG  = "image/png"
	mimeText = "text/plain;charset=utf-8"
)

// clipboardBridge keeps the local and the Mac's clipboards equal. Text goes both
// ways; an image copied on the Mac is offered to local programs as image/png.
type clipboardBridge struct {
	client *rfb.Client
	// last is the text both sides last agreed on. It stops a copy from bouncing back.
	last string

	access   sync.Mutex
	offer    map[string][]byte
	callback sdl.ClipboardDataCallback
	cleanup  sdl.ClipboardCleanupCallback
}

func newClipboardBridge(client *rfb.Client) *clipboardBridge {
	bridge := &clipboardBridge{client: client}
	bridge.callback = sdl.NewClipboardDataCallback(bridge.data)
	bridge.cleanup = sdl.NewClipboardCleanupCallback(func() {})
	return bridge
}

func (b *clipboardBridge) data(mime string) []byte {
	b.access.Lock()
	defer b.access.Unlock()
	return b.offer[mime]
}

// fromMac puts what the Mac copied on the local clipboard.
func (b *clipboardBridge) fromMac(clipboard *rfb.Clipboard) error {
	text, hasText := clipboard.Text()
	var picture []byte
	for _, item := range clipboard.Items {
		for _, flavor := range item.Flavors {
			if flavor.Type == typePNG && !flavor.Promise && len(flavor.Data) > 0 {
				picture = flavor.Data
			}
		}
	}
	switch {
	case picture != nil:
		offer := map[string][]byte{mimePNG: picture}
		mimes := []string{mimePNG}
		if hasText {
			offer[mimeText] = []byte(text)
			mimes = append(mimes, mimeText)
		}
		b.access.Lock()
		b.offer = offer
		b.access.Unlock()
		b.last = text
		return sdl.SetClipboardData(b.callback, b.cleanup, mimes)
	case hasText && text != "" && text != b.last:
		b.last = text
		return sdl.SetClipboardText(text)
	}
	return nil
}

// toMac sends the local clipboard text to the Mac when it changed.
func (b *clipboardBridge) toMac() error {
	text, err := sdl.GetClipboardText()
	if err != nil || text == "" || text == b.last {
		return nil
	}
	b.last = text
	return b.client.SetClipboard(rfb.NewTextClipboard(text))
}

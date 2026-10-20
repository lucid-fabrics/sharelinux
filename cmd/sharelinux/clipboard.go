package main

import (
	"sync"
	"sync/atomic"

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

	access sync.Mutex
	offer  map[string][]byte
}

// purego can create only about 2000 callbacks for the life of the process and
// never frees them, so the two SDL callbacks are made once and serve whichever
// bridge is active.
var (
	callbackOnce    sync.Once
	dataCallback    sdl.ClipboardDataCallback
	cleanupCallback sdl.ClipboardCleanupCallback
	activeBridge    atomic.Pointer[clipboardBridge]
)

func clipboardCallbacks() (sdl.ClipboardDataCallback, sdl.ClipboardCleanupCallback) {
	callbackOnce.Do(func() {
		dataCallback = sdl.NewClipboardDataCallback(func(mime string) []byte {
			bridge := activeBridge.Load()
			if bridge == nil {
				return nil
			}
			return bridge.data(mime)
		})
		cleanupCallback = sdl.NewClipboardCleanupCallback(func() {})
	})
	return dataCallback, cleanupCallback
}

func newClipboardBridge(client *rfb.Client) *clipboardBridge {
	bridge := &clipboardBridge{client: client}
	clipboardCallbacks()
	activeBridge.Store(bridge)
	return bridge
}

// close stops the shared callbacks from reading this bridge.
func (b *clipboardBridge) close() {
	activeBridge.CompareAndSwap(b, nil)
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
		callback, cleanup := clipboardCallbacks()
		return sdl.SetClipboardData(callback, cleanup, mimes)
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

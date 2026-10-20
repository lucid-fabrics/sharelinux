//go:build linux

package main

import (
	"crypto/sha256"
	"sync"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	typePNG  = "public.png"
	mimePNG  = "image/png"
	mimeText = "text/plain;charset=utf-8"
)

// clipboardBridge keeps the local and the Mac's clipboards equal. Text and
// images go both ways. Every call runs on the main loop.
type clipboardBridge struct {
	client    *rfb.Client
	clipboard *gdk.Clipboard
	notify    func(string)
	handle    coreglib.SignalHandle

	// last is the text both sides last agreed on, lastPicture the picture. They
	// stop a copy from bouncing back.
	last        string
	lastPicture [sha256.Size]byte

	access sync.Mutex
	closed bool
}

func newClipboardBridge(client *rfb.Client, widget gtk.Widgetter, notify func(string)) *clipboardBridge {
	bridge := &clipboardBridge{client: client, notify: notify}
	bridge.clipboard = gtk.BaseWidget(widget).Display().Clipboard()
	bridge.handle = bridge.clipboard.ConnectChanged(func() {
		// A change this program made (a copy from the Mac) is not sent back.
		if bridge.clipboard.IsLocal() {
			return
		}
		bridge.toMac()
	})
	return bridge
}

func (b *clipboardBridge) isClosed() bool {
	b.access.Lock()
	defer b.access.Unlock()
	return b.closed
}

// close stops the bridge from reading the clipboard.
func (b *clipboardBridge) close() {
	b.access.Lock()
	b.closed = true
	b.access.Unlock()
	b.clipboard.HandlerDisconnect(b.handle)
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
		b.lastPicture = sha256.Sum256(picture)
		providers := []*gdk.ContentProvider{gdk.NewContentProviderForBytes(mimePNG, glib.NewBytesWithGo(picture))}
		if hasText {
			b.last = text
			providers = append(providers, gdk.NewContentProviderForValue(coreglib.NewValue(text)))
		}
		if !b.clipboard.SetContent(gdk.NewContentProviderUnion(providers)) {
			return E.New("the clipboard did not take the picture")
		}
	case hasText && text != "" && text != b.last:
		b.last = text
		b.clipboard.SetText(text)
	}
	return nil
}

// toMac sends the local clipboard to the Mac when it changed.
func (b *clipboardBridge) toMac() {
	if b.isClosed() {
		return
	}
	formats := b.clipboard.Formats()
	switch {
	case formats.ContainGType(coreglib.TypeString):
		b.clipboard.ReadTextAsync(nil, func(result gio.AsyncResulter) {
			text, err := b.clipboard.ReadTextFinish(result)
			if err != nil || text == "" || text == b.last || b.isClosed() {
				return
			}
			b.last = text
			err = b.client.SetClipboard(rfb.NewTextClipboard(text))
			if err != nil {
				b.notify("Clipboard: " + err.Error())
			}
		})
	case formats.ContainMIMEType(mimePNG) || formats.ContainGType(gdk.GTypeTexture):
		b.clipboard.ReadTextureAsync(nil, func(result gio.AsyncResulter) {
			texture, err := b.clipboard.ReadTextureFinish(result)
			if err != nil || texture == nil || b.isClosed() {
				return
			}
			data := gdk.BaseTexture(texture).SaveToPNGBytes().Data()
			sum := sha256.Sum256(data)
			if sum == b.lastPicture {
				return
			}
			b.lastPicture = sum
			clipboard := &rfb.Clipboard{Items: []rfb.ClipboardItem{{Flavors: []rfb.ClipboardFlavor{{Type: typePNG, Data: data}}}}}
			err = b.client.SetClipboard(clipboard)
			if err != nil {
				b.notify("Clipboard: " + err.Error())
			}
		})
	}
}

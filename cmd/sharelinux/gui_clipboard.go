//go:build linux

package main

import (
	"context"
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
	// allow says whether the local clipboard may go to the Mac. It is false in
	// View mode: someone who only watches has not agreed to share what they copy.
	allow  func() bool
	handle coreglib.SignalHandle
	// withheld is set when the local clipboard changed while it could not be
	// sent. Entering Control later must not send what was copied in View mode;
	// the next copy clears it.
	withheld bool

	// last is the text both sides last agreed on, lastPicture the picture. They
	// stop a copy from bouncing back.
	last        string
	lastPicture [sha256.Size]byte

	access sync.Mutex
	closed bool
}

func newClipboardBridge(client *rfb.Client, widget gtk.Widgetter, notify func(string), allow func() bool) *clipboardBridge {
	bridge := &clipboardBridge{client: client, notify: notify, allow: allow}
	bridge.clipboard = gtk.BaseWidget(widget).Display().Clipboard()
	bridge.handle = bridge.clipboard.ConnectChanged(func() {
		// A change this program made (a copy from the Mac) is not sent back.
		if bridge.clipboard.IsLocal() {
			return
		}
		if bridge.noteLocalChange() {
			bridge.toMac()
		}
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

// permitted reports whether this bridge may send at all right now.
func (b *clipboardBridge) permitted() bool {
	return !b.isClosed() && b.allow != nil && b.allow()
}

// noteLocalChange records that the local clipboard changed and says whether
// the new content may go to the Mac. A copy made while sending is not allowed
// is withheld for good, even if sending becomes allowed afterwards.
func (b *clipboardBridge) noteLocalChange() bool {
	b.withheld = !b.permitted()
	return !b.withheld
}

// mayCopyToMac reports whether the local clipboard may be sent now.
func (b *clipboardBridge) mayCopyToMac() bool {
	return b.permitted() && !b.withheld
}

// toMac sends the local clipboard to the Mac when it changed, and only while
// controlling it.
func (b *clipboardBridge) toMac() {
	if !b.mayCopyToMac() {
		return
	}
	formats := b.clipboard.Formats()
	switch {
	case formats.ContainGType(coreglib.TypeString):
		b.clipboard.ReadTextAsync(context.Background(), func(result gio.AsyncResulter) {
			text, err := b.clipboard.ReadTextFinish(result)
			if err != nil || text == "" || text == b.last || !b.mayCopyToMac() {
				return
			}
			b.last = text
			err = b.client.SetClipboard(rfb.NewTextClipboard(text))
			if err != nil {
				b.notify("Clipboard: " + err.Error())
			}
		})
	case formats.ContainMIMEType(mimePNG) || formats.ContainGType(gdk.GTypeTexture):
		b.clipboard.ReadTextureAsync(context.Background(), func(result gio.AsyncResulter) {
			texture, err := b.clipboard.ReadTextureFinish(result)
			if err != nil || texture == nil || !b.mayCopyToMac() {
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

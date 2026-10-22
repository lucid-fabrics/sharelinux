//go:build linux

package main

import (
	"context"
	"encoding/hex"
	"net"
	"sort"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/flynn/noise"
	"github.com/nekohasekai/open-screen-sharing/internal/machelper"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// flushEvery is the shortest gap between helper packets for motion: 250 a
// second, well under the Mac's 1000 a second, and finer than any display.
const flushEvery = 4 * time.Millisecond

// helperUse is one session's use of the Mac mouse helper: which paired Mac is
// this one, the helper session while Exclusive mode is on, the header chip and
// the routing of the locked pointer. Everything here runs on the GTK main loop;
// the helper link reports from its own goroutine through onMain.
type helperUse struct {
	v    *sessionView
	chip *gtk.Label

	// known is set once the paired Macs were read; paired once one of them is
	// the Mac this session is connected to.
	known  bool
	paired bool
	mac    pairedMac
	dial   string
	static noise.DHKey

	link   *machelper.Link
	linked bool
	status machelper.LinkStatus
	gen    int
	route  helperRoute

	lastFlush   time.Time
	flushTimer  coreglib.SourceHandle
	flushReport uint64 // cursor reports the Mac had made at the last flush
	engagedAt   time.Time
}

func newHelperUse(v *sessionView) *helperUse {
	h := &helperUse{v: v}
	h.route.send = h.sendPacket
	h.chip = gtk.NewLabel("")
	h.chip.AddCSSClass("oss-chip")
	h.chip.SetVAlign(gtk.AlignCenter)
	h.chip.SetVisible(false)
	return h
}

func (h *helperUse) sendPacket(p machelper.InputPacket) error {
	if h.link == nil {
		return machelper.ErrLinkNotReady
	}
	return h.link.Send(p)
}

func (h *helperUse) enabled() bool { return h.v.g.app.preferences().MouseHelper }

// routed reports whether locked-pointer input goes to the helper.
func (h *helperUse) routed() bool { return h.route.mode == routeHelper }

// begin reads which paired Mac this session is, off the main thread: the
// keyring can ask for an unlock, and a name may need a lookup.
func (h *helperUse) begin() {
	v := h.v
	keys := v.g.app.helperKeys()
	host := v.live.req.target.Host
	go func() {
		macs, err := keys.Macs()
		var mac pairedMac
		var dial string
		var found bool
		var static noise.DHKey
		if err == nil {
			mac, dial, found = helperTarget(host, macs, lookupHost)
		}
		if found {
			if static, err = keys.ClientKey(); err != nil {
				v.g.app.logger.Info("mouse helper: no client key", "error", err)
				found = false
			}
		}
		onMain(func() {
			if v.ended {
				return
			}
			changed := h.paired != found || h.mac.Key != mac.Key || h.dial != dial
			h.known, h.paired, h.mac, h.dial, h.static = true, found, mac, dial, static
			if changed && h.link != nil {
				h.stopLink()
			}
			h.sync()
		})
	}()
}

// reload re-reads the pairing, after the person paired or removed a Mac.
func (h *helperUse) reload() {
	if h.v.ended {
		return
	}
	h.begin()
}

// lookupHost resolves a name for the "same Mac" test, briefly.
func lookupHost(name string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupHost(ctx, name)
	if err != nil {
		return nil
	}
	sort.Strings(addresses)
	return addresses
}

// sync starts or stops the helper session to match Exclusive mode and the
// setting, then refreshes the chip and the routing.
func (h *helperUse) sync() {
	v := h.v
	want := h.known && h.paired && h.enabled() && v.mode == rfb.ModeExclusiveControl && !v.ended
	switch {
	case want && h.link == nil:
		h.startLink()
	case !want && h.link != nil:
		h.stopLink()
	}
	h.updateChip()
	h.reroute()
}

func (h *helperUse) startLink() {
	h.gen++
	gen := h.gen
	macKey, err := hex.DecodeString(h.mac.Key)
	if err != nil {
		return
	}
	link, err := machelper.StartLink(machelper.LinkConfig{
		Address: h.dial, Static: h.static, MacKey: macKey,
		OnStatus: func(st machelper.LinkStatus) {
			onMain(func() { h.onStatus(gen, st) })
		},
	})
	if err != nil {
		h.v.g.app.logger.Info("mouse helper: could not start", "error", err)
		h.status, h.linked = machelper.LinkStatus{State: machelper.LinkDown, Reason: err}, true
		return
	}
	h.link, h.linked = link, false
	h.status = machelper.LinkStatus{State: machelper.LinkConnecting}
	h.v.g.app.logger.Info("mouse helper: session started", "mac", h.mac.Name)
}

// stopLink ends the session. Routing drops first, so the Mac gets a packet
// with the buttons up while the link can still send it.
func (h *helperUse) stopLink() {
	if h.routed() {
		h.endRouting(true)
	}
	link := h.link
	h.link, h.linked = nil, false
	h.gen++
	h.status = machelper.LinkStatus{}
	if link != nil {
		go link.Close()
		h.v.g.app.logger.Info("mouse helper: session ended")
	}
	h.updateChip()
}

func (h *helperUse) onStatus(gen int, st machelper.LinkStatus) {
	if gen != h.gen || h.link == nil || h.v.ended {
		return
	}
	h.status, h.linked = st, true
	h.v.g.app.logger.Debug("mouse helper status", "state", int(st.State), "helper", int(st.Helper), "reason", st.Reason)
	h.updateChip()
	h.reroute()
}

func (h *helperUse) updateChip() {
	v := h.v
	show := h.known && h.enabled() && v.mode == rfb.ModeExclusiveControl && !v.ended
	h.chip.SetVisible(show)
	if !show {
		return
	}
	look := chipFor(h.paired, h.linked, h.status)
	h.chip.SetLabel(look.Label)
	h.chip.SetTooltipText(look.Tooltip)
	for _, class := range []string{"dim-label", "success", "warning"} {
		h.chip.RemoveCSSClass(class)
	}
	h.chip.AddCSSClass(look.Class)
}

// reroute applies the routing rules to the current facts. It is cheap and
// runs on every change of one of them and once per poll.
func (h *helperUse) reroute() {
	v := h.v
	in := routeInputs{
		lockHeld:   v.lock.held && v.lockBackend.relative() && v.controlling() && !v.ended,
		enabled:    h.enabled(),
		linkActive: h.link != nil && h.status.Active(),
		rfbButtons: v.buttons,
	}
	switch h.route.decide(in) {
	case routeEngaged:
		h.engage()
	case routeDropped:
		h.afterDrop()
	}
}

func (h *helperUse) engage() {
	v := h.v
	h.lastFlush = time.Time{}
	h.engagedAt = time.Now()
	h.flushReport = v.h.cursorReports()
	h.route.TotalX, h.route.TotalY, h.route.Packets = 0, 0, 0
	h.link.SetPointerLocked(true)
	width, _ := v.client.FramebufferSize()
	v.g.app.logger.Info("mouse helper: routing locked pointer to the helper", "mac_x", v.pointerX, "mac_y", v.pointerY,
		"view_w", v.viewport().W, "frame_w", width, "scale", h.scale(), "rfb_pointer_sends", v.pointerSends)
}

// endRouting drops routing by hand (the session is stopping) and does the
// same follow-up as a drop the rules decided.
func (h *helperUse) endRouting(linkAlive bool) {
	h.route.drop(linkAlive)
	h.afterDrop()
}

// afterDrop puts the absolute path back where the Mac's pointer is. The
// helper moved it without Screen Sharing knowing, so the position comes from
// the Mac's own report when one arrived after our last packet, else from the
// running total. Nothing is sent: the next move starts from here, so the
// pointer does not jump, and with no button held there is nothing to lift.
func (h *helperUse) afterDrop() {
	v := h.v
	h.cancelFlush()
	if h.link != nil {
		h.link.SetPointerLocked(false)
	}
	reports := v.h.cursorReports()
	rx, ry := v.h.remoteCursor()
	x, y := dropPosition(reports, h.flushReport, rx, ry, v.pointerX, v.pointerY)
	v.rel.set(x, y)
	v.pointerX, v.pointerY = x, y
	v.sent, v.sentReports = true, reports
	v.g.app.logger.Info("mouse helper: routing back to Screen Sharing",
		"mac_x", v.pointerX, "mac_y", v.pointerY, "sent_x16", h.route.TotalX, "sent_y16", h.route.TotalY,
		"packets", h.route.Packets, "held_ms", time.Since(h.engagedAt).Milliseconds(), "rfb_pointer_sends", v.pointerSends)
}

func (h *helperUse) cancelFlush() {
	if h.flushTimer != 0 {
		coreglib.SourceRemove(h.flushTimer)
		h.flushTimer = 0
	}
}

// motion takes a locked-pointer move in window units. The running position
// still follows it (clamped to the picture) so the pointer reappears in the
// right place on unlock, but the Mac gets the whole move, with no edge.
func (h *helperUse) motion(dx, dy float64) {
	v := h.v
	width, height := v.client.FramebufferSize()
	x, y := v.rel.move(float32(dx), float32(dy), v.viewport(), width, height)
	v.pointerX, v.pointerY = x, y
	px, py := motionPoints(dx, dy, v.viewport(), width, height, h.scale())
	h.route.motion(px, py)
	h.schedule()
}

// scale is the framebuffer pixels per Mac point of the first display, as the
// absolute path uses it.
func (h *helperUse) scale() float64 {
	layout := h.v.h.displayLayout()
	if layout != nil && len(layout.Displays) > 0 && layout.Displays[0].Scale > 0 {
		return layout.Displays[0].Scale
	}
	return 1
}

func (h *helperUse) scroll(px, py float64) {
	h.route.scroll(px, py)
	h.flush()
}

// button handles a press or release while routing to the helper. It reports
// whether the helper took it (or swallowed it).
func (h *helperUse) button(rfbBit uint8, down bool) bool {
	bit := helperButton(rfbBit)
	if bit == 0 {
		return false
	}
	if !down && h.route.swallowRelease(bit) {
		return true
	}
	if !h.routed() {
		return false
	}
	if !down && h.route.buttons&bit == 0 {
		return false
	}
	h.route.button(bit, down)
	h.flush()
	return true
}

// releaseButtons lifts every button the helper holds (focus lost).
func (h *helperUse) releaseButtons() {
	if !h.routed() || h.route.buttons == 0 {
		return
	}
	h.route.orphan |= h.route.buttons
	h.route.button(h.route.buttons, false)
	h.flush()
}

// schedule sends waiting motion now, or soon if the last packet was too recent.
func (h *helperUse) schedule() {
	since := time.Since(h.lastFlush)
	if since >= flushEvery {
		h.flush()
		return
	}
	if h.flushTimer == 0 {
		h.flushTimer = coreglib.TimeoutAdd(uint(max((flushEvery-since).Milliseconds(), 1)), func() bool {
			h.flushTimer = 0
			h.flush()
			return false
		})
	}
}

func (h *helperUse) flush() {
	if !h.routed() || !h.route.waiting() {
		return
	}
	h.lastFlush = time.Now()
	v := h.v
	h.flushReport = v.h.cursorReports()
	v.rel.sent(v.pointerX, v.pointerY, h.lastFlush)
	if err := h.route.flush(); err != nil {
		v.g.app.logger.Info("mouse helper: send failed, back to Screen Sharing", "error", err)
		h.status = machelper.LinkStatus{State: machelper.LinkDown, Reason: err}
		h.linked = true
		h.updateChip()
		h.reroute()
	}
}

// end stops the helper session with the Screen Sharing session.
func (h *helperUse) end() {
	h.stopLink()
}

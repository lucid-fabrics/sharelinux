package main

import (
	"errors"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/nekohasekai/open-screen-sharing/internal/machelper"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// Routing of the locked pointer between Screen Sharing and the Mac mouse helper.
//
// Screen Sharing carries absolute positions only. While the pointer is locked
// (Exclusive mode) and the helper is up and accepting input, motion, buttons
// and scroll go to the helper as deltas, and no absolute pointer event goes
// over Screen Sharing: the keyboard stays on it. The moment any condition
// fails, routing falls back to the absolute path from the last position known.

// helperChip is what the session header says about the helper.
type helperChip int

const (
	chipNotPaired helperChip = iota
	chipWaiting
	chipActive
	chipOffOnMac
	chipUnavailable
)

// chipLook is the chip's words. Class is the style class for the label.
type chipLook struct {
	Chip    helperChip
	Label   string
	Tooltip string
	Class   string
}

const pairHint = "Pair this computer with the Mac in Preferences, under Keyboard, Mouse helper."

// chipFor turns what is known into the chip. paired says whether this Mac has a
// pinned helper; link is meaningful only when linked is set.
func chipFor(paired, linked bool, link machelper.LinkStatus) chipLook {
	if !paired {
		return chipLook{chipNotPaired, "No mouse helper on this Mac",
			"The mouse helper gives games and 3D tools smooth, unlimited mouse movement. " + pairHint +
				" Until then the mouse works as usual.", "muted"}
	}
	if !linked {
		return chipLook{chipWaiting, "Helper waiting",
			"Connecting to the mouse helper on the Mac. The mouse works as usual meanwhile.", "muted"}
	}
	switch link.State {
	case machelper.LinkConnecting:
		return chipLook{chipWaiting, "Helper waiting",
			"Connecting to the mouse helper on the Mac. The mouse works as usual meanwhile.", "muted"}
	case machelper.LinkDown:
		return chipLook{chipUnavailable, "Helper unavailable", unreachableWords(link.Reason), "warning"}
	}
	switch link.Helper {
	case machelper.StateActive:
		return chipLook{chipActive, "Helper active",
			"The mouse helper on the Mac is moving the pointer, so games and 3D tools get smooth, unlimited movement while the pointer is locked.", "success"}
	case machelper.StateDisabled:
		return chipLook{chipOffOnMac, "Helper off on the Mac",
			"The mouse helper on the Mac is switched off. Turn on Allow raw mouse in its menu on the Mac to use it. The mouse works as usual meanwhile.", "warning"}
	}
	return chipLook{chipUnavailable, "Helper unavailable", helperStateWords(link.Helper), "warning"}
}

func unreachableWords(reason error) string {
	switch {
	case errors.Is(reason, machelper.ErrRefused):
		return "The mouse helper app is not running on the Mac. Start it there and this computer finds it again by itself. The mouse works as usual meanwhile."
	case errors.Is(reason, machelper.ErrSilent):
		return "The mouse helper on the Mac stopped answering, so the mouse works as usual. This computer keeps trying."
	}
	return "The mouse helper on the Mac does not answer. It may be closed, or this computer's clock differs from the Mac's by more than five minutes. The mouse works as usual meanwhile."
}

func helperStateWords(state machelper.HelperState) string {
	switch state {
	case machelper.StateLocked:
		return "The Mac's screen is locked, so the helper waits. The mouse works as usual meanwhile."
	case machelper.StateNoSession:
		return "The helper does not see this Screen Sharing session on the Mac. The mouse works as usual meanwhile."
	case machelper.StateNotConsole:
		return "Someone else is signed in at the Mac, so the helper waits. The mouse works as usual meanwhile."
	case machelper.StateRateLimited:
		return "The Mac is slowing the helper down for a moment. The mouse works as usual meanwhile."
	case machelper.StateNeedsGrant:
		return "The helper needs Accessibility access on the Mac. Allow it in System Settings there. The mouse works as usual meanwhile."
	}
	return "The mouse helper cannot be used right now. The mouse works as usual meanwhile."
}

// routeMode is where motion goes.
type routeMode int

const (
	routeRFB routeMode = iota
	routeHelper
)

// routeInputs are the facts the decision needs.
type routeInputs struct {
	// lockHeld: the pointer really is held with relative motion.
	lockHeld bool
	// enabled: the "use the mouse helper" setting.
	enabled bool
	// linkActive: the helper session is up and the Mac accepts input.
	linkActive bool
	// rfbButtons: buttons Screen Sharing believes are down.
	rfbButtons uint8
}

type routeChange int

const (
	routeStays routeChange = iota
	routeEngaged
	routeDropped
)

// helperRoute is the routing state machine plus the packets it builds. It does
// no I/O of its own: send is the helper link, and the owner calls decide
// whenever any input of it may have changed and flush when motion is waiting.
type helperRoute struct {
	mode routeMode
	send func(machelper.InputPacket) error

	// Motion and scroll wait here in the wire's units (1/16 point, pixels)
	// together with the part of a unit that did not fit yet.
	fracX, fracY   float64
	pendX, pendY   int
	fracSX, fracSY float64
	pendSX, pendSY int

	buttons uint8 // helper bits down
	dirty   bool  // a button changed since the last packet
	// orphan holds buttons that were down when routing ended. Their physical
	// release arrives later and must not reach Screen Sharing, which never saw
	// the press.
	orphan uint8

	// Totals of what was handed to send, in the wire's units, for the 1 % check.
	TotalX, TotalY int64
	Packets        uint64
}

// decide applies the rules and reports a change. Engaging waits for every
// Screen Sharing button to be up, so a button never has its press on one
// channel and its release on the other. Dropping is immediate and sends what
// was waiting plus a packet with no buttons, so the Mac lifts them at once.
func (r *helperRoute) decide(in routeInputs) routeChange {
	usable := in.lockHeld && in.enabled && in.linkActive
	switch r.mode {
	case routeRFB:
		if usable && in.rfbButtons == 0 {
			r.reset()
			r.mode = routeHelper
			return routeEngaged
		}
	case routeHelper:
		if !usable {
			r.drop()
			return routeDropped
		}
	}
	return routeStays
}

// announce sends one packet with every button up when routing starts. The Mac
// treats the buttons of a new session as up until it has seen such a packet
// (so a held mask can never become a fresh press), and this is that packet: a
// click right after engaging is then a real click, not a swallowed one.
func (r *helperRoute) announce() error {
	r.dirty = true
	return r.flush()
}

func (r *helperRoute) reset() {
	r.fracX, r.fracY, r.pendX, r.pendY = 0, 0, 0, 0
	r.fracSX, r.fracSY, r.pendSX, r.pendSY = 0, 0, 0, 0
	r.buttons, r.dirty = 0, false
}

// drop ends helper routing. The Mac is told the buttons are up whatever the
// link looks like: a link that has only gone quiet may still hear this packet,
// and one that is really gone refuses the send at once (the error is ignored,
// the Mac lifts the buttons itself after a second without input). Waiting for
// the link to look healthy first left a button held on a quiet link.
func (r *helperRoute) drop() {
	r.mode = routeRFB
	r.orphan |= r.buttons
	if r.buttons != 0 || r.pendX != 0 || r.pendY != 0 || r.pendSX != 0 || r.pendSY != 0 {
		held := r.buttons != 0
		r.buttons = 0
		r.dirty = true
		_ = r.flush()
		if held {
			// A button is a full bitmask in every packet, so repeating the
			// "all up" costs nothing and survives one lost datagram.
			r.dirty = true
			_ = r.flush()
		}
	}
	r.reset()
}

// pressed notes a button going down. A release that was waiting to be swallowed
// belongs to an older press: the person pressed again, so that one is over and
// its release was lost (the window never heard it, in another mode or after
// focus left). Keeping the mark would swallow the new press's release.
func (r *helperRoute) pressed(bit uint8) {
	r.orphan &^= bit
}

// buttonEvent handles a press or release from the window and reports whether the
// route took it (sent it to the helper, or swallowed the late release of a
// button the helper held when routing ended). False: Screen Sharing gets it.
func (r *helperRoute) buttonEvent(bit uint8, down bool) bool {
	if down {
		r.pressed(bit)
	}
	if !down && r.swallowRelease(bit) {
		return true
	}
	if r.mode != routeHelper {
		return false
	}
	if !down && r.buttons&bit == 0 {
		return false
	}
	r.button(bit, down)
	return true
}

// lift ends every button the helper holds without waiting for the person (focus
// lost, mode left). The physical release may never arrive, or arrive later, so
// the mark is kept for it. It reports whether a packet is due.
func (r *helperRoute) lift() bool {
	if r.buttons == 0 {
		return false
	}
	r.orphan |= r.buttons
	r.button(r.buttons, false)
	return true
}

// swallowRelease reports whether the release of a button is the end of one the
// helper held when routing ended, and forgets it.
func (r *helperRoute) swallowRelease(bit uint8) bool {
	if r.orphan&bit == 0 {
		return false
	}
	r.orphan &^= bit
	return true
}

// motion adds a move in Mac points.
func (r *helperRoute) motion(dx, dy float64) {
	if !finiteMotion(dx, dy) {
		return
	}
	r.fracX += dx * 16
	r.fracY += dy * 16
	whole := math.Trunc(r.fracX)
	r.pendX, r.fracX = r.pendX+int(whole), r.fracX-whole
	whole = math.Trunc(r.fracY)
	r.pendY, r.fracY = r.pendY+int(whole), r.fracY-whole
}

// scroll adds a scroll in the Mac's pixel units (positive y up, positive x left).
func (r *helperRoute) scroll(dx, dy float64) {
	if !finiteMotion(dx, dy) {
		return
	}
	r.fracSX += dx
	r.fracSY += dy
	whole := math.Trunc(r.fracSX)
	r.pendSX, r.fracSX = r.pendSX+int(whole), r.fracSX-whole
	whole = math.Trunc(r.fracSY)
	r.pendSY, r.fracSY = r.pendSY+int(whole), r.fracSY-whole
}

// button records a button going down or up.
func (r *helperRoute) button(bit uint8, down bool) {
	if down {
		r.buttons |= bit
	} else {
		r.buttons &^= bit
	}
	r.dirty = true
}

// waiting reports whether a packet is due.
func (r *helperRoute) waiting() bool {
	return r.dirty || r.pendX != 0 || r.pendY != 0 || r.pendSX != 0 || r.pendSY != 0
}

// flush sends everything waiting. A packet carries at most the wire's clamp,
// so a long wait becomes several packets; the buttons ride on the first and
// stay in the rest. An error from send is returned and the rest is dropped:
// the helper is gone, and the owner falls back.
func (r *helperRoute) flush() error {
	for r.waiting() {
		p := machelper.InputPacket{Buttons: r.buttons}
		p.DX, r.pendX = takeClamped(r.pendX, machelper.MaxMotion)
		p.DY, r.pendY = takeClamped(r.pendY, machelper.MaxMotion)
		p.SX, r.pendSX = takeClamped(r.pendSX, machelper.MaxScroll)
		p.SY, r.pendSY = takeClamped(r.pendSY, machelper.MaxScroll)
		r.dirty = false
		if err := r.send(p); err != nil {
			r.pendX, r.pendY, r.pendSX, r.pendSY = 0, 0, 0, 0
			return err
		}
		r.TotalX += int64(p.DX)
		r.TotalY += int64(p.DY)
		r.Packets++
	}
	return nil
}

func takeClamped(pending int, limit int16) (int16, int) {
	n := min(max(pending, -int(limit)), int(limit))
	return int16(n), pending - n
}

// motionPoints converts a motion of the locked pointer, in window units, to
// Mac points: window units to framebuffer pixels as the absolute path does,
// then to points by the display's scale (framebuffer pixels per point).
func motionPoints(dx, dy float64, v viewport, frameWidth, frameHeight int, scale float64) (float64, float64) {
	if v.empty() || frameWidth <= 0 || frameHeight <= 0 || !finiteMotion(dx, dy) {
		return 0, 0
	}
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 1
	}
	return dx * float64(frameWidth) / float64(v.W) / scale, dy * float64(frameHeight) / float64(v.H) / scale
}

// logRouting records a routing change. The line at the default level says only that it
// happened: where the pointer is, how far it moved and for how long say what the person is
// doing, and stay in the debug detail.
func logRouting(logger *slog.Logger, what string, detail ...any) {
	logger.Info("mouse helper: " + what)
	logger.Debug("mouse helper: "+what+" (detail)", detail...)
}

// helperButton maps a Screen Sharing button bit to the helper's bit.
func helperButton(rfbBit uint8) uint8 {
	switch rfbBit {
	case rfb.ButtonLeft:
		return machelper.ButtonLeft
	case rfb.ButtonRight:
		return machelper.ButtonRight
	case rfb.ButtonMiddle:
		return machelper.ButtonMiddle
	}
	return 0
}

// addressHost strips the port and the brackets of an address.
func addressHost(address string) string {
	address = strings.TrimSpace(address)
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	return strings.Trim(address, "[]")
}

// normalIP returns the address in one spelling (no brackets, no zone), or "" when
// the text is not an IP address.
func normalIP(text string) string {
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(text), "[]"))
	if err != nil {
		return ""
	}
	return addr.WithZone("").Unmap().String()
}

// macIsAt reports whether a paired Mac's helper address is the machine at ip: the same address, or
// a name that resolves to it.
func macIsAt(pairedHost, ip string, lookup func(string) []string) bool {
	if pairedHost == "" || ip == "" {
		return false
	}
	if own := normalIP(pairedHost); own != "" {
		return own == ip
	}
	if lookup == nil {
		return false
	}
	for _, found := range lookup(strings.ToLower(pairedHost)) {
		if normalIP(found) == ip {
			return true
		}
	}
	return false
}

// helperTarget picks the paired Mac that is the one this session is connected
// to, and the address to send to: the exact IP address of the live Screen
// Sharing connection (never a name that would be looked up again, and might
// answer differently the second time) with the helper port the pairing used.
// Nothing is ever sent to any other address.
func helperTarget(peer string, macs []pairedMac, lookup func(string) []string) (pairedMac, string, bool) {
	ip := normalIP(peer)
	if ip == "" {
		return pairedMac{}, "", false
	}
	for _, mac := range macs {
		if !macIsAt(addressHost(mac.Address), ip, lookup) {
			continue
		}
		port := machelper.DefaultPort
		if _, p, err := net.SplitHostPort(mac.Address); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
				port = n
			}
		}
		return mac, net.JoinHostPort(addressHost(peer), strconv.Itoa(port)), true
	}
	return pairedMac{}, "", false
}

// dropPosition is where the Mac's pointer is believed to be when helper
// routing ends. The helper moved it without Screen Sharing carrying the
// moves, so the Mac's own cursor report counts when one arrived after our last
// packet (reports now beyond the count at that packet); otherwise the running
// total, which followed every delta, is the best knowledge.
func dropPosition(reports, reportsAtLastPacket uint64, reportedX, reportedY, totalX, totalY int) (int, int) {
	if reports > reportsAtLastPacket {
		return reportedX, reportedY
	}
	return totalX, totalY
}

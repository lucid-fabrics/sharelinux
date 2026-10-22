package main

import (
	"errors"
	"math"
	"net"
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
		return chipLook{chipNotPaired, "Helper not paired",
			"The mouse helper gives games and 3D tools smooth, unlimited mouse movement. " + pairHint +
				" Until then the mouse works as usual.", "dim-label"}
	}
	if !linked {
		return chipLook{chipWaiting, "Helper waiting",
			"Connecting to the mouse helper on the Mac. The mouse works as usual meanwhile.", "dim-label"}
	}
	switch link.State {
	case machelper.LinkConnecting:
		return chipLook{chipWaiting, "Helper waiting",
			"Connecting to the mouse helper on the Mac. The mouse works as usual meanwhile.", "dim-label"}
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
			r.drop(in.linkActive)
			return routeDropped
		}
	}
	return routeStays
}

func (r *helperRoute) reset() {
	r.fracX, r.fracY, r.pendX, r.pendY = 0, 0, 0, 0
	r.fracSX, r.fracSY, r.pendSX, r.pendSY = 0, 0, 0, 0
	r.buttons, r.dirty = 0, false
}

// drop ends helper routing. When the link may still be alive the Mac is told
// the buttons are up; if it is not, it lifts them itself (silence, refusal).
func (r *helperRoute) drop(linkAlive bool) {
	r.mode = routeRFB
	r.orphan |= r.buttons
	if linkAlive && (r.buttons != 0 || r.pendX != 0 || r.pendY != 0 || r.pendSX != 0 || r.pendSY != 0) {
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

// sameMac reports whether the paired Mac's helper address and the address this
// session is connected to are one machine: the same name or address, or names
// that resolve to a shared address.
func sameMac(sessionHost, pairedAddress string, lookup func(string) []string) bool {
	a, b := strings.ToLower(addressHost(sessionHost)), strings.ToLower(addressHost(pairedAddress))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if lookup == nil {
		return false
	}
	addresses := map[string]bool{}
	for _, x := range append(lookup(a), a) {
		if ip := net.ParseIP(x); ip != nil {
			addresses[ip.String()] = true
		}
	}
	for _, x := range append(lookup(b), b) {
		if ip := net.ParseIP(x); ip != nil && addresses[ip.String()] {
			return true
		}
	}
	return false
}

// helperTarget picks the paired Mac that is the one this session is connected
// to, and the address to send to: the session's own host with the helper port
// the pairing used. Nothing is ever sent to any other address.
func helperTarget(sessionHost string, macs []pairedMac, lookup func(string) []string) (pairedMac, string, bool) {
	for _, mac := range macs {
		if !sameMac(sessionHost, mac.Address, lookup) {
			continue
		}
		port := machelper.DefaultPort
		if _, p, err := net.SplitHostPort(mac.Address); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
				port = n
			}
		}
		return mac, net.JoinHostPort(addressHost(sessionHost), strconv.Itoa(port)), true
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

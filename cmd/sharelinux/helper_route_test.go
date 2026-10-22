package main

import (
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/machelper"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

type packetLog struct {
	packets []machelper.InputPacket
	err     error
}

func (l *packetLog) send(p machelper.InputPacket) error {
	if l.err != nil {
		return l.err
	}
	l.packets = append(l.packets, p)
	return nil
}

func newRoute() (*helperRoute, *packetLog) {
	log := &packetLog{}
	return &helperRoute{send: log.send}, log
}

var allGood = routeInputs{lockHeld: true, enabled: true, linkActive: true}

func engaged(t *testing.T) (*helperRoute, *packetLog) {
	t.Helper()
	r, log := newRoute()
	if r.decide(allGood) != routeEngaged {
		t.Fatal("did not engage with every condition met")
	}
	return r, log
}

func TestRouteEngagesOnlyWhenEverythingHolds(t *testing.T) {
	for name, in := range map[string]routeInputs{
		"lock not held":  {enabled: true, linkActive: true},
		"setting off":    {lockHeld: true, linkActive: true},
		"helper not up":  {lockHeld: true, enabled: true},
		"button held":    {lockHeld: true, enabled: true, linkActive: true, rfbButtons: rfb.ButtonLeft},
		"nothing at all": {},
	} {
		r, _ := newRoute()
		if got := r.decide(in); got != routeStays || r.mode != routeRFB {
			t.Errorf("%s: change %v mode %v, want to stay on Screen Sharing", name, got, r.mode)
		}
	}
	r, _ := newRoute()
	if r.decide(allGood) != routeEngaged || r.mode != routeHelper {
		t.Fatal("no engage")
	}
	if r.decide(allGood) != routeStays {
		t.Fatal("engaging twice")
	}
}

func TestRouteEngageWaitsForTheLastScreenSharingButton(t *testing.T) {
	r, _ := newRoute()
	in := allGood
	in.rfbButtons = rfb.ButtonRight
	if r.decide(in) != routeStays {
		t.Fatal("engaged with a button down on the other channel")
	}
	in.rfbButtons = 0
	if r.decide(in) != routeEngaged {
		t.Fatal("did not engage once the button came up")
	}
}

func TestRouteDropsOnEachLostCondition(t *testing.T) {
	for name, lose := range map[string]func(*routeInputs){
		"lock released":      func(in *routeInputs) { in.lockHeld = false },
		"setting turned off": func(in *routeInputs) { in.enabled = false },
		"helper gone":        func(in *routeInputs) { in.linkActive = false },
	} {
		r, _ := engaged(t)
		in := allGood
		lose(&in)
		if got := r.decide(in); got != routeDropped || r.mode != routeRFB {
			t.Errorf("%s: change %v mode %v", name, got, r.mode)
		}
	}
}

func TestRouteDropWhileAButtonIsHeldLiftsItOnTheMacAndSwallowsTheRelease(t *testing.T) {
	r, log := engaged(t)
	r.button(machelper.ButtonLeft, true)
	r.motion(5, 0)
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	log.packets = nil
	r.motion(2, 1) // waiting when the lock is released
	in := allGood
	in.lockHeld = false
	if r.decide(in) != routeDropped {
		t.Fatal("no drop")
	}
	if len(log.packets) != 1 {
		t.Fatalf("%d packets on drop, want the one that lifts the buttons", len(log.packets))
	}
	last := log.packets[0]
	if last.Buttons != 0 || last.DX != 32 || last.DY != 16 {
		t.Fatalf("drop packet %+v: buttons must be up and the waiting motion must not be lost", last)
	}
	if !r.swallowRelease(machelper.ButtonLeft) {
		t.Fatal("the physical release was not swallowed")
	}
	if r.swallowRelease(machelper.ButtonLeft) {
		t.Fatal("swallowed twice")
	}
	if r.swallowRelease(machelper.ButtonRight) {
		t.Fatal("swallowed a button that was never held")
	}
}

func TestRouteDropWithADeadLinkSendsNothing(t *testing.T) {
	r, log := engaged(t)
	r.button(machelper.ButtonLeft, true)
	r.motion(3, 3)
	in := allGood
	in.linkActive = false
	r.decide(in)
	if len(log.packets) != 0 {
		t.Fatalf("sent %d packets to a helper that is gone", len(log.packets))
	}
	if !r.swallowRelease(machelper.ButtonLeft) {
		t.Fatal("release of a button the dead helper held must still be swallowed")
	}
	if r.waiting() {
		t.Fatal("stale motion kept after the drop")
	}
}

func TestRouteDropWithNothingHeldSendsNothing(t *testing.T) {
	r, log := engaged(t)
	in := allGood
	in.lockHeld = false
	r.decide(in)
	if len(log.packets) != 0 {
		t.Fatalf("sent %d packets for an idle drop", len(log.packets))
	}
}

func TestRouteReengagesCleanAfterADrop(t *testing.T) {
	r, log := engaged(t)
	r.motion(0.5, 0.5)
	in := allGood
	in.linkActive = false
	r.decide(in)
	if r.decide(allGood) != routeEngaged {
		t.Fatal("no re-engage")
	}
	if err := r.flush(); err != nil || len(log.packets) != 0 {
		t.Fatalf("old motion leaked into the new stretch: %v %v", err, log.packets)
	}
}

func TestRouteMotionKeepsSubUnitRemainders(t *testing.T) {
	r, log := engaged(t)
	// 1/32 point is half a wire unit: two of them make one.
	for range 10 {
		r.motion(1.0/32, -1.0/32)
		if err := r.flush(); err != nil {
			t.Fatal(err)
		}
	}
	var sx, sy int
	for _, p := range log.packets {
		sx += int(p.DX)
		sy += int(p.DY)
	}
	if sx != 5 || sy != -5 {
		t.Fatalf("sent %d,%d wire units for 10 half units, want 5,-5", sx, sy)
	}
}

func TestRouteTotalsStayWithinOneUnitOfTheTruth(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	r, _ := engaged(t)
	var wantX, wantY float64
	for range 5000 {
		dx, dy := rng.NormFloat64()*3, rng.NormFloat64()*3
		wantX += dx
		wantY += dy
		r.motion(dx, dy)
		if rng.Intn(3) == 0 {
			if err := r.flush(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(r.TotalX)-wantX*16) > 1 || math.Abs(float64(r.TotalY)-wantY*16) > 1 {
		t.Fatalf("sent %d,%d units, want %.1f,%.1f", r.TotalX, r.TotalY, wantX*16, wantY*16)
	}
}

func TestRouteSplitsHugeMotionAndLosesNothing(t *testing.T) {
	r, log := engaged(t)
	r.motion(10000, -7000)
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	if len(log.packets) < 5 {
		t.Fatalf("%d packets for 10000 points, want it split", len(log.packets))
	}
	for _, p := range log.packets {
		if p.DX > machelper.MaxMotion || p.DX < -machelper.MaxMotion || p.DY > machelper.MaxMotion || p.DY < -machelper.MaxMotion {
			t.Fatalf("packet over the clamp: %+v", p)
		}
	}
	if r.TotalX != 160000 || r.TotalY != -112000 {
		t.Fatalf("totals %d,%d", r.TotalX, r.TotalY)
	}
}

func TestRouteButtonsRideOnEveryPacketOfAFlush(t *testing.T) {
	r, log := engaged(t)
	r.button(machelper.ButtonLeft, true)
	r.motion(5000, 0)
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	for _, p := range log.packets {
		if p.Buttons != machelper.ButtonLeft {
			t.Fatalf("packet without the held button: %+v", p)
		}
	}
}

func TestRouteButtonAloneMakesAPacketAndIdleMakesNone(t *testing.T) {
	r, log := engaged(t)
	if err := r.flush(); err != nil || len(log.packets) != 0 {
		t.Fatalf("idle flush sent %v", log.packets)
	}
	r.button(machelper.ButtonRight, true)
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	r.button(machelper.ButtonRight, false)
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	if len(log.packets) != 2 || log.packets[0].Buttons != machelper.ButtonRight || log.packets[1].Buttons != 0 {
		t.Fatalf("packets %+v", log.packets)
	}
	if err := r.flush(); err != nil || len(log.packets) != 2 {
		t.Fatal("a second flush repeated a packet")
	}
}

func TestRouteScrollCarriesRemainderAndClamps(t *testing.T) {
	r, log := engaged(t)
	r.scroll(0, 0.4)
	r.scroll(0, 0.4)
	r.scroll(0, 0.4)
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	if len(log.packets) != 1 || log.packets[0].SY != 1 {
		t.Fatalf("packets %+v, want one unit of scroll from 1.2", log.packets)
	}
	log.packets = nil
	r.scroll(-5000, 0)
	if err := r.flush(); err != nil {
		t.Fatal(err)
	}
	var total int
	for _, p := range log.packets {
		if p.SX < -machelper.MaxScroll {
			t.Fatalf("scroll over the clamp: %+v", p)
		}
		total += int(p.SX)
	}
	if total != -5000 {
		t.Fatalf("scroll total %d", total)
	}
}

func TestRouteSendErrorIsReturnedAndNothingStaysQueued(t *testing.T) {
	r, log := engaged(t)
	log.err = machelper.ErrRefused
	r.motion(10, 10)
	if err := r.flush(); !errors.Is(err, machelper.ErrRefused) {
		t.Fatalf("flush: %v", err)
	}
	if r.waiting() && r.pendX != 0 {
		t.Fatal("motion kept for a helper that is gone")
	}
}

func TestRouteIgnoresNonFiniteInput(t *testing.T) {
	r, log := engaged(t)
	r.motion(math.NaN(), 1)
	r.motion(math.Inf(1), 1)
	r.scroll(math.NaN(), 1)
	if err := r.flush(); err != nil || len(log.packets) != 0 {
		t.Fatalf("non-finite input produced %v", log.packets)
	}
}

func TestChipForEveryState(t *testing.T) {
	live := func(s machelper.HelperState) machelper.LinkStatus {
		return machelper.LinkStatus{State: machelper.LinkLive, Helper: s}
	}
	cases := []struct {
		name           string
		paired, linked bool
		link           machelper.LinkStatus
		want           helperChip
		label          string
	}{
		{"not paired", false, false, machelper.LinkStatus{}, chipNotPaired, "Helper not paired"},
		{"paired, link not started", true, false, machelper.LinkStatus{}, chipWaiting, "Helper waiting"},
		{"connecting", true, true, machelper.LinkStatus{State: machelper.LinkConnecting}, chipWaiting, "Helper waiting"},
		{"active", true, true, live(machelper.StateActive), chipActive, "Helper active"},
		{"disabled on the Mac", true, true, live(machelper.StateDisabled), chipOffOnMac, "Helper off on the Mac"},
		{"locked", true, true, live(machelper.StateLocked), chipUnavailable, "Helper unavailable"},
		{"no session", true, true, live(machelper.StateNoSession), chipUnavailable, "Helper unavailable"},
		{"not console", true, true, live(machelper.StateNotConsole), chipUnavailable, "Helper unavailable"},
		{"rate limited", true, true, live(machelper.StateRateLimited), chipUnavailable, "Helper unavailable"},
		{"needs grant", true, true, live(machelper.StateNeedsGrant), chipUnavailable, "Helper unavailable"},
		{"refused", true, true, machelper.LinkStatus{State: machelper.LinkDown, Reason: machelper.ErrRefused}, chipUnavailable, "Helper unavailable"},
		{"silent", true, true, machelper.LinkStatus{State: machelper.LinkDown, Reason: machelper.ErrSilent}, chipUnavailable, "Helper unavailable"},
		{"no answer", true, true, machelper.LinkStatus{State: machelper.LinkDown, Reason: machelper.ErrNoAnswer}, chipUnavailable, "Helper unavailable"},
	}
	tooltips := map[string]bool{}
	for _, c := range cases {
		look := chipFor(c.paired, c.linked, c.link)
		if look.Chip != c.want || look.Label != c.label {
			t.Errorf("%s: got %v %q, want %v %q", c.name, look.Chip, look.Label, c.want, c.label)
		}
		if look.Tooltip == "" || look.Class == "" {
			t.Errorf("%s: empty tooltip or class", c.name)
		}
		if strings.ContainsRune(look.Tooltip+look.Label, '—') {
			t.Errorf("%s: em dash in the copy", c.name)
		}
		tooltips[c.name] = true
	}
	// Each unavailable reason has words of its own, so the tooltip can tell what to do.
	seen := map[string]string{}
	for _, c := range cases {
		if c.want != chipUnavailable {
			continue
		}
		tip := chipFor(c.paired, c.linked, c.link).Tooltip
		if other, dup := seen[tip]; dup && c.name != "no answer" && other != "no answer" {
			t.Errorf("%s and %s share a tooltip", c.name, other)
		}
		seen[tip] = c.name
	}
	if !strings.Contains(chipFor(false, false, machelper.LinkStatus{}).Tooltip, "Preferences") {
		t.Error("the not paired tooltip does not say where to pair")
	}
}

func TestMotionPointsScalesLikeTheAbsolutePath(t *testing.T) {
	view := viewport{X: 0, Y: 0, W: 1000, H: 500}
	// A 2000x1000 picture in a 1000x500 view: one window unit is two framebuffer pixels.
	x, y := motionPoints(10, -5, view, 2000, 1000, 1)
	if x != 20 || y != -10 {
		t.Fatalf("scale 1: %v,%v", x, y)
	}
	// On a Retina Mac the framebuffer has two pixels per point.
	x, y = motionPoints(10, -5, view, 2000, 1000, 2)
	if x != 10 || y != -5 {
		t.Fatalf("scale 2: %v,%v", x, y)
	}
	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		x, _ = motionPoints(10, 0, view, 2000, 1000, bad)
		if x != 20 {
			t.Fatalf("scale %v not treated as 1: %v", bad, x)
		}
	}
	if x, y = motionPoints(10, 10, viewport{}, 2000, 1000, 1); x != 0 || y != 0 {
		t.Fatal("empty viewport moved the pointer")
	}
	if x, y = motionPoints(math.NaN(), 10, view, 2000, 1000, 1); x != 0 || y != 0 {
		t.Fatal("NaN moved the pointer")
	}
}

func TestHelperButtonMapping(t *testing.T) {
	if helperButton(rfb.ButtonLeft) != machelper.ButtonLeft ||
		helperButton(rfb.ButtonRight) != machelper.ButtonRight ||
		helperButton(rfb.ButtonMiddle) != machelper.ButtonMiddle ||
		helperButton(0) != 0 || helperButton(0x80) != 0 {
		t.Fatal("button mapping")
	}
}

func TestHelperTargetIsOnlyTheMacTheSessionIsConnectedTo(t *testing.T) {
	macs := []pairedMac{
		{Name: "Studio", Address: "192.168.1.218", Key: strings.Repeat("a", 64)},
		{Name: "Other", Address: "10.0.0.9:6000", Key: strings.Repeat("b", 64)},
	}
	mac, addr, ok := helperTarget("192.168.1.218:5900", macs, nil)
	if !ok || mac.Name != "Studio" || addr != "192.168.1.218:5961" {
		t.Fatalf("%v %v %v", mac, addr, ok)
	}
	mac, addr, ok = helperTarget("10.0.0.9", macs, nil)
	if !ok || mac.Name != "Other" || addr != "10.0.0.9:6000" {
		t.Fatalf("custom port: %v %v %v", mac, addr, ok)
	}
	if _, _, ok = helperTarget("192.168.1.77", macs, nil); ok {
		t.Fatal("matched a Mac the session is not connected to")
	}
	if _, _, ok = helperTarget("", macs, nil); ok {
		t.Fatal("matched an empty address")
	}
	if _, _, ok = helperTarget("192.168.1.218", nil, nil); ok {
		t.Fatal("matched with nobody paired")
	}
}

func TestHelperTargetUsesTheSessionHostNotTheStoredOne(t *testing.T) {
	// Paired by name, connected by address: the datagrams go to the address
	// the Screen Sharing connection uses, so they cannot reach another machine.
	lookup := func(name string) []string {
		if name == "studio.local" {
			return []string{"192.168.1.218"}
		}
		return nil
	}
	macs := []pairedMac{{Name: "Studio", Address: "Studio.local", Key: strings.Repeat("a", 64)}}
	_, addr, ok := helperTarget("192.168.1.218", macs, lookup)
	if !ok || addr != "192.168.1.218:5961" {
		t.Fatalf("%v %v", addr, ok)
	}
	if _, _, ok = helperTarget("192.168.1.219", macs, lookup); ok {
		t.Fatal("matched another address")
	}
	if _, _, ok = helperTarget("192.168.1.218", macs, nil); ok {
		t.Fatal("matched a name without a lookup")
	}
}

func TestSameMacSpellings(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"[fe80::1]:5900", "fe80::1", true},
		{"MAC.local:5900", "mac.local", true},
		{"mac.local", "mac2.local", false},
		{"", "mac.local", false},
	} {
		if got := sameMac(c.a, c.b, nil); got != c.want {
			t.Errorf("sameMac(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

import Foundation
import HelperCore

public enum ReleaseReason: String, Sendable {
    case denied, ended, killSwitch, sleep, locked, environment, timeout, toggleOff
}

/// Turns accepted input packets into poster calls, and makes sure no button is
/// ever left down.
///
/// The gate has already accepted what reaches `apply`; this type never decides
/// whether input is allowed. Its own job:
///  * carry the sub-pixel remainder (deltas travel as points * 16),
///  * keep the button state and post presses/releases from the full bitmask,
///  * compute the click count,
///  * release every held button on any deny, session end, sleep, lock, kill
///    switch, toggle off, or when the channel goes quiet for `silenceTimeout`.
///
/// Order inside one packet: motion first (under the button state before the
/// packet), then button changes, then scroll. A press that arrived with motion
/// therefore lands at the new position, and a release ends a drag after its
/// last move.
///
/// Not thread safe: the runtime calls it from its own serial queue.
public final class MouseController {
    private let poster: EventPoster
    private let clock: () -> Double
    private let doubleClickInterval: () -> Double
    private var remX = 0, remY = 0  // sub-pixel remainder, in 1/16 point
    public private(set) var held: ButtonMask = []
    public private(set) var injecting = false
    /// Any accepted packet, keepalives included: how long the session has been alive.
    private var lastActivity = -Double.infinity
    /// The last accepted INPUT packet. A held button lives on this alone: a keepalive says the
    /// link is up, not that the person still holds the button, and a lost "all up" must not be
    /// kept alive by the keepalives that keep flowing on a link that dropped its input.
    private var lastInput = -Double.infinity
    private var lastDown: (button: MouseButton, at: Double, count: Int)?
    private var movedSinceDown = 0
    /// "Buttons must be seen up first". Set by every release (deny, kill, gate flip, end, sleep,
    /// timeout) and by a session start. While set, a packet with buttons down is applied with the
    /// buttons up: the Linux side may still be holding a button the Mac already let go of (its
    /// last "deny" status can be lost or rate limited), and that must not become a new press. The
    /// first packet with every button up clears it, and a press after that is a real one.
    public private(set) var needsUpFirst = false
    /// Called when `injecting` changes (menu bar icon).
    public var onInjectingChange: ((Bool) -> Void)?
    /// Releases that actually posted something, for logs and tests.
    public private(set) var releaseCount = 0

    /// Farther than this since the last press (points) and it is no double click.
    static let clickSlop = 4

    public init(
        poster: EventPoster, clock: @escaping () -> Double,
        doubleClickInterval: @escaping () -> Double = { 0.5 }
    ) {
        self.poster = poster; self.clock = clock; self.doubleClickInterval = doubleClickInterval
    }

    /// Posts one accepted input packet (already clamped by the channel).
    public func apply(_ p: InputPacket) {
        let now = clock()
        lastActivity = now
        lastInput = now
        setInjecting(true)

        // Motion, carrying the remainder.
        remX += Int(p.dx); remY += Int(p.dy)
        let wx = remX / 16, wy = remY / 16  // truncates toward zero
        remX -= wx * 16; remY -= wy * 16
        if wx != 0 || wy != 0 {
            poster.postMove(dx: wx, dy: wy, held: held)
            movedSinceDown += abs(wx) + abs(wy)
        }

        // Buttons from the full mask: releases first so a swapped button never overlaps.
        var want = p.buttons
        if needsUpFirst {
            if want.isEmpty { needsUpFirst = false } else { want = [] }
        }
        for b in MouseButton.allCases where held.contains(b.mask) && !want.contains(b.mask) {
            held.remove(b.mask)
            poster.postButton(b, down: false, clickState: lastDown?.button == b ? (lastDown?.count ?? 1) : 1)
        }
        for b in MouseButton.allCases where !held.contains(b.mask) && want.contains(b.mask) {
            held.insert(b.mask)
            let count = nextClickCount(for: b, now: now)
            lastDown = (b, now, count)
            movedSinceDown = 0
            poster.postButton(b, down: true, clickState: count)
        }

        if p.sx != 0 || p.sy != 0 { poster.postScroll(dx: Int(p.sx), dy: Int(p.sy)) }
    }

    private func nextClickCount(for b: MouseButton, now: Double) -> Int {
        guard let last = lastDown, last.button == b, now >= last.at,
            now - last.at <= doubleClickInterval(), movedSinceDown <= MouseController.clickSlop
        else { return 1 }
        return last.count + 1
    }

    /// A keepalive was accepted: the channel is alive. It keeps the session (and the
    /// indicator) going but never a held button: only input packets do that.
    public func touch() {
        lastActivity = clock()
    }

    /// A new session starts: its first press must follow a packet with every button up.
    public func requireUpFirst() { needsUpFirst = true }

    /// Releases every held button and forgets the motion remainder. Safe to
    /// call at any time, any number of times.
    public func releaseAll(_ reason: ReleaseReason) {
        let wasHeld = held, last = lastDown
        held = []
        needsUpFirst = true
        remX = 0; remY = 0
        lastDown = nil
        for b in MouseButton.allCases where wasHeld.contains(b.mask) {
            poster.postButton(b, down: false, clickState: last?.button == b ? last!.count : 1)
            releaseCount += 1
        }
        setInjecting(false)
    }

    /// Watchdog, call at least every 100 ms: no accepted traffic for
    /// `ChannelLimits.silenceTimeout` means the buttons come up, and so does no accepted
    /// INPUT for that long while a button is held (keepalives do not count for buttons).
    public func tick() {
        let now = clock()
        guard injecting || !held.isEmpty else { return }
        if now < lastActivity || now - lastActivity >= ChannelLimits.silenceTimeout {
            releaseAll(.timeout)
        } else if !held.isEmpty, now < lastInput || now - lastInput >= ChannelLimits.silenceTimeout {
            releaseAll(.timeout)
        }
    }

    private func setInjecting(_ on: Bool) {
        guard injecting != on else { return }
        injecting = on
        onInjectingChange?(on)
    }
}

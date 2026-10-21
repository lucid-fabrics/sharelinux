import CoreGraphics
import Foundation
import HelperCore

/// Posts the helper's mouse events as CGEvents on the HID tap.
///
///  * Source: `hidSystemState`, `localEventsSuppressionInterval = 0` so a person
///    at the Mac is never locked out of the mouse after one of our events, and a
///    fixed `eventSourceUserData` so the events are recognisable in logs and probes.
///  * Motion: location is the current cursor position plus the delta, clamped to
///    the union of the displays; `mouseEventDeltaX/Y` carry the delta itself, so
///    an app that disconnected the cursor (games, 3D navigation) still sees it.
///  * Tap: `cghidEventTap` (design 2.2/2.3): every session-level tap and app sees
///    the event as hardware input, the same place screensharingd's events enter.
///  * No keyboard events exist in this type, and no other event is ever posted.
///
/// Needs the Accessibility (PostEvent) grant; without it macOS drops the
/// events. Never instantiated by tests.
public final class CGEventPoster: EventPoster {
    /// "OSS" in ASCII, stored in kCGEventSourceUserData.
    public static let userData: Int64 = 0x4F5353
    private let source: CGEventSource?

    public init() {
        source = CGEventSource(stateID: .hidSystemState)
        source?.userData = CGEventPoster.userData
        source?.localEventsSuppressionInterval = 0
    }

    private func currentLocation() -> CGPoint {
        CGEvent(source: nil)?.location ?? .zero
    }

    private func displayBounds() -> [PostingGeometry.Rect] {
        var count: UInt32 = 0
        var ids = [CGDirectDisplayID](repeating: 0, count: 16)
        guard CGGetActiveDisplayList(UInt32(ids.count), &ids, &count) == .success else { return [] }
        return ids.prefix(Int(count)).map {
            let r = CGDisplayBounds($0)
            return PostingGeometry.Rect(x: r.origin.x, y: r.origin.y, width: r.width, height: r.height)
        }
    }

    private static func cgButton(_ b: MouseButton) -> CGMouseButton {
        switch b {
        case .left: return .left
        case .right: return .right
        case .middle: return .center
        case .back: return CGMouseButton(rawValue: 3) ?? .center
        case .forward: return CGMouseButton(rawValue: 4) ?? .center
        }
    }

    private static func downType(_ b: MouseButton) -> CGEventType {
        switch b {
        case .left: return .leftMouseDown
        case .right: return .rightMouseDown
        default: return .otherMouseDown
        }
    }

    private static func upType(_ b: MouseButton) -> CGEventType {
        switch b {
        case .left: return .leftMouseUp
        case .right: return .rightMouseUp
        default: return .otherMouseUp
        }
    }

    private static func dragType(_ held: ButtonMask) -> (CGEventType, MouseButton)? {
        for b in MouseButton.allCases where held.contains(b.mask) {
            switch b {
            case .left: return (.leftMouseDragged, b)
            case .right: return (.rightMouseDragged, b)
            default: return (.otherMouseDragged, b)
            }
        }
        return nil
    }

    public func postMove(dx: Int, dy: Int, held: ButtonMask) {
        let cur = currentLocation()
        let target = PostingGeometry.clamp(
            .init(x: Double(cur.x) + Double(dx), y: Double(cur.y) + Double(dy)), to: displayBounds())
        let (type, button) = CGEventPoster.dragType(held) ?? (.mouseMoved, .left)
        guard
            let e = CGEvent(
                mouseEventSource: source, mouseType: type, mouseCursorPosition: CGPoint(x: target.x, y: target.y),
                mouseButton: CGEventPoster.cgButton(button))
        else { return }
        e.setIntegerValueField(.mouseEventDeltaX, value: Int64(dx))
        e.setIntegerValueField(.mouseEventDeltaY, value: Int64(dy))
        e.setIntegerValueField(.eventSourceUserData, value: CGEventPoster.userData)
        e.post(tap: .cghidEventTap)
    }

    public func postButton(_ button: MouseButton, down: Bool, clickState: Int) {
        guard
            let e = CGEvent(
                mouseEventSource: source, mouseType: down ? CGEventPoster.downType(button) : CGEventPoster.upType(button),
                mouseCursorPosition: currentLocation(), mouseButton: CGEventPoster.cgButton(button))
        else { return }
        e.setIntegerValueField(.mouseEventButtonNumber, value: Int64(button.rawValue))
        e.setIntegerValueField(.mouseEventClickState, value: Int64(clickState))
        e.setIntegerValueField(.eventSourceUserData, value: CGEventPoster.userData)
        e.post(tap: .cghidEventTap)
    }

    public func postScroll(dx: Int, dy: Int) {
        guard
            let e = CGEvent(
                scrollWheelEvent2Source: source, units: .pixel, wheelCount: 2, wheel1: Int32(dy), wheel2: Int32(dx),
                wheel3: 0)
        else { return }
        e.setIntegerValueField(.eventSourceUserData, value: CGEventPoster.userData)
        e.post(tap: .cghidEventTap)
    }
}

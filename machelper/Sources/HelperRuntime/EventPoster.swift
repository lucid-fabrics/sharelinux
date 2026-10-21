import Foundation
import HelperCore

public enum MouseButton: Int, CaseIterable, Sendable {
    case left = 0, right = 1, middle = 2, back = 3, forward = 4

    public var mask: ButtonMask { ButtonMask(rawValue: 1 << UInt8(rawValue)) }
}

/// What the helper asks the window server to do. The real implementation
/// (`CGEventPoster`) posts CGEvents; tests and the harness use a fake, so no
/// test ever moves the owner's mouse.
public protocol EventPoster: AnyObject {
    /// Relative motion in whole points. `held` is the button state the motion
    /// happened under: it decides between "moved" and "dragged".
    func postMove(dx: Int, dy: Int, held: ButtonMask)
    func postButton(_ button: MouseButton, down: Bool, clickState: Int)
    /// Scroll in pixel units: positive `dy` scrolls up, positive `dx` scrolls left
    /// (CGEvent wheel convention).
    func postScroll(dx: Int, dy: Int)
}

/// Pure geometry for the real poster: where a relative move lands.
public enum PostingGeometry {
    public struct Point: Equatable, Sendable {
        public var x: Double, y: Double
        public init(x: Double, y: Double) { self.x = x; self.y = y }
    }
    public struct Rect: Equatable, Sendable {
        public var x: Double, y: Double, width: Double, height: Double
        public init(x: Double, y: Double, width: Double, height: Double) {
            self.x = x; self.y = y; self.width = width; self.height = height
        }
    }

    /// The point inside the union of `displays` nearest to `p` (p itself when it
    /// is already inside one). A cursor pushed against the edge of the desktop
    /// stays on it. Display bounds are "inclusive" of the last pixel row/column.
    /// With no displays the point is returned unchanged.
    public static func clamp(_ p: Point, to displays: [Rect]) -> Point {
        guard !displays.isEmpty else { return p }
        var best = p
        var bestDistance = Double.infinity
        for r in displays {
            let maxX = r.x + max(r.width - 1, 0), maxY = r.y + max(r.height - 1, 0)
            let c = Point(x: min(max(p.x, r.x), maxX), y: min(max(p.y, r.y), maxY))
            let d = (c.x - p.x) * (c.x - p.x) + (c.y - p.y) * (c.y - p.y)
            if d < bestDistance { best = c; bestDistance = d }
            if d == 0 { break }
        }
        return best
    }
}

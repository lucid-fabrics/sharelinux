import CoreGraphics

struct LoupeSpot {
    var source: CGRect
    var frame: CGRect
}

#if os(iOS)
import UIKit

final class PointerLoupeView: UIView {
    static let borderWidth: CGFloat = 2

    init() {
        super.init(frame: .zero)
        isHidden = true
        isUserInteractionEnabled = false
        layer.borderWidth = Self.borderWidth
        layer.borderColor = UIColor(white: 1, alpha: 0.85).cgColor
    }

    required init?(coder: NSCoder) {
        fatalError()
    }

    func show(content frame: CGRect) {
        self.frame = frame.insetBy(dx: -Self.borderWidth, dy: -Self.borderWidth)
        isHidden = false
    }
}

struct ScreenObstructions {
    private static let occlusionGap: CGFloat = 8

    var occlusions: [CGRect] = []
    var topLeft: CGFloat = 0
    var topRight: CGFloat = 0
    var bottomLeft: CGFloat = 0
    var bottomRight: CGFloat = 0

    private var corners: [(radius: CGFloat, left: Bool, top: Bool)] {
        [(topLeft, true, true), (topRight, false, true), (bottomLeft, true, false), (bottomRight, false, false)]
    }

    func clearingCorners(_ insets: UIEdgeInsets) -> UIEdgeInsets {
        var cleared = insets
        for (radius, left, top) in corners {
            let horizontal = left ? cleared.left : cleared.right
            let vertical = top ? cleared.top : cleared.bottom
            guard Self.hides(radius: radius, horizontal: horizontal, vertical: vertical) else { continue }
            let outer = max(horizontal, vertical, Self.cornerInset(radius: radius))
            let inner = radius - (radius * radius - (radius - outer) * (radius - outer)).squareRoot()
            let (newHorizontal, newVertical) = horizontal >= vertical ? (outer, inner) : (inner, outer)
            if left { cleared.left = newHorizontal } else { cleared.right = newHorizontal }
            if top { cleared.top = newVertical } else { cleared.bottom = newVertical }
        }
        return cleared
    }

    func topCornerInset(left: Bool, in area: CGRect) -> CGFloat? {
        let radius = left ? topLeft : topRight
        let side = CGRect(x: left ? area.minX : area.midX, y: area.minY, width: area.width / 2, height: area.height)
        guard radius > 0, !occlusions.contains(where: { $0.intersects(side) }) else { return nil }
        return Self.cornerInset(radius: radius)
    }

    func loupe(for icon: CGRect, in area: CGRect) -> LoupeSpot? {
        if let occlusion = occlusions.first(where: { $0.intersects(icon) }) {
            let gaps = [occlusion.minX - area.minX, area.maxX - occlusion.maxX, occlusion.minY - area.minY, area.maxY - occlusion.maxY]
            let offset = Self.occlusionGap + PointerLoupeView.borderWidth
            var frame = occlusion
            switch gaps.indices.min(by: { gaps[$0] < gaps[$1] }) {
            case 0: frame.origin.x = occlusion.maxX + offset
            case 1: frame.origin.x = occlusion.minX - offset - occlusion.width
            case 2: frame.origin.y = occlusion.maxY + offset
            default: frame.origin.y = occlusion.minY - offset - occlusion.height
            }
            return LoupeSpot(source: occlusion, frame: frame)
        }
        for (radius, left, top) in corners {
            let horizontal = left ? max(icon.minX, area.minX) - area.minX : area.maxX - min(icon.maxX, area.maxX)
            let vertical = top ? max(icon.minY, area.minY) - area.minY : area.maxY - min(icon.maxY, area.maxY)
            guard Self.hides(radius: radius, horizontal: horizontal, vertical: vertical) else { continue }
            let source = CGRect(
                x: left ? area.minX : area.maxX - radius, y: top ? area.minY : area.maxY - radius, width: radius, height: radius)
            let inset = Self.cornerInset(radius: radius) + PointerLoupeView.borderWidth
            return LoupeSpot(source: source, frame: source.offsetBy(dx: left ? inset : -inset, dy: top ? inset : -inset))
        }
        return nil
    }

    private static func hides(radius: CGFloat, horizontal: CGFloat, vertical: CGFloat) -> Bool {
        horizontal < radius && vertical < radius && hypot(radius - horizontal, radius - vertical) > radius
    }

    private static func cornerInset(radius: CGFloat) -> CGFloat {
        radius * (1 - 1 / 2.squareRoot())
    }
}
#endif

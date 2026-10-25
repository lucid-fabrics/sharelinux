#!/usr/bin/env python3
"""Builds the symbolic icons of cmd/sharelinux/data/icons.

The design draws its icons as 1.75 px strokes on a 24 grid. GTK recolours a
symbolic icon by forcing `fill` on every path, which turns strokes into solid
blobs, so each icon is shipped as the outline of its strokes (fill only).

Needs shapely and svgpathtools (pip install shapely svgpathtools):
    python3 packaging/icons/outline.py cmd/sharelinux/data/icons
"""
import math
import sys

from shapely.geometry import MultiPolygon, Polygon
from shapely.ops import unary_union
from svgpathtools import parse_path

STROKE = 1.75

# name -> list of ("path", d) | ("rect", x, y, w, h, rx) | ("circle", cx, cy, r)
ICONS = {
    "fullscreen": [("path", "M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5")],
    "exclusive": [("rect", 7, 3, 10, 18, 5), ("path", "M12 7v3M3 12h2M19 12h2")],
    "sound": [("path", "M4 10v4h4l5 4V6L8 10z"), ("path", "M16.5 9a4 4 0 0 1 0 6M19 6.5a7.5 7.5 0 0 1 0 11")],
    "sound-muted": [("path", "M4 10v4h4l5 4V6L8 10z"), ("path", "M17 9.5l4 5M21 9.5l-4 5")],
    "clipboard": [("rect", 6, 4, 12, 17, 2), ("path", "M9 4V3h6v1"), ("path", "M9 10h6M9 14h4")],
    "send-file": [("path", "M7 3h7l5 5v11a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"), ("path", "M14 3v5h5M12 17v-6M9.5 13.5L12 11l2.5 2.5")],
    "displays": [("rect", 2.5, 5, 13, 9, 1.5), ("rect", 9, 10, 12.5, 9, 1.5)],
    "stats": [("path", "M4 20V10M10 20V4M16 20v-7M22 20H2")],
    "network": [("circle", 12, 12, 9), ("path", "M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18M3.5 9h17M3.5 15h17")],
    "paired": [("path", "M12 3a4 4 0 0 1 4 4v2h1a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h1V7a4 4 0 0 1 4-4z"), ("path", "M12 14v3")],
    "disconnect": [("path", "M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3"), ("path", "M10 8l-4 4 4 4M6 12h10")],
    "macs": [("rect", 3, 4, 18, 12, 2), ("path", "M8 20h8M12 16v4")],
    "recent": [("circle", 12, 12, 9), ("path", "M12 7v5l3 2")],
    "mouse-helper": [("rect", 7, 3, 10, 18, 5), ("path", "M12 7v3")],
    "preferences": [("path", "M4 7h10M18 7h2M4 17h4M12 17h8"), ("circle", 16, 7, 2), ("circle", 10, 17, 2)],
    "search": [("circle", 11, 11, 6.5), ("path", "M16 16l4 4")],
    "add": [("path", "M12 5v14M5 12h14")],
    "more": [("circle", 5, 12, 1), ("circle", 12, 12, 1), ("circle", 19, 12, 1)],
    "view": [("path", "M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"), ("circle", 12, 12, 2.8)],
    "keyboard": [("rect", 2.5, 6, 19, 12, 2), ("path", "M6.5 10h.01M10 10h.01M14 10h.01M17.5 10h.01M7.5 14h9")],
    "close": [("path", "M6 6l12 12M18 6L6 18")],
    "forward": [("path", "M9 5l7 7-7 7")],
    "check": [("path", "M5 12.5l4.5 4.5L19 7.5")],
}


def rect_d(x, y, w, h, r):
    return (f"M{x + r} {y}H{x + w - r}A{r} {r} 0 0 1 {x + w} {y + r}V{y + h - r}"
            f"A{r} {r} 0 0 1 {x + w - r} {y + h}H{x + r}A{r} {r} 0 0 1 {x} {y + h - r}"
            f"V{y + r}A{r} {r} 0 0 1 {x + r} {y}Z")


def circle_d(cx, cy, r):
    return f"M{cx - r} {cy}A{r} {r} 0 1 0 {cx + r} {cy}A{r} {r} 0 1 0 {cx - r} {cy}Z"


def sample(d):
    """Polylines of a path, one per subpath."""
    lines = []
    for sub in parse_path(d).continuous_subpaths():
        points = []
        for seg in sub:
            steps = max(2, int(seg.length() / 0.08))
            for i in range(steps):
                z = seg.point(i / steps)
                points.append((z.real, z.imag))
        end = sub[-1].point(1)
        points.append((end.real, end.imag))
        lines.append((points, sub.isclosed()))
    return lines


def outline(shapes):
    parts = []
    for shape in shapes:
        if shape[0] == "path":
            d = shape[1]
        elif shape[0] == "rect":
            d = rect_d(*shape[1:])
        else:
            d = circle_d(*shape[1:])
        for points, closed in sample(d):
            if len(points) < 2:
                continue
            from shapely.geometry import LineString
            line = LineString(points)
            parts.append(line.buffer(STROKE / 2, quad_segs=16, cap_style="round", join_style="round"))
    merged = unary_union(parts)
    polygons = merged.geoms if isinstance(merged, MultiPolygon) else [merged]
    return polygons


def ring_d(coords):
    pts = list(coords)[:-1]
    out = "M" + " ".join(f"{x:.2f} {y:.2f}" for x, y in pts[:1])
    out += "L" + " ".join(f"{x:.2f} {y:.2f}" for x, y in pts[1:])
    return out + "Z"


def to_svg(polygons):
    d = ""
    for poly in polygons:
        poly = poly.simplify(0.01)
        d += ring_d(poly.exterior.coords)
        for hole in poly.interiors:
            if Polygon(hole).area > 0.05:
                d += ring_d(hole.coords)
    return ('<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">'
            f'<path fill="#bebebe" fill-rule="evenodd" d="{d}"/></svg>\n')


def main(out):
    for name, shapes in ICONS.items():
        with open(f"{out}/sharelinux-{name}-symbolic.svg", "w") as f:
            f.write(to_svg(outline(shapes)))


if __name__ == "__main__":
    main(sys.argv[1])

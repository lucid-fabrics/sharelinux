package main

import (
	"math"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// viewport is where the remote screen sits inside the window, in window pixels.
type viewport struct {
	X, Y, W, H float32
}

// fitViewport scales a framebuffer to the largest size that fits the window
// without changing its aspect ratio and centers it.
func fitViewport(windowWidth, windowHeight, frameWidth, frameHeight int) viewport {
	if windowWidth <= 0 || windowHeight <= 0 || frameWidth <= 0 || frameHeight <= 0 {
		return viewport{}
	}
	scale := math.Min(float64(windowWidth)/float64(frameWidth), float64(windowHeight)/float64(frameHeight))
	width := float32(float64(frameWidth) * scale)
	height := float32(float64(frameHeight) * scale)
	return viewport{
		X: (float32(windowWidth) - width) / 2,
		Y: (float32(windowHeight) - height) / 2,
		W: width,
		H: height,
	}
}

func (v viewport) empty() bool {
	return v.W <= 0 || v.H <= 0
}

// toFramebuffer converts a window pixel to a framebuffer pixel. inside reports
// whether the point is on the picture; the result is clamped to it either way,
// so a drag that leaves the window keeps pointing at the nearest edge.
func (v viewport) toFramebuffer(x, y float32, frameWidth, frameHeight int) (fx, fy int, inside bool) {
	if v.empty() || frameWidth <= 0 || frameHeight <= 0 {
		return 0, 0, false
	}
	inside = x >= v.X && x < v.X+v.W && y >= v.Y && y < v.Y+v.H
	fx = clampInt(int(math.Floor(float64((x-v.X)/v.W)*float64(frameWidth))), 0, frameWidth-1)
	fy = clampInt(int(math.Floor(float64((y-v.Y)/v.H)*float64(frameHeight))), 0, frameHeight-1)
	return fx, fy, inside
}

// toWindow is the inverse of toFramebuffer for the top-left corner of a framebuffer pixel.
func (v viewport) toWindow(fx, fy float64, frameWidth, frameHeight int) (float32, float32) {
	if frameWidth <= 0 || frameHeight <= 0 {
		return 0, 0
	}
	return v.X + float32(fx/float64(frameWidth))*v.W, v.Y + float32(fy/float64(frameHeight))*v.H
}

// scale is the window pixels per framebuffer pixel.
func (v viewport) scale(frameWidth int) float32 {
	if frameWidth <= 0 {
		return 1
	}
	return v.W / float32(frameWidth)
}

func clampInt(value, low, high int) int {
	return min(max(value, low), high)
}

func clampFloat(value, low, high float64) float64 {
	return math.Min(math.Max(value, low), high)
}

// relativePointer follows a mouse that the window has captured: only motion
// arrives, so the position is kept here in framebuffer pixels.
type relativePointer struct {
	X, Y float64
}

func (p *relativePointer) set(x, y int) {
	p.X, p.Y = float64(x), float64(y)
}

// move applies a motion in window pixels and keeps the pointer on the screen.
func (p *relativePointer) move(dx, dy float32, v viewport, frameWidth, frameHeight int) (int, int) {
	if v.empty() || frameWidth <= 0 || frameHeight <= 0 {
		return int(p.X), int(p.Y)
	}
	p.X = clampFloat(p.X+float64(dx)*float64(frameWidth)/float64(v.W), 0, float64(frameWidth-1))
	p.Y = clampFloat(p.Y+float64(dy)*float64(frameHeight)/float64(v.H), 0, float64(frameHeight-1))
	return int(p.X), int(p.Y)
}

// wheelPoints is how many points of scrolling one wheel unit stands for. A notch
// of a mouse wheel is one unit; a trackpad reports fractions of it.
const wheelPoints = 16.0

// scrollWheelMessage is the message type of a Mac's scroll event, the only way
// to scroll smoothly. Servers without it get wheel button pulses.
const scrollWheelMessage = 0x17

// scrollPoints converts SDL wheel units to the points SendScroll takes. A
// server that scrolls by wheel button pulses gets one pulse per notch (16
// points would be 0.8 of a pulse, so a lone notch would send nothing), and
// SendScroll counts horizontal motion positive to the left where SDL counts it
// positive to the right.
func scrollPoints(info rfb.ServerInfo, x, y float32) (float64, float64) {
	if info.Dialect != rfb.DialectApple && !info.Accepts(scrollWheelMessage) {
		return -float64(x) * rfb.StandardPointsPerPulse, float64(y) * rfb.StandardPointsPerPulse
	}
	return -float64(x) * wheelPoints, float64(y) * wheelPoints
}

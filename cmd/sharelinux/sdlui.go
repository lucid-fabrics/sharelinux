package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"
	E "github.com/sagernet/sing/common/exceptions"
)

var errQuit = E.New("quit")

// libraryCandidates are tried in order when SDL3 is not in the loader's path.
func libraryCandidates() []string {
	candidates := []string{}
	if path := os.Getenv("SDL3_LIBRARY"); path != "" {
		candidates = append(candidates, path)
	}
	candidates = append(candidates, sdl.Path())
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/opt/homebrew/lib/libSDL3.dylib", "/usr/local/lib/libSDL3.dylib")
	}
	return candidates
}

// loadSDL opens the SDL3 shared library and starts its video system.
func loadSDL() error {
	var failure error
	for _, path := range libraryCandidates() {
		err := sdl.LoadLibrary(path)
		if err == nil {
			failure = nil
			break
		}
		failure = E.Errors(failure, err)
	}
	if failure != nil {
		return E.Cause(failure, "load SDL3 (install libsdl3 or set SDL3_LIBRARY)")
	}
	_ = sdl.SetHint(sdl.HINT_VIDEO_ALLOW_SCREENSAVER, "1")
	_ = sdl.SetHint(sdl.HINT_MOUSE_FOCUS_CLICKTHROUGH, "1")
	_ = sdl.SetHint(sdl.HINT_APP_NAME, "Open Screen Sharing")
	err := sdl.Init(sdl.INIT_VIDEO)
	if err != nil {
		return E.Cause(err, "start SDL video")
	}
	return nil
}

// canvas draws the viewer's own screens with SDL's built-in 8x8 font.
type canvas struct {
	window   *sdl.Window
	renderer *sdl.Renderer
}

var (
	colorBackground = sdl.Color{R: 22, G: 24, B: 29, A: 255}
	colorText       = sdl.Color{R: 235, G: 238, B: 245, A: 255}
	colorMuted      = sdl.Color{R: 140, G: 148, B: 165, A: 255}
	colorAccent     = sdl.Color{R: 96, G: 165, B: 250, A: 255}
	colorError      = sdl.Color{R: 248, G: 113, B: 113, A: 255}
	colorPanel      = sdl.Color{R: 36, G: 40, B: 48, A: 255}
)

func (c *canvas) density() float32 {
	value, err := c.window.PixelDensity()
	if err != nil || value < 1 {
		return 1
	}
	return value
}

// size is the window size in pixels.
func (c *canvas) size() (int, int) {
	width, height, err := c.window.SizeInPixels()
	if err != nil {
		return 1, 1
	}
	return int(width), int(height)
}

// begin clears the window and sets the scale so one glyph is 8*factor logical pixels.
// It returns the window size in logical pixels.
func (c *canvas) begin(factor float32) (float32, float32) {
	scale := c.density() * factor
	_ = c.renderer.SetScale(scale, scale)
	_ = c.renderer.SetDrawColor(colorBackground.R, colorBackground.G, colorBackground.B, 255)
	_ = c.renderer.Clear()
	width, height := c.size()
	return float32(width) / scale, float32(height) / scale
}

func (c *canvas) text(x, y float32, color sdl.Color, text string) {
	_ = c.renderer.SetDrawColor(color.R, color.G, color.B, color.A)
	_ = c.renderer.DebugText(x, y, text)
}

func (c *canvas) rect(x, y, width, height float32, color sdl.Color) {
	_ = c.renderer.SetDrawColor(color.R, color.G, color.B, color.A)
	_ = c.renderer.RenderFillRect(&sdl.FRect{X: x, Y: y, W: width, H: height})
}

// glyphs is how many 8-pixel characters fit in a logical width.
func glyphs(width float32) int {
	return int(width) / 8
}

// savePNG writes the renderer's current picture to a PNG file.
func (c *canvas) savePNG(directory string) (string, error) {
	surface, err := c.renderer.ReadPixels(nil)
	if err != nil {
		return "", E.Cause(err, "read the window pixels")
	}
	defer surface.Destroy()
	converted, err := surface.Convert(sdl.PIXELFORMAT_ARGB8888)
	if err != nil {
		return "", err
	}
	defer converted.Destroy()
	pixels := converted.Pixels()
	picture := image.NewNRGBA(image.Rect(0, 0, int(converted.W), int(converted.H)))
	for y := range int(converted.H) {
		row := pixels[y*int(converted.Pitch):]
		for x := range int(converted.W) {
			picture.SetNRGBA(x, y, color.NRGBA{R: row[x*4+2], G: row[x*4+1], B: row[x*4], A: 255})
		}
	}
	if directory == "" {
		directory = "."
	}
	path := filepath.Join(directory, "sharelinux-"+time.Now().Format("20060102-150405")+".png")
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	err = png.Encode(file, picture)
	closeErr := file.Close()
	return path, E.Errors(err, closeErr)
}

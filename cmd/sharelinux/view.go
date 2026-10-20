package main

import (
	"fmt"
	"time"
	"unicode"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	toastLifetime = 4 * time.Second
	hudInterval   = 250 * time.Millisecond
)

var qualities = []rfb.Quality{rfb.QualityAdaptive, rfb.QualityFull, rfb.QualityHigh, rfb.QualityMedium, rfb.QualityLow}

type toast struct {
	text    string
	expires time.Time
}

// view shows a connected session until it ends or the person quits.
type view struct {
	app    *app
	live   *liveSession
	client *rfb.Client
	h      *handler
	canvas *canvas

	texture       *sdl.Texture
	textureWidth  int
	textureHeight int
	cursorTexture *sdl.Texture
	cursorImage   *rfb.Cursor
	redraw        bool

	mode     rfb.Mode
	quality  rfb.Quality
	hdr      bool
	keyboard *keyboardLearner
	held     map[uint32]macKey
	swallow  map[uint32]bool
	text     textFilter
	buttons  uint8

	pointerX, pointerY int
	pointerInside      bool
	relative           relativePointer
	focused            bool

	hud      bool
	meter    meter
	frames   int
	hudAt    time.Time
	toasts   []toast
	clip     *clipboardBridge
	transfer *transfers

	dropPaths    []string
	dropX, dropY int
	fetching     bool
}

func (a *app) view(live *liveSession) error {
	v := &view{
		app:      a,
		live:     live,
		client:   live.client,
		h:        live.handler,
		canvas:   a.canvas,
		mode:     live.req.opts.Mode,
		quality:  live.req.opts.Quality,
		hdr:      live.req.opts.HDR,
		keyboard: newKeyboardLearner(live.req.opts.Keyboard),
		held:     make(map[uint32]macKey),
		swallow:  make(map[uint32]bool),
		hud:      live.req.opts.HUD,
		focused:  true,
		redraw:   true,
	}
	return v.run()
}

func (v *view) run() error {
	defer v.release()
	v.transfer = &transfers{client: v.client, handler: v.h, context: v.live.ctx}
	if !v.live.req.opts.NoClipboard {
		v.clip = newClipboardBridge(v.client)
	}
	info := v.client.Info()
	title := info.Name
	if title == "" {
		title = v.live.req.target.Address()
	}
	_ = v.canvas.window.SetTitle(title + " - " + windowTitle)
	_ = v.canvas.window.StartTextInput()
	if info.Flags&rfb.ServerFlagObserveOnly != 0 && v.mode != rfb.ModeObserve {
		v.mode = rfb.ModeObserve
		v.h.toast("The Mac allows viewing only")
	}
	v.applyMode(v.mode, false)
	for {
		select {
		case <-v.live.done:
			err := v.live.runErr
			if err == nil {
				err = E.New("the Mac closed the connection")
			}
			return err
		default:
		}
		var event sdl.Event
		if sdl.WaitEventTimeout(&event, 4) {
			for {
				if isQuit(&event) {
					return errQuit
				}
				quit := v.handleEvent(&event)
				if quit {
					return errQuit
				}
				if !sdl.PollEvent(&event) {
					break
				}
			}
			v.text.Reset()
		}
		v.poll()
		if v.redraw || (v.hud && time.Since(v.hudAt) > hudInterval) {
			v.render()
		}
	}
}

// release hands back everything the view took: grabs, held keys and SDL objects.
func (v *view) release() {
	v.releaseInput()
	_ = v.canvas.window.SetKeyboardGrab(false)
	_ = v.canvas.window.SetMouseGrab(false)
	_ = v.canvas.window.SetRelativeMouseMode(false)
	_ = sdl.ShowCursor()
	_ = v.canvas.window.StopTextInput()
	if v.texture != nil {
		v.texture.Destroy()
	}
	if v.cursorTexture != nil {
		v.cursorTexture.Destroy()
	}
	v.live.cancel()
	_ = v.client.Close()
	<-v.live.done
}

// releaseInput lifts every key and button the Mac thinks is down.
func (v *view) releaseInput() {
	for scancode, key := range v.held {
		_ = v.client.SendKeyCode(key.Code, v.keyboard.Type(), key.Keysym, false)
		delete(v.held, scancode)
	}
	if v.buttons != 0 {
		v.buttons = 0
		_ = v.client.SendPointer(v.pointerX, v.pointerY, 0)
	}
}

// poll moves what the network goroutines collected onto the SDL thread.
func (v *view) poll() {
	v.syncTexture()
	if cursor, changed := v.h.takeCursor(); changed {
		v.setCursor(cursor)
	}
	if clipboard := v.h.takeClipboard(); clipboard != nil && v.clip != nil {
		err := v.clip.fromMac(clipboard)
		if err != nil {
			v.notify("Clipboard: " + err.Error())
		}
	}
	for _, message := range v.h.takeToasts() {
		v.notify(message)
	}
	if drag, changed := v.h.takeDrag(); changed && v.fetching && drag != nil {
		v.fetching = false
		v.transfer.fetch(drag)
	}
	now := time.Now()
	if len(v.toasts) > 0 {
		kept := v.toasts[:0]
		for _, item := range v.toasts {
			if item.expires.After(now) {
				kept = append(kept, item)
			}
		}
		if len(kept) != len(v.toasts) {
			v.redraw = true
		}
		v.toasts = kept
	}
}

func (v *view) notify(message string) {
	v.toasts = append(v.toasts, toast{text: message, expires: time.Now().Add(toastLifetime)})
	v.redraw = true
}

// syncTexture copies changed pixels from the framebuffer into the streaming texture.
func (v *view) syncTexture() {
	dirty, full := v.h.takeDirty()
	width, height := v.client.FramebufferSize()
	if width <= 0 || height <= 0 {
		return
	}
	if v.texture == nil || width != v.textureWidth || height != v.textureHeight {
		if v.texture != nil {
			v.texture.Destroy()
		}
		texture, err := v.canvas.renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_STREAMING, width, height)
		if err != nil {
			v.notify("Cannot create the screen texture: " + err.Error())
			v.texture = nil
			return
		}
		_ = texture.SetBlendMode(sdl.BLENDMODE_NONE)
		_ = texture.SetScaleMode(sdl.SCALEMODE_LINEAR)
		v.texture, v.textureWidth, v.textureHeight = texture, width, height
		full = true
	}
	if !full && dirty.Empty() {
		return
	}
	v.client.Framebuffer(func(framebuffer *bgra.Image) {
		if framebuffer == nil {
			return
		}
		area := dirty.Intersect(framebuffer.Bounds())
		if full {
			area = framebuffer.Bounds()
		}
		if area.Empty() {
			return
		}
		rect := sdl.Rect{X: int32(area.Min.X), Y: int32(area.Min.Y), W: int32(area.Dx()), H: int32(area.Dy())}
		err := v.texture.Update(&rect, framebuffer.Pix[framebuffer.Offset(area.Min.X, area.Min.Y):], int32(framebuffer.Stride))
		if err != nil {
			v.h.toast("Screen update failed: " + err.Error())
		}
	})
	v.frames++
	v.redraw = true
}

// setCursor builds a texture from the Mac's cursor image.
func (v *view) setCursor(cursor *rfb.Cursor) {
	if v.cursorTexture != nil {
		v.cursorTexture.Destroy()
		v.cursorTexture = nil
	}
	v.cursorImage = cursor
	v.redraw = true
	if cursor == nil || cursor.Width <= 0 || cursor.Height <= 0 || len(cursor.Pixels) < cursor.Width*cursor.Height*4 {
		return
	}
	texture, err := v.canvas.renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_STATIC, cursor.Width, cursor.Height)
	if err != nil {
		return
	}
	pixels := straightAlpha(cursor.Pixels)
	_ = texture.Update(nil, pixels, int32(cursor.Width*4))
	_ = texture.SetBlendMode(sdl.BLENDMODE_BLEND)
	_ = texture.SetScaleMode(sdl.SCALEMODE_LINEAR)
	v.cursorTexture = texture
}

// straightAlpha turns the Mac's premultiplied cursor pixels into plain BGRA.
func straightAlpha(pixels []byte) []byte {
	result := make([]byte, len(pixels))
	for index := 0; index+3 < len(pixels); index += 4 {
		alpha := int(pixels[index+3])
		result[index+3] = byte(alpha)
		if alpha == 0 {
			continue
		}
		for channel := range 3 {
			result[index+channel] = byte(min(int(pixels[index+channel])*255/alpha, 255))
		}
	}
	return result
}

func (v *view) viewport() viewport {
	width, height := v.canvas.size()
	frameWidth, frameHeight := v.client.FramebufferSize()
	return fitViewport(width, height, frameWidth, frameHeight)
}

func (v *view) render() {
	c := v.canvas
	_ = c.renderer.SetScale(1, 1)
	_ = c.renderer.SetDrawColor(0, 0, 0, 255)
	_ = c.renderer.Clear()
	port := v.viewport()
	if v.texture != nil && !port.empty() {
		_ = c.renderer.RenderTexture(v.texture, nil, &sdl.FRect{X: port.X, Y: port.Y, W: port.W, H: port.H})
		v.drawCursor(port)
	}
	v.drawOverlay(port)
	_ = c.renderer.Present()
	v.redraw = false
	v.hudAt = time.Now()
}

// drawCursor paints the Mac's cursor where the pointer is, so the shape matches
// what the Mac shows even though SDL only knows the local arrow.
func (v *view) drawCursor(port viewport) {
	if v.cursorTexture == nil || v.cursorImage == nil {
		return
	}
	x, y := float64(v.pointerX), float64(v.pointerY)
	if v.mode == rfb.ModeObserve || !v.pointerInside && v.mode != rfb.ModeExclusiveControl {
		cx, cy := v.h.remoteCursor()
		x, y = float64(cx), float64(cy)
	}
	frameWidth, frameHeight := v.client.FramebufferSize()
	left, top := port.toWindow(x-float64(v.cursorImage.HotspotX), y-float64(v.cursorImage.HotspotY), frameWidth, frameHeight)
	scale := port.scale(frameWidth)
	_ = v.canvas.renderer.RenderTexture(v.cursorTexture, nil, &sdl.FRect{
		X: left, Y: top, W: float32(v.cursorImage.Width) * scale, H: float32(v.cursorImage.Height) * scale,
	})
}

func (v *view) drawOverlay(port viewport) {
	c := v.canvas
	scale := c.density()
	_ = c.renderer.SetScale(scale, scale)
	width, height := c.size()
	logicalWidth, logicalHeight := float32(width)/scale, float32(height)/scale
	y := logicalHeight - 12
	for index := len(v.toasts) - 1; index >= 0; index-- {
		text := ellipsize(v.toasts[index].text, glyphs(logicalWidth)-2)
		c.rect(4, y-4, float32(len(text)*8+8), 16, sdl.Color{R: 0, G: 0, B: 0, A: 200})
		c.text(8, y, colorText, text)
		y -= 20
	}
	if v.hud {
		v.drawHUD()
	}
}

func (v *view) drawHUD() {
	now := time.Now()
	v.meter.Tick(now, v.frames, v.h.updateCount(), v.client.Statistics().WireBytes)
	frameWidth, frameHeight := v.client.FramebufferSize()
	info := v.client.Info()
	lines := []string{
		fmt.Sprintf("screen   %dx%d", frameWidth, frameHeight),
		fmt.Sprintf("fps      %.0f drawn, %.0f updates", v.meter.Frames, v.meter.Updates),
		fmt.Sprintf("bandwidth %s", formatBits(v.meter.BitsPS)),
		fmt.Sprintf("latency  %.0f ms input to picture", float64(v.live.echo.Value())/float64(time.Millisecond)),
		fmt.Sprintf("codec    %s", v.h.lastEncoding()),
		fmt.Sprintf("mode     %s, quality %s", v.mode, v.quality),
		fmt.Sprintf("server   %s, %s, %s", info.Dialect, info.Version, info.SecurityType),
	}
	longest := 0
	for _, line := range lines {
		longest = max(longest, len(line))
	}
	v.canvas.rect(4, 4, float32(longest*8+12), float32(len(lines)*12+8), sdl.Color{R: 0, G: 0, B: 0, A: 190})
	for index, line := range lines {
		v.canvas.text(10, float32(8+index*12), sdl.Color{R: 120, G: 255, B: 160, A: 255}, line)
	}
	v.redraw = false
}

// toFramebuffer converts window coordinates (SDL units) to framebuffer pixels.
func (v *view) toFramebuffer(x, y float32) (int, int, bool) {
	frameWidth, frameHeight := v.client.FramebufferSize()
	density := v.canvas.density()
	return v.viewport().toFramebuffer(x*density, y*density, frameWidth, frameHeight)
}

func (v *view) handleEvent(event *sdl.Event) bool {
	switch event.Type {
	case sdl.EVENT_WINDOW_RESIZED, sdl.EVENT_WINDOW_PIXEL_SIZE_CHANGED, sdl.EVENT_WINDOW_EXPOSED, sdl.EVENT_WINDOW_DISPLAY_SCALE_CHANGED:
		v.redraw = true
	case sdl.EVENT_WINDOW_FOCUS_LOST:
		v.focused = false
		v.releaseInput()
		_ = v.canvas.window.SetKeyboardGrab(false)
		_ = v.canvas.window.SetMouseGrab(false)
		_ = v.canvas.window.SetRelativeMouseMode(false)
		_ = sdl.ShowCursor()
	case sdl.EVENT_WINDOW_FOCUS_GAINED:
		v.focused = true
		v.applyGrabs()
		if v.clip != nil && v.mode != rfb.ModeObserve {
			_ = v.clip.toMac()
		}
	case sdl.EVENT_CLIPBOARD_UPDATE:
		if v.clip != nil && v.mode != rfb.ModeObserve {
			err := v.clip.toMac()
			if err != nil {
				v.notify("Clipboard: " + err.Error())
			}
		}
	case sdl.EVENT_MOUSE_MOTION:
		v.onMotion(event.MouseMotionEvent())
	case sdl.EVENT_MOUSE_BUTTON_DOWN, sdl.EVENT_MOUSE_BUTTON_UP:
		v.onButton(event.MouseButtonEvent())
	case sdl.EVENT_MOUSE_WHEEL:
		v.onWheel(event.MouseWheelEvent())
	case sdl.EVENT_KEY_DOWN, sdl.EVENT_KEY_UP:
		return v.onKey(event.KeyboardEvent())
	case sdl.EVENT_TEXT_INPUT:
		v.onText(event.TextInputEvent().Text)
	case sdl.EVENT_DROP_BEGIN:
		v.dropPaths = nil
	case sdl.EVENT_DROP_FILE:
		drop := event.DropEvent()
		v.dropPaths = append(v.dropPaths, drop.Data)
		v.dropX, v.dropY, _ = v.toFramebuffer(drop.X, drop.Y)
	case sdl.EVENT_DROP_COMPLETE:
		v.finishDrop()
	}
	return false
}

func (v *view) controlling() bool {
	return v.mode != rfb.ModeObserve
}

func (v *view) sendPointer(x, y int) {
	v.pointerX, v.pointerY = x, y
	v.live.echo.Sent(time.Now())
	err := v.client.SendPointer(x, y, v.buttons)
	if err != nil {
		v.app.logger.Debug("pointer", "error", err)
	}
	v.redraw = true
}

func (v *view) onMotion(motion *sdl.MouseMotionEvent) {
	if !v.controlling() {
		return
	}
	if v.mode == rfb.ModeExclusiveControl {
		density := v.canvas.density()
		frameWidth, frameHeight := v.client.FramebufferSize()
		x, y := v.relative.move(motion.Xrel*density, motion.Yrel*density, v.viewport(), frameWidth, frameHeight)
		v.pointerInside = true
		v.sendPointer(x, y)
		return
	}
	x, y, inside := v.toFramebuffer(motion.X, motion.Y)
	v.pointerInside = inside
	v.updateSystemCursor()
	if inside || v.buttons != 0 {
		v.sendPointer(x, y)
	}
}

// updateSystemCursor hides SDL's arrow while the Mac's own cursor is drawn over the picture.
func (v *view) updateSystemCursor() {
	if v.cursorTexture != nil && v.pointerInside && v.controlling() {
		_ = sdl.HideCursor()
		return
	}
	_ = sdl.ShowCursor()
}

func (v *view) onButton(button *sdl.MouseButtonEvent) {
	if !v.controlling() {
		return
	}
	var bit uint8
	switch button.Button {
	case 1:
		bit = rfb.ButtonLeft
	case 2:
		bit = rfb.ButtonMiddle
	case 3:
		bit = rfb.ButtonRight
	default:
		return
	}
	if button.Down {
		x, y, inside := v.toFramebuffer(button.X, button.Y)
		if v.mode == rfb.ModeExclusiveControl {
			x, y, inside = v.pointerX, v.pointerY, true
		}
		if !inside {
			return
		}
		v.buttons |= bit
		v.sendPointer(x, y)
		return
	}
	if v.buttons&bit == 0 {
		return
	}
	v.buttons &^= bit
	v.sendPointer(v.pointerX, v.pointerY)
}

func (v *view) onWheel(wheel *sdl.MouseWheelEvent) {
	if !v.controlling() {
		return
	}
	dx, dy := scrollPoints(wheel.X, wheel.Y)
	err := v.client.SendScroll(v.pointerX, v.pointerY, dx, dy)
	if err != nil {
		v.app.logger.Debug("scroll", "error", err)
	}
}

// keyRune is the character SDL's layout gives a key, or 0 for keys without one.
func keyRune(key sdl.Keycode) rune {
	if key == 0 || key >= 0x40000000 || key > unicode.MaxRune {
		return 0
	}
	return rune(key)
}

func (v *view) onKey(key *sdl.KeyboardEvent) bool {
	scancode := uint32(key.Scancode)
	mod := uint16(key.Mod)
	if key.Down && !key.Repeat {
		if command := chordCommand(scancode, mod); command != commandNone {
			v.swallow[scancode] = true
			return v.runCommand(command)
		}
	}
	if !key.Down && v.swallow[scancode] {
		delete(v.swallow, scancode)
		return false
	}
	if !v.controlling() {
		return false
	}
	v.keyboard.Learn(scancode)
	mapped, found := mapKey(scancode, keyRune(key.Key), mod&modShift != 0, v.keyboard.Type())
	if !found {
		return false
	}
	if key.Down {
		v.held[scancode] = mapped
		v.text.KeyDown(scancode, mod)
	} else {
		if _, down := v.held[scancode]; !down {
			return false
		}
		delete(v.held, scancode)
	}
	v.live.echo.Sent(time.Now())
	err := v.client.SendKeyCode(mapped.Code, v.keyboard.Type(), mapped.Keysym, key.Down)
	if err != nil {
		v.app.logger.Debug("key", "error", err)
	}
	return false
}

func (v *view) onText(text string) {
	if !v.controlling() || v.text.Consume(text) {
		return
	}
	err := v.client.SendText(text)
	if err != nil {
		v.app.logger.Debug("text", "error", err)
	}
}

// applyMode switches between observing, controlling and exclusive control.
func (v *view) applyMode(mode rfb.Mode, announce bool) {
	previous := v.mode
	if previous == rfb.ModeExclusiveControl && mode != previous {
		v.warpToPointer()
	}
	v.mode = mode
	if mode == rfb.ModeObserve {
		v.releaseInput()
	}
	err := v.client.SetMode(mode)
	if err != nil {
		v.notify("Mode: " + err.Error())
	}
	if mode == rfb.ModeExclusiveControl {
		v.relative.set(v.pointerX, v.pointerY)
	}
	v.applyGrabs()
	v.updateSystemCursor()
	if announce {
		v.notify("Mode: " + mode.String())
	}
	v.redraw = true
}

// warpToPointer puts the local pointer where the Mac's pointer is after exclusive mode.
func (v *view) warpToPointer() {
	frameWidth, frameHeight := v.client.FramebufferSize()
	x, y := v.viewport().toWindow(float64(v.pointerX), float64(v.pointerY), frameWidth, frameHeight)
	density := v.canvas.density()
	v.canvas.window.WarpMouseIn(x/density, y/density)
}

// applyGrabs sets keyboard and mouse capture for the current mode and focus.
func (v *view) applyGrabs() {
	window := v.canvas.window
	grab := v.focused && v.controlling() && !v.app.opts.NoGrab
	_ = window.SetKeyboardGrab(grab)
	exclusive := v.focused && v.mode == rfb.ModeExclusiveControl
	_ = window.SetMouseGrab(exclusive)
	_ = window.SetRelativeMouseMode(exclusive)
}

func (v *view) runCommand(command command) bool {
	switch command {
	case commandQuit:
		return true
	case commandReleaseExclusive:
		if v.mode == rfb.ModeExclusiveControl {
			v.applyMode(rfb.ModeControl, true)
		}
	case commandCycleMode:
		next := rfb.Mode(nextCycle(int(v.mode), int(rfb.ModeExclusiveControl)+1))
		v.applyMode(next, true)
	case commandToggleHUD:
		v.hud = !v.hud
		v.redraw = true
	case commandFullscreen:
		fullscreen := v.canvas.window.Flags()&sdl.WINDOW_FULLSCREEN != 0
		_ = v.canvas.window.SetFullscreen(!fullscreen)
	case commandCycleQuality:
		index := 0
		for position, quality := range qualities {
			if quality == v.quality {
				index = position
			}
		}
		v.quality = qualities[nextCycle(index, len(qualities))]
		err := v.client.SetQuality(v.quality)
		v.report("Quality: "+v.quality.String(), err)
	case commandCycleDisplay:
		v.cycleDisplay()
	case commandVirtualDisplay:
		width, height, err := v.canvas.window.Size()
		if err != nil {
			break
		}
		density := 1.0
		if v.canvas.density() >= 1.5 {
			density = 2
		}
		err = v.client.SetVirtualDisplay(int(width), int(height), density, rfb.VirtualDisplayLargestSide, rfb.VirtualDisplayLargestSide)
		v.report(fmt.Sprintf("Virtual display %dx%d@%g", width, height, density), err)
	case commandToggleHDR:
		v.hdr = !v.hdr
		err := v.client.SetHDR(v.hdr)
		v.report(fmt.Sprint("HDR ", map[bool]string{true: "on", false: "off"}[v.hdr]), err)
	case commandRefresh:
		v.report("Refreshing", v.client.RequestRefresh())
	case commandTypeClipboard:
		text, err := sdl.GetClipboardText()
		if err != nil || text == "" {
			v.notify("The local clipboard has no text")
			break
		}
		v.report("Typed the clipboard", v.client.PasteText(text))
	case commandFetchDrag:
		v.fetching = true
		v.report("Asking the Mac for the files it is dragging", v.client.RequestDrag())
	case commandScreenshot:
		path, err := v.canvas.savePNG(v.app.opts.ScreenshotDir)
		v.report("Saved "+path, err)
	}
	return false
}

func (v *view) report(message string, err error) {
	if err != nil {
		v.notify(err.Error())
		return
	}
	v.notify(message)
}

func (v *view) cycleDisplay() {
	layout := v.h.displayLayout()
	if layout == nil || len(layout.Displays) == 0 {
		v.notify("The Mac reported no displays")
		return
	}
	ids := []uint32{0}
	for _, display := range layout.Displays {
		ids = append(ids, display.ID)
	}
	current := 0
	for index, id := range ids {
		if id == layout.Current || (layout.Current == rfb.AllDisplays && id == 0) {
			current = index
		}
	}
	next := ids[nextCycle(current, len(ids))]
	name := "all displays"
	if next != 0 {
		name = fmt.Sprint("display ", next)
	}
	v.report("Showing "+name, v.client.SelectDisplay(next))
}

// finishDrop hands dropped files to the transfer code, aimed at where they were dropped.
func (v *view) finishDrop() {
	paths := v.dropPaths
	v.dropPaths = nil
	if len(paths) == 0 {
		return
	}
	if !v.controlling() {
		v.notify("Switch to control mode to send files")
		return
	}
	v.notify("Dropping " + pluralize(len(paths), "item"))
	v.transfer.drop(paths, v.dropX, v.dropY)
}

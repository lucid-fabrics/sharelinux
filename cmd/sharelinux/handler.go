package main

import (
	"image"
	"log/slog"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

// handler receives the session's events on the network goroutines and keeps
// them for the SDL thread, which owns every SDL object.
type handler struct {
	logger *slog.Logger
	// choose answers a session selection. It runs on the connecting goroutine.
	choose func(rfb.SessionRequest) (rfb.SessionChoice, error)
	echo   *latency

	access       sync.Mutex
	dirty        image.Rectangle
	fullRedraw   bool
	cursor       *rfb.Cursor
	cursorNew    bool
	cursorX      int
	cursorSeen   uint64
	cursorTaken  uint64
	cursorY      int
	layout       *rfb.DisplayLayout
	layoutNew    bool
	clipboard    *rfb.Clipboard
	toasts       []string
	drag         *rfb.RemoteDrag
	dragChanged  bool
	destinations []dropDestination
	abandoned    map[uint32]bool
	updates      uint64
	encoding     rfb.Encoding
	states       chan rfb.State
	transfer     transferState
	transferNew  bool
}

type dropDestination struct {
	ID   uint32
	Path string
}

func newHandler(logger *slog.Logger, echo *latency) *handler {
	return &handler{logger: logger, echo: echo, states: make(chan rfb.State, 16), fullRedraw: true}
}

func (h *handler) StateChanged(state rfb.State) {
	h.logger.Info("state", "state", state.String())
	select {
	case h.states <- state:
	default:
	}
}

func (h *handler) ResizeFramebuffer(width, height int) (*bgra.Image, error) {
	h.logger.Info("framebuffer", "width", width, "height", height)
	h.access.Lock()
	h.fullRedraw = true
	h.access.Unlock()
	return nil, nil
}

func (h *handler) FramebufferUpdated(update rfb.Update) {
	h.access.Lock()
	h.updates++
	rectangle := image.Rect(update.Dirty.X, update.Dirty.Y, update.Dirty.X+update.Dirty.Width, update.Dirty.Y+update.Dirty.Height)
	h.dirty = h.dirty.Union(rectangle)
	if len(update.Rectangles) > 0 {
		h.encoding = update.Rectangles[0].Encoding
	}
	if h.echo != nil {
		h.echo.Updated(time.Now())
	}
	h.access.Unlock()
}

// takeDirty returns the area changed since the last call, and whether the whole picture changed.
func (h *handler) takeDirty() (image.Rectangle, bool) {
	h.access.Lock()
	defer h.access.Unlock()
	dirty, full := h.dirty, h.fullRedraw
	h.dirty, h.fullRedraw = image.Rectangle{}, false
	return dirty, full
}

func (h *handler) CursorChanged(cursor *rfb.Cursor) {
	h.access.Lock()
	h.cursor, h.cursorNew = cursor, true
	h.access.Unlock()
}

func (h *handler) CursorMoved(x, y int) {
	h.access.Lock()
	h.cursorX, h.cursorY = x, y
	h.cursorSeen++
	h.access.Unlock()
}

func (h *handler) takeCursor() (*rfb.Cursor, bool) {
	h.access.Lock()
	defer h.access.Unlock()
	changed := h.cursorNew
	h.cursorNew = false
	return h.cursor, changed
}

// takeCursorMove returns the Mac's cursor position when it has reported one
// since the last call.
func (h *handler) takeCursorMove() (int, int, bool) {
	h.access.Lock()
	defer h.access.Unlock()
	changed := h.cursorSeen != h.cursorTaken
	h.cursorTaken = h.cursorSeen
	return h.cursorX, h.cursorY, changed
}

// cursorReports counts the cursor positions the Mac has reported.
func (h *handler) cursorReports() uint64 {
	h.access.Lock()
	defer h.access.Unlock()
	return h.cursorSeen
}

func (h *handler) remoteCursor() (int, int) {
	h.access.Lock()
	defer h.access.Unlock()
	return h.cursorX, h.cursorY
}

func (h *handler) DisplaysChanged(layout *rfb.DisplayLayout) {
	h.access.Lock()
	h.layout, h.layoutNew = layout, true
	h.access.Unlock()
}

func (h *handler) displayLayout() *rfb.DisplayLayout {
	h.access.Lock()
	defer h.access.Unlock()
	return h.layout
}

func (h *handler) ClipboardReceived(clipboard *rfb.Clipboard) {
	h.access.Lock()
	h.clipboard = clipboard
	h.access.Unlock()
}

func (h *handler) takeClipboard() *rfb.Clipboard {
	h.access.Lock()
	defer h.access.Unlock()
	clipboard := h.clipboard
	h.clipboard = nil
	return clipboard
}

func (h *handler) ClipboardFailed(problem rfb.ClipboardProblem, err error) {
	h.logger.Warn("clipboard", "problem", int(problem), "error", err)
	h.toast("The Mac's clipboard could not be read")
}

func (h *handler) Bell() {}

func (h *handler) KeyboardSourceChanged(source rfb.KeyboardSource) {
	h.logger.Debug("keyboard source", "id", source.ID, "secure", source.SecureInput)
}

func (h *handler) StatusReceived(status rfb.Status) {
	h.logger.Debug("status", "status", status.String())
}

func (h *handler) DeviceInfoReceived(info rfb.DeviceInfo) {
	h.logger.Debug("device", "model", info.Model)
}

func (h *handler) RemoteDragChanged(drag *rfb.RemoteDrag) {
	h.access.Lock()
	h.drag, h.dragChanged = drag, true
	h.access.Unlock()
}

func (h *handler) takeDrag() (*rfb.RemoteDrag, bool) {
	h.access.Lock()
	defer h.access.Unlock()
	changed := h.dragChanged
	h.dragChanged = false
	return h.drag, changed
}

func (h *handler) DropDestinationReceived(id uint32, path string) {
	h.access.Lock()
	defer h.access.Unlock()
	if h.abandoned[id] {
		delete(h.abandoned, id)
		return
	}
	h.destinations = append(h.destinations, dropDestination{ID: id, Path: path})
}

// abandonDestination is called when a drop ends without its destination. One
// that is already here is dropped, and one that arrives later is ignored, so
// it does not stay in the list for the rest of the session.
func (h *handler) abandonDestination(id uint32) {
	h.access.Lock()
	defer h.access.Unlock()
	for index, destination := range h.destinations {
		if destination.ID == id {
			h.destinations = append(h.destinations[:index], h.destinations[index+1:]...)
			return
		}
	}
	if h.abandoned == nil {
		h.abandoned = make(map[uint32]bool)
	}
	h.abandoned[id] = true
}

func (h *handler) takeDestination(id uint32) (string, bool) {
	h.access.Lock()
	defer h.access.Unlock()
	for index, destination := range h.destinations {
		if destination.ID == id {
			h.destinations = append(h.destinations[:index], h.destinations[index+1:]...)
			return destination.Path, true
		}
	}
	return "", false
}

func (h *handler) SystemCommandsChanged(commands []rfb.SystemCommand) {
	h.logger.Debug("system commands", "count", len(commands))
}

func (h *handler) ChooseSession(request rfb.SessionRequest) (rfb.SessionChoice, error) {
	if h.choose == nil {
		return rfb.SessionCancel, E.New("the Mac asks which session to share")
	}
	return h.choose(request)
}

// setTransfer records the state of the running file copy for the copy card.
func (h *handler) setTransfer(state transferState) {
	h.access.Lock()
	h.transfer, h.transferNew = state, true
	h.access.Unlock()
}

func (h *handler) takeTransfer() (transferState, bool) {
	h.access.Lock()
	defer h.access.Unlock()
	changed := h.transferNew
	h.transferNew = false
	return h.transfer, changed
}

// toast queues a short message for the overlay.
func (h *handler) toast(message string) {
	h.access.Lock()
	h.toasts = append(h.toasts, message)
	h.access.Unlock()
}

func (h *handler) takeToasts() []string {
	h.access.Lock()
	defer h.access.Unlock()
	toasts := h.toasts
	h.toasts = nil
	return toasts
}

func (h *handler) lastEncoding() rfb.Encoding {
	h.access.Lock()
	defer h.access.Unlock()
	return h.encoding
}

func (h *handler) updateCount() uint64 {
	h.access.Lock()
	defer h.access.Unlock()
	return h.updates
}

// layoutChanged reports, once, that the Mac sent a display layout.
func (h *handler) layoutChanged() bool {
	h.access.Lock()
	defer h.access.Unlock()
	changed := h.layoutNew
	h.layoutNew = false
	return changed
}

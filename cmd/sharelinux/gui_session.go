//go:build linux

package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/cairo"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	sessionPageTag = "session"
	statsInterval  = 250 * time.Millisecond
	// pollInterval is how often a session collects what the network goroutines have for it, in milliseconds.
	pollInterval = 8
	// modAltGr is the Mod5 bit GDK reports for the third level key.
	modAltGr gdk.ModifierType = 1 << 7
	// revealZone is how far from the top edge the pointer wakes the header in fullscreen.
	revealZone = 6.0
)

// sessionView is the page that shows one connected Mac.
type sessionView struct {
	g      *gui
	live   *liveSession
	client *rfb.Client
	h      *handler

	page    *adw.NavigationPage
	toolbar *adw.ToolbarView
	screen  *screenView
	scroll  *gtk.ScrolledWindow
	im      *gtk.IMMulticontext

	modes *adw.ToggleGroup
	// The floating bar: the Mac's name, the delay and the buttons.
	bar             *gtk.Revealer
	identity        *gtk.Box
	delay           *gtk.Label
	exclusiveButton *gtk.Button
	soundButton     *gtk.Button
	soundIcon       *gtk.Image
	statsButton     *gtk.Button
	displayButton   *gtk.MenuButton
	sessionMenu     *gtk.MenuButton
	hint            *gtk.Revealer
	hintHide        coreglib.SourceHandle
	thumbTimer      coreglib.SourceHandle
	// thumbBusy is set while a last frame is being encoded and written.
	thumbBusy     atomic.Bool
	displayMenu   *gio.Menu
	actions       *gio.SimpleActionGroup
	displayAction *gio.SimpleAction
	qualityAction *gio.SimpleAction
	statsAction   *gio.SimpleAction
	hdrAction     *gio.SimpleAction
	muteAction    *gio.SimpleAction

	stats      *gtk.Box
	statValues []*gtk.Label
	statNames  []*gtk.Label
	statGrid   *gtk.Grid
	spark      *gtk.DrawingArea
	delays     []float64
	delayAt    time.Time
	copyCard   *gtk.Box
	copyTitle  *gtk.Label
	copyValue  *gtk.Label
	copyBar    *gtk.ProgressBar
	copyCancel *gtk.Button
	// copyPulse keeps the bar moving while a drop waits for the Mac's answer.
	copyPulse bool
	copyHide  coreglib.SourceHandle

	mode    rfb.Mode
	quality rfb.Quality
	hdr     bool
	// muted is the session's choice to silence the Mac's sound; it holds across stream restarts.
	muted     bool
	showStats bool
	keyboard  *keyboardLearner
	held      map[uint32]macKey
	swallow   map[uint32]bool
	text      textFilter
	buttons   uint8
	clicks    clickFilter
	focused   bool

	// lockBackend is how Exclusive mode holds the pointer here. lock follows the
	// request from start to release: the pointer's relative motion drives the
	// Mac's pointer only once lock.held says it really is held.
	lockBackend lockBackend
	lock        pointerLock
	rel         relativePointer
	warp        warpEcho
	// helper is this session's use of the Mac mouse helper.
	helper *helperUse
	// sent is set once a pointer position went to the Mac; sentReports is how many
	// cursor reports the Mac had made by then.
	sent        bool
	sentReports uint64
	// pointerSends counts the pointer and scroll events sent over Screen Sharing
	// (logged around helper routing, which must send none).
	pointerSends uint64

	// overScreen is set while the pointer is over the picture's widget.
	overScreen bool

	pointerX, pointerY int
	pointerInside      bool
	pointerHeld        bool
	// clickCount is GTK's press count (1 single, 2 double...) of the button
	// held now. The Mac reads it from the press and its release.
	clickCount int
	drag       dragAsk

	transfer  *transfers
	clip      *clipboardBridge
	meter     meter
	frames    uint64
	fps       float64
	statsAt   time.Time
	loggedAt  time.Time
	pollTimer coreglib.SourceHandle
	ended     bool

	// vid is the session's use of the Mac's video stream.
	vid videoSession
	// hdrNotice is what was last said about the screen the window is on, so it is said once.
	hdrNotice string

	layoutSeen   bool
	lastWidth    int
	lastHeight   int
	virtualAsked bool
	fullscreen   bool
	hideTimer    coreglib.SourceHandle
	// outliving holds the handlers on objects that outlive the session; end() removes them.
	outliving signalScope
}

// startSession shows a connected Mac on its own page.
func (g *gui) startSession(live *liveSession) {
	v := &sessionView{
		g:           g,
		live:        live,
		client:      live.client,
		h:           live.handler,
		mode:        live.req.opts.Mode,
		quality:     live.req.opts.Quality,
		hdr:         g.app.hdrEffective(live.req.opts.HDR),
		keyboard:    newKeyboardLearner(live.req.opts.Keyboard),
		held:        map[uint32]macKey{},
		swallow:     map[uint32]bool{},
		focused:     true,
		lockBackend: detectLockBackend(),
	}
	v.helper = newHelperUse(v)
	v.showStats = live.req.opts.HUD
	g.session = v
	g.setListActions(false)
	v.build()
	g.nav.Push(v.page)
	v.start()
}

func (v *sessionView) build() {
	info := v.client.Info()
	name := info.Name
	if name == "" {
		name = v.live.req.target.Address()
	}

	v.screen = newScreenView()
	v.g.window.SetTitle(name)
	v.im = gtk.NewIMMulticontext()
	v.im.SetClientWidget(v.screen)
	v.scroll = gtk.NewScrolledWindow()
	v.scroll.SetPolicy(gtk.PolicyAutomatic, gtk.PolicyAutomatic)
	v.scroll.SetChild(v.screen)

	overlay := gtk.NewOverlay()
	overlay.SetChild(v.scroll)
	v.buildStats(overlay)
	v.buildCopyCard(overlay)
	v.buildHint(overlay)
	v.buildBar(overlay, name)
	v.buildModes()

	// The window controls only: the name and the buttons float over the picture.
	header := adw.NewHeaderBar()
	header.SetShowTitle(false)
	v.toolbar = adw.NewToolbarView()
	v.toolbar.AddTopBar(header)
	v.toolbar.SetContent(overlay)
	v.page = adw.NewNavigationPageWithTag(v.toolbar, name, sessionPageTag)
	// Leaving by the back arrow too: keep the last picture while the page is still shown.
	v.page.ConnectHiding(v.captureLast)
	v.installActions()
	v.installInput()
	v.installFullscreenReveal()
}

// toolButton is one icon button of the floating bar. It never takes the focus
// by a click, so the next key still goes to the Mac.
func (v *sessionView) toolButton(iconName, label string, click func()) *gtk.Button {
	button := gtk.NewButton()
	button.SetChild(icon(iconName, 20))
	button.AddCSSClass("oss-tool")
	button.SetTooltipText(label)
	setAccessibleLabel(button, label)
	button.SetFocusOnClick(false)
	button.ConnectClicked(func() {
		click()
		if v.controlling() {
			v.screen.GrabFocus()
		}
	})
	return button
}

// barSeparator is the thin line between groups of buttons.
func barSeparator() *gtk.Box {
	separator := gtk.NewBox(gtk.OrientationVertical, 0)
	separator.AddCSSClass("oss-bar-sep")
	separator.SetVAlign(gtk.AlignCenter)
	return separator
}

// buildBar builds the glass bar at the top of the picture: who this is, how
// long the Mac takes to answer, and the buttons a session needs.
func (v *sessionView) buildBar(overlay *gtk.Overlay, name string) {
	dot := gtk.NewBox(gtk.OrientationHorizontal, 0)
	dot.AddCSSClass("oss-dot")
	dot.AddCSSClass("online")
	dot.SetVAlign(gtk.AlignCenter)
	title := gtk.NewLabel(name)
	title.AddCSSClass("oss-bar-name")
	title.SetEllipsize(3)
	title.SetMaxWidthChars(24)
	v.delay = gtk.NewLabel("")
	v.delay.AddCSSClass("oss-bar-delay")
	v.identity = gtk.NewBox(gtk.OrientationHorizontal, 10)
	v.identity.SetMarginStart(8)
	v.identity.SetMarginEnd(12)
	v.identity.SetVAlign(gtk.AlignCenter)
	v.identity.Append(dot)
	v.identity.Append(title)
	v.identity.Append(v.delay)
	v.identity.SetTooltipText(connectedText(0))

	bar := gtk.NewBox(gtk.OrientationHorizontal, 4)
	bar.AddCSSClass("oss-glass")
	bar.AddCSSClass("oss-bar")
	bar.SetOverflow(gtk.OverflowHidden)
	setAccessibleLabel(bar, "Session")
	bar.Append(v.identity)
	bar.Append(v.helper.chip)
	bar.Append(barSeparator())

	v.exclusiveButton = v.toolButton(iconExclusive, "Exclusive mode", func() {
		if v.mode == rfb.ModeExclusiveControl {
			v.applyMode(rfb.ModeControl, true)
		} else {
			v.applyMode(rfb.ModeExclusiveControl, true)
		}
	})
	v.exclusiveButton.SetTooltipText(exclusiveTooltip(v.lockBackend))
	bar.Append(v.exclusiveButton)

	v.soundButton = v.toolButton(iconSound, "Sound", func() { v.runCommand(commandMuteSound) })
	v.soundIcon = v.soundButton.Child().(*gtk.Image)
	bar.Append(v.soundButton)

	bar.Append(v.toolButton(iconSendFile, "Send a file", v.chooseFilesToSend))

	v.displayButton = gtk.NewMenuButton()
	v.displayButton.SetIconName(iconDisplays)
	v.displayButton.AddCSSClass("oss-tool")
	v.displayButton.SetTooltipText("Displays")
	setAccessibleLabel(v.displayButton, "Displays")
	v.displayButton.SetFocusOnClick(false)
	bar.Append(v.displayButton)

	v.statsButton = v.toolButton(iconStats, "Stats", func() { v.setStats(!v.showStats) })
	bar.Append(v.statsButton)

	bar.Append(v.toolButton(iconFullscreen, "Full screen", v.toggleFullscreen))

	v.sessionMenu = gtk.NewMenuButton()
	v.sessionMenu.SetIconName(iconMore)
	v.sessionMenu.AddCSSClass("oss-tool")
	v.sessionMenu.SetTooltipText("More")
	setAccessibleLabel(v.sessionMenu, "More")
	v.sessionMenu.SetFocusOnClick(false)
	v.sessionMenu.SetPrimary(true)
	bar.Append(v.sessionMenu)

	bar.Append(barSeparator())
	disconnect := v.toolButton(iconDisconnect, "Disconnect", func() { v.disconnect() })
	disconnect.AddCSSClass("danger")
	bar.Append(disconnect)

	// A closed menu hands the keyboard back to the picture, so the next key goes to the Mac.
	for _, button := range []*gtk.MenuButton{v.sessionMenu, v.displayButton} {
		button.NotifyProperty("active", func() {
			if !button.Active() && v.controlling() {
				v.screen.GrabFocus()
			}
		})
	}

	v.bar = gtk.NewRevealer()
	v.bar.SetTransitionType(gtk.RevealerTransitionTypeSlideDown)
	v.bar.SetTransitionDuration(180)
	v.bar.SetRevealChild(true)
	v.bar.SetChild(bar)
	v.bar.SetHAlign(gtk.AlignCenter)
	v.bar.SetVAlign(gtk.AlignStart)
	v.bar.SetMarginTop(16)
	overlay.AddOverlay(v.bar)
}

// buildModes builds the View, Control and Exclusive choice. It sits in the More menu.
func (v *sessionView) buildModes() {
	v.modes = adw.NewToggleGroup()
	for _, item := range []struct{ id, label, icon string }{
		{"observe", "View", iconView},
		{"control", "Control", iconMouseHelper},
		{"exclusive", "Exclusive", iconExclusive},
	} {
		toggle := adw.NewToggle()
		toggle.SetName(item.id)
		toggle.SetLabel(item.label)
		toggle.SetIconName(item.icon)
		v.modes.Add(toggle)
	}
	if v.client.Info().Flags&rfb.ServerFlagObserveOnly != 0 {
		for _, id := range []string{"control", "exclusive"} {
			toggle := v.modes.ToggleByName(id)
			toggle.SetEnabled(false)
			toggle.SetTooltip("This Mac only allows viewing")
		}
		v.exclusiveButton.SetSensitive(false)
	}
	v.modes.ToggleByName("exclusive").SetTooltip(exclusiveTooltip(v.lockBackend))
	v.modes.SetActiveName(modeID(allowedMode(v.mode, v.client.Info().Flags)))
	v.syncModeButton()
}

// syncModeButton lights the Exclusive button while Exclusive mode is on.
func (v *sessionView) syncModeButton() {
	if v.exclusiveButton == nil {
		return
	}
	if v.mode == rfb.ModeExclusiveControl {
		v.exclusiveButton.AddCSSClass("on")
	} else {
		v.exclusiveButton.RemoveCSSClass("on")
	}
}

// buildHint builds the line that says how to let go of the mouse. It shows for a
// few seconds when Exclusive mode starts.
func (v *sessionView) buildHint(overlay *gtk.Overlay) {
	box := gtk.NewBox(gtk.OrientationHorizontal, 4)
	box.AddCSSClass("oss-hint")
	before := gtk.NewLabel("Exclusive mode is on. Press ")
	box.Append(before)
	for _, key := range []string{"Ctrl", "Alt", "Esc"} {
		cap := gtk.NewLabel(key)
		cap.AddCSSClass("oss-keycap")
		box.Append(cap)
	}
	box.Append(gtk.NewLabel(" to let go of the mouse."))
	box.SetCanTarget(false)
	v.hint = gtk.NewRevealer()
	v.hint.SetTransitionType(gtk.RevealerTransitionTypeCrossfade)
	v.hint.SetTransitionDuration(200)
	v.hint.SetChild(box)
	v.hint.SetHAlign(gtk.AlignCenter)
	v.hint.SetVAlign(gtk.AlignStart)
	v.hint.SetMarginTop(84)
	v.hint.SetCanTarget(false)
	overlay.AddOverlay(v.hint)
}

// hintShownFor is how long the exclusive hint stays, in milliseconds.
const hintShownFor = 8000

// showExclusiveHint shows the hint for a while, or hides it at once.
func (v *sessionView) showExclusiveHint(on bool) {
	if v.hintHide != 0 {
		coreglib.SourceRemove(v.hintHide)
		v.hintHide = 0
	}
	v.hint.SetRevealChild(on)
	if !on {
		return
	}
	v.hintHide = coreglib.TimeoutAdd(hintShownFor, func() bool {
		v.hintHide = 0
		v.hint.SetRevealChild(false)
		return false
	})
}

// disconnect leaves the session. The last picture is kept first, while it is still on screen.
func (v *sessionView) disconnect() {
	v.captureLast()
	v.g.nav.Pop()
}

func modeID(mode rfb.Mode) string {
	switch mode {
	case rfb.ModeObserve:
		return "observe"
	case rfb.ModeExclusiveControl:
		return "exclusive"
	}
	return "control"
}

func modeFromID(id string) rfb.Mode {
	switch id {
	case "observe":
		return rfb.ModeObserve
	case "exclusive":
		return rfb.ModeExclusiveControl
	}
	return rfb.ModeControl
}

func (v *sessionView) buildStats(overlay *gtk.Overlay) {
	heading := gtk.NewLabel("STREAM")
	heading.AddCSSClass("overline")
	heading.SetXAlign(0)
	v.statGrid = gtk.NewGrid()
	v.statGrid.SetRowSpacing(10)
	v.statGrid.SetColumnSpacing(14)
	v.spark = gtk.NewDrawingArea()
	v.spark.SetContentWidth(208)
	v.spark.SetContentHeight(36)
	v.spark.SetDrawFunc(v.drawSparkline)
	v.spark.SetTooltipText("Delay over the last minute")
	setAccessibleLabel(v.spark, "Delay over the last minute")
	v.stats = gtk.NewBox(gtk.OrientationVertical, 10)
	v.stats.AddCSSClass("oss-stats")
	v.stats.SetHAlign(gtk.AlignEnd)
	v.stats.SetVAlign(gtk.AlignStart)
	v.stats.SetMarginEnd(24)
	v.stats.SetMarginTop(88)
	v.stats.SetSizeRequest(240, -1)
	v.stats.SetCanTarget(false)
	v.stats.Append(heading)
	v.stats.Append(v.statGrid)
	v.stats.Append(v.spark)
	v.stats.SetVisible(false)
	overlay.AddOverlay(v.stats)
}

// delayHistory is how many one second samples the sparkline keeps.
const delayHistory = 60

// noteDelay adds a sample of the delay, at most once a second.
func (v *sessionView) noteDelay(now time.Time, ms float64) {
	if now.Sub(v.delayAt) < time.Second {
		return
	}
	v.delayAt = now
	v.delays = appendSample(v.delays, ms, delayHistory)
	if v.spark != nil {
		v.spark.QueueDraw()
	}
}

// drawSparkline draws the delay history as one blue line.
func (v *sessionView) drawSparkline(_ *gtk.DrawingArea, cr *cairo.Context, width, height int) {
	points := sparkPoints(v.delays, float64(width), float64(height), 4)
	if len(points) < 2 {
		return
	}
	cr.SetSourceRGB(0x5B/255.0, 0x8C/255.0, 0xFF/255.0)
	cr.SetLineWidth(1.75)
	cr.SetLineJoin(cairo.LineJoinRound)
	cr.SetLineCap(cairo.LineCapRound)
	cr.MoveTo(points[0][0], points[0][1])
	for _, point := range points[1:] {
		cr.LineTo(point[0], point[1])
	}
	cr.Stroke()
}

func (v *sessionView) buildCopyCard(overlay *gtk.Overlay) {
	v.copyTitle = gtk.NewLabel("")
	v.copyTitle.SetHExpand(true)
	v.copyTitle.SetXAlign(0)
	v.copyTitle.SetEllipsize(3)
	v.copyValue = gtk.NewLabel("")
	v.copyValue.AddCSSClass("dim-label")
	v.copyCancel = iconButton(iconClose, "Cancel the copy", 16)
	v.copyCancel.AddCSSClass("flat")
	v.copyCancel.AddCSSClass("circular")
	v.copyCancel.SetTooltipText("Cancel the copy")
	v.copyCancel.ConnectClicked(func() {
		if v.transfer != nil {
			v.transfer.cancelCopy()
		}
	})
	row := gtk.NewBox(gtk.OrientationHorizontal, 10)
	row.Append(v.copyTitle)
	row.Append(v.copyValue)
	row.Append(v.copyCancel)
	v.copyBar = gtk.NewProgressBar()
	v.copyCard = gtk.NewBox(gtk.OrientationVertical, 8)
	v.copyCard.AddCSSClass("oss-copy-card")
	v.copyCard.SetHAlign(gtk.AlignCenter)
	v.copyCard.SetVAlign(gtk.AlignEnd)
	v.copyCard.SetMarginBottom(24)
	v.copyCard.SetSizeRequest(340, -1)
	v.copyCard.Append(row)
	v.copyCard.Append(v.copyBar)
	v.copyCard.SetVisible(false)
	overlay.AddOverlay(v.copyCard)
}

// installActions builds the session menus and their actions.
func (v *sessionView) installActions() {
	v.actions = gio.NewSimpleActionGroup()
	simple := func(name string, f func()) {
		action := gio.NewSimpleAction(name, nil)
		action.ConnectActivate(func(*glib.Variant) { f() })
		v.actions.AddAction(action)
	}
	simple("disconnect", func() { v.disconnect() })
	simple("type-clipboard", v.typeClipboard)
	simple("fetch-drag", func() { v.runCommand(commandFetchDrag) })
	simple("refresh", func() { v.report("Refreshing", v.client.RequestRefresh()) })
	simple("screenshot", func() { v.runCommand(commandScreenshot) })
	simple("virtual", func() { v.runCommand(commandVirtualDisplay) })

	v.statsAction = gio.NewSimpleActionStateful("stats", nil, glib.NewVariantBoolean(v.showStats))
	v.statsAction.ConnectActivate(func(*glib.Variant) { v.setStats(!v.showStats) })
	v.actions.AddAction(v.statsAction)

	v.muteAction = gio.NewSimpleActionStateful("mute", nil, glib.NewVariantBoolean(v.muted))
	v.muteAction.ConnectActivate(func(*glib.Variant) { v.runCommand(commandMuteSound) })
	v.actions.AddAction(v.muteAction)

	v.hdrAction = gio.NewSimpleActionStateful("hdr", nil, glib.NewVariantBoolean(v.hdr))
	v.hdrAction.ConnectActivate(func(*glib.Variant) { v.runCommand(commandToggleHDR) })
	v.actions.AddAction(v.hdrAction)

	v.displayAction = gio.NewSimpleActionStateful("display", glib.NewVariantType("s"), glib.NewVariantString("0"))
	v.displayAction.ConnectActivate(func(parameter *glib.Variant) {
		var id uint32
		_, text := parameter.DupString()
		_, _ = fmt.Sscan(text, &id)
		v.selectDisplay(text, id)
	})
	v.actions.AddAction(v.displayAction)

	v.qualityAction = gio.NewSimpleActionStateful("quality", glib.NewVariantType("s"), glib.NewVariantString(v.quality.String()))
	v.qualityAction.ConnectActivate(func(parameter *glib.Variant) {
		_, text := parameter.DupString()
		quality, err := parseQuality(text)
		if err != nil {
			return
		}
		v.setQuality(quality)
	})
	v.actions.AddAction(v.qualityAction)
	v.toolbar.InsertActionGroup("session", v.actions)

	v.displayMenu = gio.NewMenu()
	v.displayButton.SetMenuModel(v.displayMenu)
	v.refreshDisplays()

	qualityMenu := gio.NewMenu()
	for _, quality := range qualities {
		qualityMenu.Append(qualityLabel(quality), "session.quality::"+quality.String())
	}
	modes := gio.NewMenu()
	custom := gio.NewMenuItem("", "")
	custom.SetAttributeValue("custom", glib.NewVariantString("modes"))
	modes.AppendItem(custom)
	menu := gio.NewMenu()
	menu.AppendSection("", modes)
	first := gio.NewMenu()
	first.AppendSubmenu("Picture quality", qualityMenu)
	first.Append("Paste clipboard as keystrokes", "session.type-clipboard")
	first.Append("Get files the Mac is dragging", "session.fetch-drag")
	first.Append("Refresh the picture", "session.refresh")
	first.Append("Save a screenshot", "session.screenshot")
	second := gio.NewMenu()
	second.Append("Virtual display at window size", "session.virtual")
	second.Append("HDR", "session.hdr")
	third := gio.NewMenu()
	third.Append("Preferences", "win.preferences")
	third.Append("Disconnect", "session.disconnect")
	menu.AppendSection("", first)
	menu.AppendSection("", second)
	menu.AppendSection("", third)
	popover := gtk.NewPopoverMenuFromModel(menu)
	popover.AddChild(v.modes, "modes")
	v.sessionMenu.SetPopover(&popover.Popover)
}

// refreshDisplays rebuilds the Display menu from the layout the Mac reported.
func (v *sessionView) refreshDisplays() {
	layout := v.h.displayLayout()
	v.displayMenu.RemoveAll()
	for _, choice := range displayChoices(layout) {
		v.displayMenu.Append(choice.Label, fmt.Sprintf("session.display::%d", choice.ID))
	}
	v.displayAction.SetState(glib.NewVariantString(fmt.Sprint(currentDisplay(layout))))
	v.displayButton.SetTooltipText(displayLabel(layout))
}

func (v *sessionView) selectDisplay(label string, id uint32) {
	v.report("", v.client.SelectDisplay(id))
	v.displayAction.SetState(glib.NewVariantString(fmt.Sprint(id)))
}

func (v *sessionView) setQuality(quality rfb.Quality) {
	v.quality = quality
	v.qualityAction.SetState(glib.NewVariantString(quality.String()))
	v.report("", v.client.SetQuality(quality))
}

// setMuted mutes the Mac's sound for this session, or plays it again. The stream keeps running.
func (v *sessionView) setMuted(muted bool) {
	v.muted = muted
	v.muteAction.SetState(glib.NewVariantBoolean(muted))
	v.soundIcon.SetFromIconName(map[bool]string{true: iconSoundMuted, false: iconSound}[muted])
	v.soundButton.SetTooltipText(map[bool]string{true: "Sound is off", false: "Sound"}[muted])
	if stream := v.vid.stream; stream != nil {
		stream.setMuted(muted)
	}
	v.notify(map[bool]string{true: "Sound muted", false: "Sound on"}[muted])
}

func (v *sessionView) setStats(on bool) {
	v.showStats = on
	v.statsAction.SetState(glib.NewVariantBoolean(on))
	v.stats.SetVisible(on)
	if on {
		v.statsButton.AddCSSClass("on")
	} else {
		v.statsButton.RemoveCSSClass("on")
	}
	if on {
		v.statsAt = time.Time{}
		v.updateStats()
	}
	_ = v.g.app.updatePreferences(func(s *settings) { s.ShowStats = on })
}

// start begins the frame loop and the shared clipboard.
func (v *sessionView) start() {
	v.transfer = &transfers{client: v.client, handler: v.h, context: v.live.ctx, folder: func() string { return v.g.app.preferences().downloadFolder() }}
	if !v.live.req.opts.NoClipboard {
		v.clip = newClipboardBridge(v.client, v.screen, func(message string) { v.notify(message) }, v.controlling)
	}
	// A frame clock tick would stop while the picture is still, and with it the
	// check for a connection that ended, so a timer drives the polling instead.
	v.pollTimer = coreglib.TimeoutAdd(pollInterval, func() bool {
		if v.ended {
			return false
		}
		v.poll()
		return !v.ended
	})
	v.applyMode(v.mode, false)
	v.helper.begin()
	v.setStats(v.showStats)
	v.screen.watchDrawing()
	// The home screen shows the last picture of this Mac: keep it fresh, so a
	// window that closes without a clean goodbye still leaves one.
	v.thumbTimer = coreglib.TimeoutSecondsAdd(30, func() bool {
		if v.ended {
			return false
		}
		v.captureLast()
		return true
	})
	v.screen.GrabFocus()
	prefs := v.g.app.preferences()
	v.screen.setRealSize(!prefs.FitWindow)
}

// end hands back everything the session took. It is safe to call twice.
func (v *sessionView) end() {
	if v.ended {
		return
	}
	v.ended = true
	if v.hideTimer != 0 {
		coreglib.SourceRemove(v.hideTimer)
	}
	if v.hintHide != 0 {
		coreglib.SourceRemove(v.hintHide)
		v.hintHide = 0
	}
	if v.thumbTimer != 0 {
		coreglib.SourceRemove(v.thumbTimer)
		v.thumbTimer = 0
	}
	if v.copyHide != 0 {
		coreglib.SourceRemove(v.copyHide)
		v.copyHide = 0
	}
	// The window outlives the session: a handler left on it would keep this
	// session, and its picture, alive and keep reacting to fullscreen.
	v.outliving.release()
	v.im.SetClientWidget(nil)
	v.releaseInput()
	if v.clip != nil {
		v.clip.close()
	}
	v.screen.inhibitShortcuts(false)
	v.setPointerLock(false)
	v.helper.end()
	coreglib.SourceRemove(v.pollTimer)
	// The decoder presents into the widget: it stops before the widget can go.
	v.stopVideo()
	live := v.live
	go func() {
		live.cancel()
		_ = live.client.Close()
		<-live.done
	}()
	if v.fullscreen {
		v.g.window.Unfullscreen()
	}
	v.g.window.SetTitle(windowName)
	// The page may still be animating out of view: tear it down afterwards.
	releaseAfterTransition(v.page, v.release)
}

// release frees what the session's widgets hold. A page that was merely
// popped stayed in memory with its whole picture, because the closures on its
// widgets held the session (see destroyTree). The picture's tiles go at once,
// the widgets and actions are destroyed, and the Go collector then takes the
// session and its framebuffer.
func (v *sessionView) release() {
	v.screen.stopWatching()
	v.screen.clear()
	for _, name := range v.actions.ListActions() {
		coreglib.Destroy(v.actions.LookupAction(name))
	}
	coreglib.Destroy(v.actions)
	coreglib.Destroy(v.im)
	destroyTree(v.page)
	// The framebuffer of a big screen is large; hand it back soon.
	coreglib.TimeoutAdd(200, func() bool {
		debug.FreeOSMemory()
		return false
	})
}

// poll moves what the network goroutines collected onto the main loop. It runs once per frame.
func (v *sessionView) poll() {
	select {
	case <-v.live.done:
		v.connectionLost()
		return
	default:
	}
	if v.pointerHeld && !v.transfer.gesture.Blocked() {
		v.pointerHeld = false
		v.sendPointer(v.pointerX, v.pointerY)
	}
	v.helper.reroute()
	v.pollVideo()
	v.syncPicture()
	if width, height := v.screen.Width(), v.screen.Height(); width != v.lastWidth || height != v.lastHeight {
		v.lastWidth, v.lastHeight = width, height
		if v.mode == rfb.ModeExclusiveControl {
			v.applyGrabs()
		}
	}
	if v.lock.on && v.lockBackend == lockWayland {
		// The picture can move without changing size; the lock follows it.
		v.screen.refreshLock(v.lockBackend)
	}
	if v.lock.held && v.focused && !v.screen.settleLock(v.lockBackend) {
		v.lockLost()
	}
	if cursor, changed := v.h.takeCursor(); changed {
		v.screen.setCursor(cursor)
		v.updateSystemCursor()
	}
	if x, y, moved := v.h.takeCursorMove(); moved && v.lock.held {
		warped := v.rel.resync(x, y, time.Now())
		v.g.app.logger.Debug("mac cursor", "x", x, "y", y, "warped", warped)
		if warped {
			// A Mac app warped its cursor: carry on from there, not from where we thought it was.
			v.pointerX, v.pointerY = x, y
		}
	}
	v.placeCursor()
	if clipboard := v.h.takeClipboard(); clipboard != nil && v.clip != nil {
		err := v.clip.fromMac(clipboard)
		if err != nil {
			v.notify("Clipboard: " + err.Error())
		}
	}
	for _, message := range v.h.takeToasts() {
		v.notify(message)
	}
	drag, changed := v.h.takeDrag()
	switch v.drag.settle(time.Now(), drag, changed) {
	case dragFetch:
		v.transfer.fetch(drag)
	case dragNothing:
		v.notify("The Mac is not dragging any files")
	}
	if state, changed := v.h.takeTransfer(); changed {
		v.showTransfer(state)
	}
	if v.h.layoutChanged() {
		v.layoutArrived()
	}
	if v.layoutSeen {
		v.askVirtualDisplay()
	}
	if time.Since(v.statsAt) >= statsInterval {
		v.statsAt = time.Now()
		v.updateStats()
		if v.copyPulse {
			v.copyBar.Pulse()
		}
	}
}

// layoutArrived reacts to the Mac's display list: the menu, and the one-time
// starting display and virtual display from the preferences.
func (v *sessionView) layoutArrived() {
	v.refreshDisplays()
	if v.layoutSeen {
		return
	}
	v.layoutSeen = true
	prefs := v.g.app.preferences()
	if prefs.MainDisplay && !v.live.req.opts.Explicit["display"] {
		if id, found := mainDisplay(v.h.displayLayout()); found && currentDisplay(v.h.displayLayout()) != id {
			v.report("", v.client.SelectDisplay(id))
		}
	}
}

// askVirtualDisplay creates the virtual display the preferences ask for at the
// size of the window, once, as soon as the window has a size.
func (v *sessionView) askVirtualDisplay() {
	if v.virtualAsked {
		return
	}
	prefs := v.g.app.preferences()
	if !prefs.virtualFollowsWindow(&v.live.req.opts) {
		v.virtualAsked = true
		return
	}
	display, ok := prefs.windowVirtualDisplay(&v.live.req.opts, v.screen.Width(), v.screen.Height())
	if !ok {
		return
	}
	v.virtualAsked = true
	err := v.client.SetVirtualDisplay(display.Width, display.Height, display.Density, rfb.VirtualDisplayLargestSide, rfb.VirtualDisplayLargestSide)
	v.report(fmt.Sprintf("Virtual display %d×%d", display.Width, display.Height), err)
}

// syncPicture copies changed pixels from the framebuffer into the widget's tiles.
// The size is read from the framebuffer inside its lock, so what is copied
// always matches the buffer, even when the Mac resized meanwhile.
func (v *sessionView) syncPicture() {
	// While the video shows, the framebuffer is not updated; just after it stops, the
	// widget keeps the last video picture until the Mac sends a new one.
	tiles := !v.videoActive() && !v.videoWaiting()
	var dirty image.Rectangle
	var full bool
	if tiles {
		dirty, full = v.h.takeDirty()
	}
	v.client.Framebuffer(func(framebuffer *bgra.Image) {
		if framebuffer == nil || framebuffer.Bounds().Empty() {
			return
		}
		resize, area := planPicture(framebuffer.Bounds(), image.Pt(v.screen.width, v.screen.height), dirty, full)
		if resize {
			v.screen.resize(framebuffer.Width, framebuffer.Height)
		}
		if !tiles {
			return
		}
		if !v.screen.update(framebuffer, area) {
			v.g.app.logger.Warn("picture does not fit the widget", "width", framebuffer.Width, "height", framebuffer.Height)
		}
	})
	v.layoutPicture()
}

// viewport is where the picture sits in the widget, in widget units.
func (v *sessionView) viewport() viewport {
	width, height := v.screen.Width(), v.screen.Height()
	frameWidth, frameHeight := v.client.FramebufferSize()
	if v.g.app.preferences().FitWindow {
		return fitViewport(width, height, frameWidth, frameHeight)
	}
	return actualViewport(width, height, frameWidth, frameHeight, float64(v.screen.ScaleFactor()))
}

func (v *sessionView) layoutPicture() {
	v.screen.setRealSize(!v.g.app.preferences().FitWindow)
	v.screen.setViewport(v.viewport())
}

// placeCursor puts the Mac's cursor over the picture where the pointer is.
func (v *sessionView) placeCursor() {
	x, y := v.pointerX, v.pointerY
	if v.mode == rfb.ModeObserve || !v.pointerInside && v.mode != rfb.ModeExclusiveControl {
		x, y = v.h.remoteCursor()
	}
	v.screen.setCursorPosition(x, y, true)
}

// toFramebuffer converts widget coordinates to framebuffer pixels.
func (v *sessionView) toFramebuffer(x, y float64) (int, int, bool) {
	frameWidth, frameHeight := v.client.FramebufferSize()
	return v.viewport().toFramebuffer(float32(x), float32(y), frameWidth, frameHeight)
}

func (v *sessionView) updateStats() {
	now := time.Now()
	drawn := int(v.screen.frames())
	if v.videoActive() {
		drawn = int(v.screen.videoCounters().Presented)
	}
	v.meter.Tick(now, drawn, v.h.updateCount(), v.client.Statistics().WireBytes+v.videoBytes())
	v.fps = v.meter.Frames
	if now.Sub(v.loggedAt) >= time.Second {
		v.loggedAt = now
		v.g.app.logger.Debug("stats", "drawn_fps", int(v.meter.Frames), "update_fps", int(v.meter.Updates), "mbit", int(v.meter.BitsPS/1e6))
	}
	delayMS := float64(v.live.echo.Value()) / float64(time.Millisecond)
	v.identity.SetTooltipText(connectedText(v.fps))
	if delayMS > 0 {
		v.delay.SetLabel(fmt.Sprintf("%.0f ms", delayMS))
		v.noteDelay(now, delayMS)
	}
	if !v.showStats {
		return
	}
	frameWidth, frameHeight := v.client.FramebufferSize()
	rows := statRows(v.client.Info(), v.meter.Frames, delayMS, v.meter.BitsPS, v.h.lastEncoding(), frameWidth, frameHeight)
	rows = applyVideoRows(rows, v.videoStatistics())
	for len(v.statNames) < len(rows) {
		name := gtk.NewLabel("")
		name.AddCSSClass("name")
		name.SetXAlign(0)
		name.SetHExpand(true)
		value := gtk.NewLabel("")
		value.AddCSSClass("value")
		value.SetXAlign(1)
		row := len(v.statNames)
		v.statGrid.Attach(name, 0, row, 1, 1)
		v.statGrid.Attach(value, 1, row, 1, 1)
		v.statNames = append(v.statNames, name)
		v.statValues = append(v.statValues, value)
	}
	for index := range v.statNames {
		visible := index < len(rows)
		v.statNames[index].SetVisible(visible)
		v.statValues[index].SetVisible(visible)
		if visible {
			v.statNames[index].SetLabel(rows[index].Label)
			v.statValues[index].SetLabel(rows[index].Value)
		}
	}
}

// copyDoneShown is how long the card says a copy is finished before it goes.
const copyDoneShown = 2500

// showTransfer is the one indicator of a file copy or drop: it says what is
// prepared, how far the copy is and, for a moment, that it is done.
func (v *sessionView) showTransfer(state transferState) {
	if v.copyHide != 0 {
		coreglib.SourceRemove(v.copyHide)
		v.copyHide = 0
	}
	v.copyCard.SetVisible(state.Active)
	v.copyPulse = state.Active && state.Phase == phasePreparing
	if !state.Active {
		return
	}
	v.copyTitle.SetLabel(state.title())
	v.copyValue.SetLabel(state.value())
	v.copyCancel.SetVisible(state.Phase != phaseDone)
	switch state.Phase {
	case phasePreparing:
		v.copyBar.Pulse()
	case phaseDone:
		v.copyBar.SetFraction(1)
		v.copyHide = coreglib.TimeoutAdd(copyDoneShown, func() bool {
			v.copyHide = 0
			v.copyCard.SetVisible(false)
			return false
		})
	default:
		v.copyBar.SetFraction(float64(state.Percent) / 100)
	}
}

// screenChanged runs when the window comes to a screen. HDR is decided when a session connects, so a
// screen of the other kind is only told to the person, with the reconnect it needs.
func (v *sessionView) screenChanged() {
	app := v.g.app
	notice := hdrMoveNotice(v.hdr, app.hdrWanted(), app.refreshHDROutput())
	if notice != "" && notice != v.hdrNotice {
		v.notify(notice)
	}
	v.hdrNotice = notice
}

func (v *sessionView) notify(message string) {
	v.g.toast(message)
}

func (v *sessionView) report(message string, err error) {
	if err != nil {
		v.notify(err.Error())
		return
	}
	if message != "" {
		v.notify(message)
	}
}

// connectionLost handles a connection that ended on its own.
func (v *sessionView) connectionLost() {
	if v.ended {
		return
	}
	err := v.live.runErr
	address := v.live.req.target.Address()
	message := "The connection to " + address + " ended."
	if err != nil {
		_, message = describeError(err, address, v.live.req.vncLogin())
	}
	req := v.live.req
	v.captureLast()
	v.g.nav.Pop()
	dialog := adw.NewAlertDialog("Disconnected", message)
	dialog.AddResponse("close", "Close")
	dialog.AddResponse("reconnect", "Reconnect")
	dialog.SetResponseAppearance("reconnect", adw.ResponseSuggested)
	dialog.SetDefaultResponse("reconnect")
	dialog.SetCloseResponse("close")
	dialog.ConnectResponse(func(response string) {
		if response == "reconnect" {
			v.g.openSignIn(req, req.dialPassword() != "")
		}
	})
	dialog.Present(v.g.window)
}

// ---- modes, grabs and the cursor ----

func (v *sessionView) controlling() bool {
	return v.mode != rfb.ModeObserve
}

// applyMode switches between observing, controlling and exclusive control.
func (v *sessionView) applyMode(mode rfb.Mode, announce bool) {
	if allowed := allowedMode(mode, v.client.Info().Flags); allowed != mode {
		mode = allowed
		v.notify("The Mac allows viewing only")
	}
	v.mode = mode
	if mode == rfb.ModeObserve {
		v.releaseInput()
	}
	err := v.client.SetMode(mode)
	if err != nil {
		v.notify("Mode: " + err.Error())
	}
	if v.modes.ActiveName() != modeID(mode) {
		v.modes.SetActiveName(modeID(mode))
	}
	v.applyGrabs()
	v.updateSystemCursor()
	v.syncModeButton()
	v.showExclusiveHint(mode == rfb.ModeExclusiveControl)
	if announce {
		if mode == rfb.ModeExclusiveControl {
			if text := exclusiveAnnouncement(v.lockBackend); text != "" {
				v.notify(text)
			}
		} else {
			v.notify("Mode: " + mode.String())
		}
	}
	if mode != rfb.ModeObserve {
		v.screen.GrabFocus()
	}
}

// applyGrabs hands the system's shortcuts to the window while controlling, and
// in exclusive mode also locks the pointer to it (see setPointerLock). Where the
// display server has no way to hold the pointer only the shortcuts are taken;
// Ctrl+Alt+Esc works either way.
func (v *sessionView) applyGrabs() {
	grab := v.focused && v.controlling() && !v.g.app.opts.NoGrab
	v.screen.inhibitShortcuts(grab)
	if v.mode != rfb.ModeExclusiveControl {
		v.lock.leaveExclusive()
	}
	exclusive := v.focused && v.mode == rfb.ModeExclusiveControl && !v.g.app.opts.NoGrab
	v.setPointerLock(exclusive)
	v.helper.sync()
}

// setPointerLock holds the pointer in the window (on) or lets it go. Every way
// out of Exclusive mode, focus loss, unmap and the end of the session come here.
func (v *sessionView) setPointerLock(on bool) {
	if on == v.lock.on {
		if on && !v.screen.refreshLock(v.lockBackend) {
			v.lockLost()
		}
		return
	}
	if !on {
		v.lock.release()
		lockSink.motion, lockSink.state = nil, nil
		v.helper.reroute()
		hint, x, y := v.unlockHint()
		trips, ticks := x11Cost()
		v.g.app.logger.Debug("pointer lock released", "round_trips", trips, "ticks", ticks, "backend", int(v.lockBackend), "hint", hint, "x", x, "y", y, "mac_x", v.pointerX, "mac_y", v.pointerY)
		if v.screen.unlockPointer(v.lockBackend, hint, x, y) {
			v.warp.arm(x, y, time.Now())
		}
		return
	}
	v.warp.disarm()
	if !v.lockBackend.holds() {
		return
	}
	// The running total starts where the Mac's pointer is believed to be, so
	// locking itself moves nothing.
	reports := v.h.cursorReports()
	rx, ry := v.h.remoteCursor()
	width, height := v.client.FramebufferSize()
	x, y := lockStart(reports > 0, rx, ry, reports > v.sentReports, v.sent, v.pointerX, v.pointerY, width, height)
	v.rel.set(x, y)
	v.pointerX, v.pointerY = x, y
	if v.lockBackend.relative() {
		lockSink.motion = v.onLockedMotion
		lockSink.state = v.onLockState
	}
	if v.lockBackend == lockWayland && !v.overScreen {
		v.lock.wait()
		v.g.app.logger.Debug("pointer lock waits for the pointer to reach the picture")
		return
	}
	held, requested := v.screen.lockPointer(v.lockBackend)
	if !requested && !held {
		lockSink.motion, lockSink.state = nil, nil
		if v.lock.failedOnce() {
			v.notify(lockFailedAnnouncement)
		}
		return
	}
	v.lock.request(held)
	v.g.app.logger.Debug("pointer lock requested", "backend", int(v.lockBackend), "held", held)
}

// lockLost ends a lock that stood and cannot any more (the window is gone, or
// its barriers could not be rebuilt). The pointer is let go and only the
// keyboard stays captured, as when a lock fails to start.
func (v *sessionView) lockLost() {
	notify := v.lock.lose()
	lockSink.motion, lockSink.state = nil, nil
	v.helper.reroute()
	v.screen.unlockPointer(v.lockBackend, false, 0, 0)
	v.g.app.logger.Debug("pointer lock lost", "backend", int(v.lockBackend))
	if notify {
		v.notify(lockFailedAnnouncement)
	}
}

// requestIfIdle asks for the lock when it is wanted and no request stands: the
// compositor ended the last one while the window kept the focus, or Exclusive
// began with the pointer outside the picture (on the header's toggle). The lock
// is oneshot, so nothing else would bring it back; the pointer moving over the
// picture is the cue that the user is back at work, and the compositor then
// begins the lock at once.
func (v *sessionView) requestIfIdle() {
	if !v.lock.wantsRequest() {
		return
	}
	held, requested := v.screen.lockPointer(v.lockBackend)
	v.g.app.logger.Debug("pointer lock requested from idle", "backend", int(v.lockBackend), "requested", requested)
	if held || requested {
		v.lock.request(held)
	}
}

// unlockHint is where the pointer reappears when the lock ends: over the Mac's
// cursor, so the local pointer does not jump away from it.
func (v *sessionView) unlockHint() (bool, float64, float64) {
	width, height := v.client.FramebufferSize()
	port := v.viewport()
	if port.empty() || width <= 0 || height <= 0 {
		return false, 0, 0
	}
	x, y := port.toWindow(float64(v.pointerX)+0.5, float64(v.pointerY)+0.5, width, height)
	return true, float64(x), float64(y)
}

// onLockState is the compositor's answer to the lock request.
func (v *sessionView) onLockState(locked bool) {
	notice := v.lock.state(locked)
	v.helper.reroute()
	v.g.app.logger.Debug("pointer lock", "locked", locked, "notice", int(notice))
	switch notice {
	case lockNoticeLocked:
		// The compositor may begin the lock well after the request (the pointer
		// has to be over the picture first), and until then the pointer's own
		// positions went to the Mac: the running total continues from there.
		v.rel.set(v.pointerX, v.pointerY)
		v.notify("Pointer locked, Ctrl+Alt+Esc releases it")
	case lockNoticeEnded:
		v.notify("Pointer released by the system")
	}
}

// onLockedMotion adds a motion of the locked pointer to the Mac's pointer. The
// deltas arrive in window units and are scaled to the Mac's screen, with no edge
// of the window to stop them; the total stays on the Mac's screen.
func (v *sessionView) onLockedMotion(dx, dy float64) {
	if !v.lock.held || !v.controlling() || !finiteMotion(dx, dy) {
		return
	}
	if v.helper.routed() {
		v.helper.motion(dx, dy)
		return
	}
	width, height := v.client.FramebufferSize()
	x, y := v.rel.move(float32(dx), float32(dy), v.viewport(), width, height)
	if x == v.pointerX && y == v.pointerY {
		return
	}
	v.rel.sent(x, y, time.Now())
	v.sendPointer(x, y)
}

// updateSystemCursor hides the local arrow while the Mac's own cursor is drawn over the picture.
func (v *sessionView) updateSystemCursor() {
	if v.controlling() && (v.pointerInside || v.mode == rfb.ModeExclusiveControl) {
		v.screen.SetCursorFromName("none")
		return
	}
	v.screen.SetCursorFromName("default")
}

// releaseInput lifts every key and button the Mac thinks is down.
func (v *sessionView) releaseInput() {
	v.helper.releaseButtons()
	for scancode, key := range v.held {
		_ = v.client.SendKeyCode(key.Code, v.keyboard.Type(), key.Keysym, false)
		delete(v.held, scancode)
	}
	if v.buttons != 0 {
		v.buttons = 0
		_ = v.client.SendPointer(v.pointerX, v.pointerY, 0, 0)
	}
}

// ---- input ----

func (v *sessionView) installInput() {
	keys := gtk.NewEventControllerKey()
	keys.SetIMContext(v.im)
	keys.ConnectKeyPressed(func(keyval, keycode uint, state gdk.ModifierType) bool {
		return v.onKey(keys, keyval, keycode, state, true)
	})
	keys.ConnectKeyReleased(func(keyval, keycode uint, state gdk.ModifierType) {
		v.onKey(keys, keyval, keycode, state, false)
	})
	v.screen.AddController(keys)
	v.im.ConnectCommit(v.onText)

	motion := gtk.NewEventControllerMotion()
	motion.ConnectMotion(v.onMotion)
	motion.ConnectEnter(v.onMotion)
	motion.ConnectLeave(func() {
		v.overScreen = false
		v.pointerInside = false
		v.updateSystemCursor()
	})
	v.screen.AddController(motion)

	click := gtk.NewGestureClick()
	click.SetButton(0)
	click.ConnectPressed(func(presses int, x, y float64) { v.onButton(&click.GestureSingle, presses, x, y, true) })
	click.ConnectReleased(func(_ int, x, y float64) { v.onButton(&click.GestureSingle, 0, x, y, false) })
	click.ConnectUnpairedRelease(func(x, y float64, button uint, _ *gdk.EventSequence) {
		v.releaseButton(uint8(button), click.CurrentEventTime())
	})
	v.screen.AddController(click)

	scroll := gtk.NewEventControllerScroll(gtk.EventControllerScrollBothAxes)
	scroll.ConnectScroll(v.onScroll)
	v.screen.AddController(scroll)

	focus := gtk.NewEventControllerFocus()
	focus.ConnectEnter(func() {
		v.focused = true
		v.im.FocusIn()
		v.applyGrabs()
		if v.clip != nil && v.controlling() {
			v.clip.toMac()
		}
	})
	focus.ConnectLeave(func() {
		v.focused = false
		v.im.FocusOut()
		v.releaseInput()
		v.screen.inhibitShortcuts(false)
		v.setPointerLock(false)
	})
	v.screen.AddController(focus)
	// A window that is hidden or loses the focus never keeps the pointer.
	v.screen.ConnectUnmap(func() { v.setPointerLock(false) })
	active := v.g.window.NotifyProperty("is-active", func() {
		if !v.g.window.IsActive() {
			v.setPointerLock(false)
		}
	})
	v.outliving.add(v.g.window, active)
	if native := v.g.window.Surface(); native != nil {
		surface := gdk.BaseSurface(native)
		v.outliving.add(surface, surface.ConnectEnterMonitor(func(*gdk.Monitor) { v.screenChanged() }))
	}

	drop := gtk.NewDropTarget(gdk.GTypeFileList, gdk.ActionCopy)
	drop.ConnectEnter(func(x, y float64) gdk.DragAction { return v.dropHover(x, y) })
	drop.ConnectMotion(func(x, y float64) gdk.DragAction { return v.dropHover(x, y) })
	drop.ConnectDrop(func(value *coreglib.Value, x, y float64) bool { return v.onDrop(value, x, y) })
	v.screen.AddController(drop)

	v.modes.NotifyProperty("active-name", func() {
		mode := modeFromID(v.modes.ActiveName())
		if mode != v.mode {
			v.applyMode(mode, false)
		}
	})
	v.screen.SetFocusOnClick(true)
}

// modifiers folds GDK's modifier state into the viewer's own bits.
func modifiers(state gdk.ModifierType) uint16 {
	var mod uint16
	if state&gdk.ShiftMask != 0 {
		mod |= modShift
	}
	if state&gdk.ControlMask != 0 {
		mod |= modCtrl
	}
	if state&gdk.AltMask != 0 {
		mod |= modAlt
	}
	if state&(gdk.SuperMask|gdk.MetaMask) != 0 {
		mod |= modGUI
	}
	return mod
}

// keyRuneFor is the character a key types at its base level, which is what the
// Mac gets as the key's keysym. A key on the third level (AltGr) keeps the
// character it produced.
func (v *sessionView) keyRuneFor(controller *gtk.EventControllerKey, keyval, keycode uint, state gdk.ModifierType) rune {
	if state&modAltGr != 0 {
		return rune(gdk.KeyvalToUnicode(keyval))
	}
	base, _, _, _, ok := v.screen.Display().TranslateKey(keycode, 0, int(controller.Group()))
	if !ok {
		base = gdk.KeyvalToLower(keyval)
	}
	return rune(gdk.KeyvalToUnicode(base))
}

func (v *sessionView) onKey(controller *gtk.EventControllerKey, keyval, keycode uint, state gdk.ModifierType, down bool) bool {
	scancode := hidFromKeycode(uint32(keycode))
	if scancode == 0 {
		return false
	}
	mod := modifiers(state)
	if down {
		v.text.Reset()
		if command := chordCommand(scancode, mod); command != commandNone && (command != commandReleaseExclusive || v.mode == rfb.ModeExclusiveControl) {
			if !v.swallow[scancode] {
				v.swallow[scancode] = true
				v.runCommand(command)
			}
			return true
		}
	} else if v.swallow[scancode] {
		delete(v.swallow, scancode)
		return true
	}
	if !v.controlling() {
		return false
	}
	scancode = remapCommandKey(scancode, v.g.app.preferences().CommandKey)
	v.keyboard.Learn(scancode)
	mapped, found := mapKey(scancode, v.keyRuneFor(controller, keyval, keycode, state), mod&modShift != 0, v.keyboard.Type())
	if !found {
		return false
	}
	if down {
		v.held[scancode] = mapped
		v.text.KeyDown(scancode, mod)
	} else {
		if _, pressed := v.held[scancode]; !pressed {
			return true
		}
		delete(v.held, scancode)
	}
	v.live.echo.Sent(time.Now())
	err := v.client.SendKeyCode(mapped.Code, v.keyboard.Type(), mapped.Keysym, down)
	if err != nil {
		v.g.app.logger.Debug("key", "error", err)
	}
	return true
}

func (v *sessionView) onText(text string) {
	if !v.controlling() || v.text.Consume(text) {
		return
	}
	err := v.client.SendText(text)
	if err != nil {
		v.g.app.logger.Debug("text", "error", err)
	}
}

func (v *sessionView) sendPointer(x, y int) {
	v.pointerX, v.pointerY = x, y
	if v.transfer.gesture.Blocked() {
		// A drop is playing its own presses. Send this position when it is done.
		v.pointerHeld = true
		return
	}
	v.sent, v.sentReports = true, v.h.cursorReports()
	v.pointerSends++
	v.live.echo.Sent(time.Now())
	err := v.client.SendPointer(x, y, v.buttons, v.clickCount)
	if err != nil {
		v.g.app.logger.Debug("pointer", "error", err)
	}
}

func (v *sessionView) onMotion(x, y float64) {
	v.overScreen = true
	if !v.controlling() {
		return
	}
	if v.lock.held && v.lockBackend.relative() {
		// The locked pointer moves by deltas; its own position means nothing.
		return
	}
	v.requestIfIdle()
	fx, fy, inside := v.toFramebuffer(x, y)
	v.pointerInside = inside
	v.updateSystemCursor()
	if motionSends(&v.warp, x, y, time.Now(), inside, v.buttons != 0, v.mode == rfb.ModeExclusiveControl) {
		v.sendPointer(fx, fy)
	}
}

func buttonBit(button uint) uint8 {
	switch button {
	case 1:
		return rfb.ButtonLeft
	case 2:
		return rfb.ButtonMiddle
	case 3:
		return rfb.ButtonRight
	}
	return 0
}

func (v *sessionView) onButton(gesture *gtk.GestureSingle, presses int, x, y float64, down bool) {
	if !v.controlling() {
		return
	}
	button := gesture.CurrentButton()
	bit := buttonBit(button)
	if bit == 0 {
		return
	}
	at := uint64(gesture.CurrentEventTime()) * uint64(time.Millisecond)
	if !v.clicks.allow(uint8(button), down, at) {
		return
	}
	if down {
		v.screen.GrabFocus()
		v.requestIfIdle()
		if v.helper.button(bit, true) {
			return
		}
		fx, fy, inside := v.toFramebuffer(x, y)
		fx, fy, inside = clickTarget(&v.warp, x, y, time.Now(), v.mode == rfb.ModeExclusiveControl, v.lock.held, v.pointerX, v.pointerY, fx, fy, inside)
		if !inside {
			return
		}
		v.buttons |= bit
		v.clickCount = presses
		v.sendPointer(fx, fy)
		return
	}
	v.releaseButton(uint8(button), gesture.CurrentEventTime())
}

func (v *sessionView) releaseButton(button uint8, _ uint32) {
	bit := buttonBit(uint(button))
	if bit != 0 && v.helper.button(bit, false) {
		return
	}
	if bit == 0 || v.buttons&bit == 0 {
		return
	}
	v.buttons &^= bit
	v.sendPointer(v.pointerX, v.pointerY)
	if v.buttons == 0 {
		v.clickCount = 0
	}
}

func (v *sessionView) onScroll(dx, dy float64) bool {
	if !v.controlling() {
		return false
	}
	px, py := gtkScrollPoints(v.client.Info(), dx, dy, v.g.app.preferences().NaturalScrolling)
	if v.helper.routed() {
		v.helper.scroll(px, py)
		return true
	}
	v.pointerSends++
	err := v.client.SendScroll(v.pointerX, v.pointerY, px, py)
	if err != nil {
		v.g.app.logger.Debug("scroll", "error", err)
	}
	return true
}

// dropHover keeps the Mac's pointer under a file drag, so the Mac can pick
// the folder the files would land in.
func (v *sessionView) dropHover(x, y float64) gdk.DragAction {
	if !v.controlling() {
		return 0
	}
	fx, fy, inside := v.toFramebuffer(x, y)
	if inside {
		v.pointerX, v.pointerY = fx, fy
		v.pointerInside = true
		hoverPointer(&v.transfer.gesture, plainPointer(v.client), fx, fy)
	}
	return gdk.ActionCopy
}

func (v *sessionView) onDrop(value *coreglib.Value, x, y float64) bool {
	list, ok := value.GoValue().(*gdk.FileList)
	if !ok || list == nil {
		return false
	}
	var paths []string
	for _, file := range list.Files() {
		if path := file.Path(); path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return false
	}
	if !v.controlling() {
		v.notify("Switch to control mode to send files")
		return false
	}
	fx, fy, _ := v.toFramebuffer(x, y)
	v.transfer.drop(paths, fx, fy)
	return true
}

// chooseFilesToSend sends files picked in a file chooser, dropped at the Mac's pointer.
func (v *sessionView) chooseFilesToSend() {
	if !v.controlling() {
		v.notify("Switch to control mode to send files")
		return
	}
	dialog := gtk.NewFileDialog()
	dialog.SetTitle("Send files to the Mac")
	dialog.OpenMultiple(v.g.ctx, &v.g.window.Window, func(result gio.AsyncResulter) {
		files, err := dialog.OpenMultipleFinish(result)
		if err != nil {
			return
		}
		var paths []string
		for index := uint(0); index < files.NItems(); index++ {
			file, ok := files.Item(index).Cast().(gio.Filer)
			if ok {
				paths = append(paths, file.Path())
			}
		}
		if len(paths) == 0 {
			return
		}
		frameWidth, frameHeight := v.client.FramebufferSize()
		x, y := v.pointerX, v.pointerY
		if x == 0 && y == 0 {
			x, y = frameWidth/2, frameHeight/2
		}
		v.transfer.drop(paths, x, y)
	})
}

// ---- commands ----

func (v *sessionView) runCommand(command command) {
	switch command {
	case commandQuit:
		v.g.window.Close()
	case commandReleaseExclusive:
		if v.mode == rfb.ModeExclusiveControl {
			v.applyMode(rfb.ModeControl, true)
		}
	case commandCycleMode:
		v.applyMode(rfb.Mode(nextCycle(int(v.mode), int(rfb.ModeExclusiveControl)+1)), true)
	case commandToggleStats:
		v.setStats(!v.showStats)
	case commandFullscreen:
		v.toggleFullscreen()
	case commandCycleQuality:
		index := 0
		for position, quality := range qualities {
			if quality == v.quality {
				index = position
			}
		}
		v.setQuality(qualities[nextCycle(index, len(qualities))])
	case commandCycleDisplay:
		v.cycleDisplay()
	case commandVirtualDisplay:
		width, height := v.screen.Width(), v.screen.Height()
		density := 1.0
		if v.screen.ScaleFactor() >= 2 {
			density = 2
		}
		err := v.client.SetVirtualDisplay(width, height, density, rfb.VirtualDisplayLargestSide, rfb.VirtualDisplayLargestSide)
		v.report(fmt.Sprintf("Virtual display %d×%d", width, height), err)
	case commandToggleHDR:
		v.toggleHDR()
	case commandMuteSound:
		v.setMuted(!v.muted)
	case commandRefresh:
		v.report("Refreshing", v.client.RequestRefresh())
	case commandTypeClipboard:
		v.typeClipboard()
	case commandFetchDrag:
		v.drag.ask(time.Now())
		v.report("Asking the Mac for the files it is dragging", v.client.RequestDrag())
	case commandScreenshot:
		path, err := v.saveScreenshot()
		v.report("Saved "+path, err)
	}
}

func (v *sessionView) typeClipboard() {
	display := v.screen.Display()
	display.Clipboard().ReadTextAsync(v.g.ctx, func(result gio.AsyncResulter) {
		text, err := display.Clipboard().ReadTextFinish(result)
		if err != nil || text == "" {
			v.notify("The local clipboard has no text")
			return
		}
		v.report("Typed the clipboard", v.client.PasteText(text))
	})
}

func (v *sessionView) cycleDisplay() {
	choices := displayChoices(v.h.displayLayout())
	if len(choices) < 2 {
		v.notify("The Mac reported no displays")
		return
	}
	current := 0
	for index, choice := range choices {
		if choice.ID == currentDisplay(v.h.displayLayout()) {
			current = index
		}
	}
	next := choices[nextCycle(current, len(choices))]
	v.selectDisplay(next.Label, next.ID)
	v.notify("Showing " + next.Label)
}

// saveScreenshot writes the Mac's whole picture, at its real size, to a PNG file.
func (v *sessionView) saveScreenshot() (string, error) {
	directory := v.g.app.opts.ScreenshotDir
	if directory == "" {
		directory = v.g.app.preferences().downloadFolder()
	}
	path := filepath.Join(directory, "sharelinux-"+time.Now().Format("20060102-150405")+".png")
	var failure error
	v.client.Framebuffer(func(framebuffer *bgra.Image) {
		if framebuffer == nil {
			failure = E.New("there is no picture yet")
			return
		}
		picture := image.NewNRGBA(framebuffer.Bounds())
		for y := range framebuffer.Height {
			row := framebuffer.Pix[y*framebuffer.Stride:]
			for x := range framebuffer.Width {
				picture.Pix[y*picture.Stride+x*4] = row[x*4+2]
				picture.Pix[y*picture.Stride+x*4+1] = row[x*4+1]
				picture.Pix[y*picture.Stride+x*4+2] = row[x*4]
				picture.Pix[y*picture.Stride+x*4+3] = 255
			}
		}
		file, err := os.Create(path)
		if err != nil {
			failure = err
			return
		}
		err = png.Encode(file, picture)
		failure = E.Errors(err, file.Close())
	})
	return path, failure
}

// ---- fullscreen ----

func (v *sessionView) toggleFullscreen() {
	if v.g.window.IsFullscreen() {
		v.g.window.Unfullscreen()
	} else {
		v.g.window.Fullscreen()
	}
}

// installFullscreenReveal hides the window controls and the floating bar in
// fullscreen and brings the bar back when the pointer touches the top edge.
func (v *sessionView) installFullscreenReveal() {
	fullscreened := v.g.window.NotifyProperty("fullscreened", func() {
		v.fullscreen = v.g.window.IsFullscreen()
		v.toolbar.SetExtendContentToTopEdge(v.fullscreen)
		v.toolbar.SetRevealTopBars(!v.fullscreen)
		v.bar.SetRevealChild(!v.fullscreen)
	})
	v.outliving.add(v.g.window, fullscreened)
	motion := gtk.NewEventControllerMotion()
	motion.SetPropagationPhase(gtk.PhaseCapture)
	motion.ConnectMotion(func(x, y float64) {
		if !v.fullscreen {
			return
		}
		if y <= revealZone {
			v.cancelHide()
			v.bar.SetRevealChild(true)
			return
		}
		if y > 80 && v.bar.RevealChild() {
			v.scheduleHide()
		}
	})
	v.toolbar.AddController(motion)
}

func (v *sessionView) menuOpen() bool {
	return v.displayButton.Active() || v.sessionMenu.Active()
}

func (v *sessionView) cancelHide() {
	if v.hideTimer != 0 {
		coreglib.SourceRemove(v.hideTimer)
		v.hideTimer = 0
	}
}

func (v *sessionView) scheduleHide() {
	if v.hideTimer != 0 {
		return
	}
	v.hideTimer = coreglib.TimeoutAdd(900, func() bool {
		v.hideTimer = 0
		if v.fullscreen && !v.menuOpen() {
			v.bar.SetRevealChild(false)
		}
		return false
	})
}

// captureLast keeps what the home screen shows of this Mac: how the last
// session went and a small copy of the picture as it is on screen now.
func (v *sessionView) captureLast() {
	if v.ended {
		return
	}
	target := v.live.req.target
	width, height := v.client.FramebufferSize()
	summary := summarizeSession(width, height, v.videoStatistics(), v.h.lastEncoding(), v.muted)
	saved, err := v.g.app.store.Update(target, func(c *savedConnection) { c.Last = &summary })
	if err != nil {
		v.g.app.logger.Debug("note the last session", "error", err)
	}
	path := v.g.thumbnailFile(target.ID())
	if path == "" {
		return
	}
	if thumbnailAllowed(saved, path) {
		v.saveThumbnail(target, path)
	}
}

// thumbnailWidth is the width of the kept last frame, in pixels.
const thumbnailWidth = 640

// saveThumbnail keeps the picture widget as it is drawn now, scaled down, as a PNG
// file. The snapshot is taken here, on the main thread; encoding and writing run
// on another goroutine so the window never stalls on them.
func (v *sessionView) saveThumbnail(t target, path string) {
	if !v.screen.Mapped() {
		return
	}
	if !v.thumbBusy.CompareAndSwap(false, true) {
		return
	}
	snapshot := v.screen.snapshot(thumbnailWidth)
	if snapshot == nil {
		v.thumbBusy.Store(false)
		return
	}
	store, logger := v.g.app.store, v.g.app.logger
	go func() {
		defer v.thumbBusy.Store(false)
		data := snapshot.png()
		snapshot.free()
		if data == nil {
			logger.Debug("keep the last frame", "error", "the picture could not be encoded")
			return
		}
		if err := writeThumbnail(path, data); err != nil {
			logger.Debug("keep the last frame", "error", err)
			return
		}
		// The Mac may have been removed while this ran: keep nothing of it.
		if _, found := store.Lookup(t); !found {
			removeThumbnail(path)
		}
	}()
}

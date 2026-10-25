//go:build linux

package main

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const (
	probeEvery = 10 * time.Second
	scanEvery  = 25 * time.Second
)

// The views of the sidebar that show Macs.
const (
	viewMacs   = "macs"
	viewRecent = "recent"
)

// rowStatus are the widgets of a Mac's status that change without a rebuild.
type rowStatus struct {
	dot   *gtk.Box
	label *gtk.Label
	// extra follows the state word: "Online · extra".
	extra string
}

// connectionsPage is the first screen: a sidebar, and next to it the Macs: the
// one used last, the saved ones and the ones found on the network.
type connectionsPage struct {
	g     *gui
	page  *adw.NavigationPage
	split *adw.NavigationSplitView

	nav        *gtk.ListBox
	navRows    map[string]*gtk.ListBoxRow
	macsCount  *gtk.Label
	view       string
	helperDot  *gtk.Box
	helperName *gtk.Label
	helperBody *gtk.Label

	title       *gtk.Label
	searchEntry *gtk.SearchEntry
	stack       *gtk.Stack
	hero        *gtk.Box
	gridTitle   *gtk.Label
	grid        *gtk.FlowBox
	nothingHere *gtk.Label
	scanSpinner *gtk.Spinner
	hint        *gtk.Box

	statuses  map[string][]rowStatus
	saved     []savedConnection
	found     []discoveredMac
	reachable map[string]reachability

	cancel context.CancelFunc
}

func newConnectionsPage(g *gui) *connectionsPage {
	p := &connectionsPage{
		g:         g,
		view:      viewMacs,
		navRows:   map[string]*gtk.ListBoxRow{},
		statuses:  map[string][]rowStatus{},
		reachable: map[string]reachability{},
	}

	p.split = adw.NewNavigationSplitView()
	p.split.SetMinSidebarWidth(232)
	p.split.SetMaxSidebarWidth(260)
	p.split.SetSidebarWidthFraction(0.22)
	p.split.SetShowContent(true)
	p.split.SetSidebar(adw.NewNavigationPageWithTag(p.buildSidebar(), windowName, "sidebar"))
	p.split.SetContent(adw.NewNavigationPageWithTag(p.buildContent(), "Your Macs", "macs"))

	p.page = adw.NewNavigationPageWithTag(p.split, windowName, "connections")
	return p
}

// installBreakpoint folds the sidebar away on a narrow window.
func (p *connectionsPage) installBreakpoint() {
	breakpoint := adw.NewBreakpoint(adw.BreakpointConditionParse("max-width: 640sp"))
	breakpoint.AddSetter(p.split, "collapsed", true)
	p.g.window.AddBreakpoint(breakpoint)
}

// ---- sidebar ----

func (p *connectionsPage) buildSidebar() gtk.Widgetter {
	brandText := gtk.NewBox(gtk.OrientationVertical, 0)
	brandText.SetVAlign(gtk.AlignCenter)
	name := gtk.NewLabel(windowName)
	name.SetXAlign(0)
	name.AddCSSClass("oss-brand-name")
	tag := gtk.NewLabel("Mac screen sharing")
	tag.SetXAlign(0)
	tag.AddCSSClass("oss-brand-tag")
	brandText.Append(name)
	brandText.Append(tag)
	brand := gtk.NewBox(gtk.OrientationHorizontal, 12)
	brand.SetMarginStart(8)
	brand.SetMarginBottom(20)
	brand.SetMarginTop(4)
	logo := gtk.NewImageFromIconName(applicationID)
	logo.SetPixelSize(36)
	brand.Append(logo)
	brand.Append(brandText)
	handle := gtk.NewWindowHandle()
	handle.SetChild(brand)

	p.nav = gtk.NewListBox()
	p.nav.AddCSSClass("oss-nav")
	p.nav.SetSelectionMode(gtk.SelectionBrowse)
	p.nav.SetActivateOnSingleClick(true)
	macs := p.navRow(viewMacs, iconMacs, "Macs", true)
	p.navRow(viewRecent, iconRecent, "Recent sessions", false)
	p.navRow("mouse-helper", iconMouseHelper, "Mouse helper", false)
	p.navRow("preferences", iconPreferences, "Preferences", false)
	p.nav.SelectRow(macs)
	p.nav.ConnectRowActivated(func(row *gtk.ListBoxRow) { p.navActivated(row.Name()) })

	p.helperDot = gtk.NewBox(gtk.OrientationHorizontal, 0)
	p.helperDot.AddCSSClass("oss-dot")
	p.helperDot.SetVAlign(gtk.AlignCenter)
	p.helperName = gtk.NewLabel("Mouse helper")
	p.helperName.AddCSSClass("oss-helper-title")
	p.helperName.SetXAlign(0)
	p.helperName.SetHExpand(true)
	titleRow := gtk.NewBox(gtk.OrientationHorizontal, 8)
	titleRow.Append(p.helperDot)
	titleRow.Append(p.helperName)
	p.helperBody = gtk.NewLabel("")
	p.helperBody.AddCSSClass("oss-helper-body")
	p.helperBody.SetWrap(true)
	p.helperBody.SetXAlign(0)
	p.helperBody.SetMaxWidthChars(24)
	card := gtk.NewBox(gtk.OrientationVertical, 8)
	card.AddCSSClass("oss-helper-card")
	card.Append(titleRow)
	card.Append(p.helperBody)
	card.SetTooltipText("Open the mouse helper settings")
	click := gtk.NewGestureClick()
	click.ConnectReleased(func(int, float64, float64) { p.g.showPreferencesPage("keyboard") })
	card.AddController(click)

	column := gtk.NewBox(gtk.OrientationVertical, 6)
	column.AddCSSClass("oss-sidebar")
	column.Append(handle)
	column.Append(p.nav)
	spacer := gtk.NewBox(gtk.OrientationVertical, 0)
	spacer.SetVExpand(true)
	column.Append(spacer)
	column.Append(card)
	return column
}

// navRow adds one row to the sidebar list.
func (p *connectionsPage) navRow(id, iconName, text string, counted bool) *gtk.ListBoxRow {
	box := gtk.NewBox(gtk.OrientationHorizontal, 12)
	box.Append(icon(iconName, 20))
	label := gtk.NewLabel(text)
	label.SetXAlign(0)
	label.SetHExpand(true)
	box.Append(label)
	if counted {
		p.macsCount = gtk.NewLabel("0")
		p.macsCount.AddCSSClass("oss-count")
		p.macsCount.SetVAlign(gtk.AlignCenter)
		box.Append(p.macsCount)
	}
	row := gtk.NewListBoxRow()
	row.SetName(id)
	row.SetChild(box)
	p.nav.Append(row)
	p.navRows[id] = row
	return row
}

// navActivated runs a sidebar choice. The first two change what the page shows;
// the other two open a window of settings and leave the page as it was.
func (p *connectionsPage) navActivated(id string) {
	switch id {
	case viewMacs, viewRecent:
		p.view = id
		p.split.SetShowContent(true)
		p.rebuild()
	case "mouse-helper":
		p.nav.SelectRow(p.navRows[p.view])
		p.g.showPreferencesPage("keyboard")
	case "preferences":
		p.nav.SelectRow(p.navRows[p.view])
		p.g.showPreferencesPage("general")
	}
}

// refreshHelperCard reads the paired Macs off the main thread (the keyring can
// ask for an unlock) and redraws the card at the foot of the sidebar.
func (p *connectionsPage) refreshHelperCard() {
	keys := p.g.app.helperKeys()
	go func() {
		macs, err := keys.Macs()
		onMain(func() {
			title, body, ready := helperCard(macs, err)
			p.helperName.SetLabel(title)
			p.helperBody.SetLabel(body)
			if ready {
				p.helperDot.AddCSSClass("online")
			} else {
				p.helperDot.RemoveCSSClass("online")
			}
		})
	}()
}

// ---- content ----

func (p *connectionsPage) buildContent() gtk.Widgetter {
	header := adw.NewHeaderBar()
	header.SetShowTitle(false)
	menu := gio.NewMenu()
	menu.Append("Preferences", "win.preferences")
	menu.Append("About sharelinux", "win.about")
	menu.Append("Quit", "win.quit")
	menuButton := gtk.NewMenuButton()
	menuButton.SetIconName(iconMore)
	menuButton.SetTooltipText("Main menu")
	setAccessibleLabel(menuButton, "Main menu")
	menuButton.SetMenuModel(menu)
	menuButton.AddCSSClass("oss-icon")
	header.PackEnd(menuButton)

	p.title = gtk.NewLabel("Your Macs")
	p.title.AddCSSClass("oss-page-title")
	p.title.SetXAlign(0)
	p.title.SetHExpand(true)

	p.searchEntry = gtk.NewSearchEntry()
	p.searchEntry.AddCSSClass("oss-search")
	p.searchEntry.SetPlaceholderText("Search Macs")
	p.searchEntry.SetSizeRequest(120, -1)
	setAccessibleLabel(p.searchEntry, "Search Macs")
	p.searchEntry.ConnectSearchChanged(p.rebuild)

	addBox := gtk.NewBox(gtk.OrientationHorizontal, 8)
	addBox.Append(icon(iconAdd, 18))
	addBox.Append(gtk.NewLabel("Add a Mac"))
	add := gtk.NewButton()
	add.SetChild(addBox)
	add.AddCSSClass("oss-primary")
	add.SetActionName("win.new-connection")

	titleRow := gtk.NewBox(gtk.OrientationHorizontal, 10)
	titleRow.Append(p.title)
	titleRow.Append(p.searchEntry)
	titleRow.Append(add)

	p.hero = gtk.NewBox(gtk.OrientationVertical, 0)

	p.gridTitle = gtk.NewLabel("Saved and on this network")
	p.gridTitle.AddCSSClass("oss-section-title")
	p.gridTitle.SetXAlign(0)
	p.gridTitle.SetHExpand(true)
	p.scanSpinner = gtk.NewSpinner()
	p.scanSpinner.SetTooltipText("Looking for Macs")
	gridHeader := gtk.NewBox(gtk.OrientationHorizontal, 10)
	gridHeader.Append(p.gridTitle)
	gridHeader.Append(p.scanSpinner)

	p.grid = gtk.NewFlowBox()
	p.grid.SetSelectionMode(gtk.SelectionNone)
	p.grid.SetHomogeneous(true)
	p.grid.SetColumnSpacing(16)
	p.grid.SetRowSpacing(16)
	p.grid.SetMinChildrenPerLine(1)
	p.grid.SetMaxChildrenPerLine(6)
	p.grid.SetActivateOnSingleClick(false)
	p.grid.SetVAlign(gtk.AlignStart)

	p.nothingHere = gtk.NewLabel("")
	p.nothingHere.AddCSSClass("oss-muted")
	p.nothingHere.SetXAlign(0)
	p.nothingHere.SetWrap(true)

	p.hint = keycapHint("Not listed? Press ", []string{"Ctrl", "N"}, " and type its address.")
	p.hint.SetHAlign(gtk.AlignStart)

	section := gtk.NewBox(gtk.OrientationVertical, 14)
	section.Append(gridHeader)
	section.Append(p.grid)
	section.Append(p.nothingHere)
	section.Append(p.hint)

	column := gtk.NewBox(gtk.OrientationVertical, 28)
	column.SetMarginTop(8)
	column.SetMarginBottom(40)
	column.SetMarginStart(40)
	column.SetMarginEnd(40)
	column.Append(titleRow)
	column.Append(p.hero)
	column.Append(section)
	clamp := adw.NewClamp()
	clamp.SetMaximumSize(1240)
	clamp.SetTighteningThreshold(900)
	clamp.SetChild(column)
	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetChild(clamp)

	empty := adw.NewStatusPage()
	empty.SetIconName(iconMacs)
	empty.SetTitle("No Macs yet")
	empty.SetDescription("Turn on Screen Sharing on a Mac, then add it by its address. Macs on this network show up here by themselves.")
	addFirst := gtk.NewButtonWithLabel("Add a Mac")
	addFirst.AddCSSClass("oss-primary")
	addFirst.SetHAlign(gtk.AlignCenter)
	addFirst.SetActionName("win.new-connection")
	empty.SetChild(addFirst)

	p.stack = gtk.NewStack()
	p.stack.AddNamed(scroll, "list")
	p.stack.AddNamed(empty, "empty")

	toolbar := adw.NewToolbarView()
	toolbar.AddTopBar(header)
	toolbar.SetContent(p.stack)
	return toolbar
}

// keycapHint builds a line of text with keys drawn as keycaps.
func keycapHint(before string, keys []string, after string) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 5)
	box.SetHAlign(gtk.AlignCenter)
	first := gtk.NewLabel(before)
	first.AddCSSClass("oss-hint-line")
	box.Append(first)
	for _, key := range keys {
		cap := gtk.NewLabel(key)
		cap.AddCSSClass("oss-keycap")
		box.Append(cap)
	}
	last := gtk.NewLabel(after)
	last.AddCSSClass("oss-hint-line")
	box.Append(last)
	return box
}

// focusSearch puts the cursor in the search box (Ctrl+F).
func (p *connectionsPage) focusSearch() {
	p.split.SetShowContent(true)
	p.searchEntry.GrabFocus()
}

// start loads the list and begins looking for Macs and checking on saved ones.
func (p *connectionsPage) start() {
	ctx, cancel := context.WithCancel(p.g.ctx)
	p.cancel = cancel
	p.reload()
	p.refreshHelperCard()
	go p.probeLoop(ctx)
	go p.scanLoop(ctx)
}

func (p *connectionsPage) stop() {
	if p.cancel != nil {
		p.cancel()
	}
}

// reload reads the saved list again, then redraws.
func (p *connectionsPage) reload() {
	saved, err := p.g.app.store.List()
	if err != nil {
		p.g.app.logger.Warn("saved connections", "error", err)
		p.g.toast("Saved Macs could not be read: " + err.Error())
	}
	p.saved = saved
	p.rebuild()
}

func (p *connectionsPage) probeLoop(ctx context.Context) {
	dial := defaultProbeDial()
	for {
		addresses := make([]string, 0, len(p.savedSnapshot()))
		for _, connection := range p.savedSnapshot() {
			addresses = append(addresses, connection.target().Address())
		}
		if len(addresses) > 0 {
			result := probeAll(ctx, addresses, dial)
			if ctx.Err() != nil {
				return
			}
			onMain(func() { p.applyReachability(result) })
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(probeEvery):
		}
	}
}

// savedSnapshot returns the saved list for a goroutine; it asks the store, which has its own lock.
func (p *connectionsPage) savedSnapshot() []savedConnection {
	saved, err := p.g.app.store.List()
	if err != nil {
		return nil
	}
	return saved
}

func (p *connectionsPage) scanLoop(ctx context.Context) {
	for {
		onMain(func() { p.scanSpinner.SetSpinning(true) })
		found, err := scanMacs(ctx, nil)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.g.app.logger.Debug("discovery", "error", err)
		}
		learned := p.learnModels(found)
		onMain(func() {
			p.scanSpinner.SetSpinning(false)
			p.found = found
			if learned {
				p.reload()
				return
			}
			p.rebuild()
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(scanEvery):
		}
	}
}

// learnModels writes the model a Mac announced, and the picture settled on for
// it, onto the saved Mac it is, so its card keeps the right picture while it is
// switched off. It reports whether anything changed.
func (p *connectionsPage) learnModels(found []discoveredMac) bool {
	changed := false
	for _, connection := range p.savedSnapshot() {
		model := connection.Model
		for _, mac := range found {
			if mac.Model != "" && mac.matches(connection) {
				model = mac.Model
			}
		}
		art := artWith(connection, found)
		if model == connection.Model && (art == artDisplay || string(art) == connection.Art) {
			continue
		}
		updated, err := p.g.app.store.Update(connection.target(), func(c *savedConnection) {
			c.Model = model
			if art != artDisplay {
				c.Art = string(art)
			}
		})
		if err != nil {
			p.g.app.logger.Warn("note the model of a Mac", "error", err)
		}
		changed = changed || updated
	}
	return changed
}

func (p *connectionsPage) applyReachability(result map[string]reachability) {
	for address, state := range result {
		p.reachable[address] = state
	}
	for _, connection := range p.saved {
		p.paintStatus(connection)
	}
}

func (p *connectionsPage) paintStatus(connection savedConnection) {
	id := connection.target().ID()
	state := p.reachable[connection.target().Address()]
	for _, widgets := range p.statuses[id] {
		widgets.label.SetLabel(statusText(state, widgets.extra))
		if state == reachOnline {
			widgets.dot.AddCSSClass("online")
			widgets.label.AddCSSClass("online")
			widgets.label.AddCSSClass("oss-status")
		} else {
			widgets.dot.RemoveCSSClass("online")
			widgets.label.RemoveCSSClass("online")
			widgets.label.RemoveCSSClass("oss-status")
		}
	}
}

// statusText is the line under a Mac's name: "Online · last used 2 hours ago".
func statusText(state reachability, extra string) string {
	word := state.String()
	switch {
	case word == "":
		return extra
	case extra == "":
		return word
	}
	return word + " · " + extra
}

func matchesQuery(query string, fields ...string) bool {
	if query == "" {
		return true
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// artFor picks the illustration of a saved Mac: its own model, or the one a
// Mac found on the network with the same address announced, then what its
// names say, then the picture kept from an earlier look.
func (p *connectionsPage) artFor(connection savedConnection) macArt {
	return artWith(connection, p.found)
}

// artWith is artFor against a given list of Macs found on the network.
func artWith(connection savedConnection, found []discoveredMac) macArt {
	model := connection.Model
	names := []string{connection.Name, connection.Host}
	for _, mac := range found {
		if !mac.matches(connection) {
			continue
		}
		if mac.Model != "" {
			model = mac.Model
		}
		names = append(names, mac.Name, mac.Host)
	}
	if art := resolveArt(model, names...); art != artDisplay {
		return art
	}
	if kept := macArt(connection.Art); slices.Contains(allMacArt, kept) {
		return kept
	}
	return artDisplay
}

// rebuild redraws the hero and the grid from the saved and found data and the search text.
func (p *connectionsPage) rebuild() {
	p.statuses = map[string][]rowStatus{}
	p.grid.RemoveAll()
	for child := p.hero.FirstChild(); child != nil; child = p.hero.FirstChild() {
		p.hero.Remove(child)
	}
	query := strings.ToLower(strings.TrimSpace(p.searchEntry.Text()))
	current := now()
	others := unsaved(p.found, p.saved)
	p.macsCount.SetLabel(strconv.Itoa(len(p.saved) + len(others)))

	var cards int
	switch p.view {
	case viewRecent:
		p.title.SetLabel("Recent sessions")
		p.gridTitle.SetLabel("Macs you connected to, newest first")
		for _, connection := range recentMacs(p.saved) {
			if !matchesQuery(query, connection.displayName(), connection.Host, connection.User) {
				continue
			}
			p.grid.Append(p.savedCard(connection, current))
			cards++
		}
		p.nothingHere.SetLabel("No sessions yet. A Mac shows up here after you connect to it.")
		if query != "" {
			p.nothingHere.SetLabel("No Mac matches that search.")
		}
		p.hint.SetVisible(false)
	default:
		p.title.SetLabel("Your Macs")
		p.gridTitle.SetLabel("Saved and on this network")
		if last, ok := lastUsedMac(p.saved); ok && query == "" {
			p.hero.Append(p.heroCard(last, current))
		}
		for _, connection := range p.saved {
			if !matchesQuery(query, connection.displayName(), connection.Host, connection.User) {
				continue
			}
			p.grid.Append(p.savedCard(connection, current))
			cards++
		}
		for _, mac := range others {
			if !matchesQuery(query, mac.Name, mac.Host) {
				continue
			}
			p.grid.Append(p.foundCard(mac))
			cards++
		}
		p.nothingHere.SetLabel("No Mac matches that search.")
		p.hint.SetVisible(true)
	}
	p.hero.SetVisible(p.hero.FirstChild() != nil)
	p.grid.SetVisible(cards > 0)
	p.nothingHere.SetVisible(cards == 0 && (query != "" || p.view == viewRecent))
	for _, connection := range p.saved {
		p.paintStatus(connection)
	}
	nothing := len(p.saved) == 0 && len(others) == 0 && query == ""
	if nothing && p.view == viewMacs {
		p.stack.SetVisibleChildName("empty")
	} else {
		p.stack.SetVisibleChildName("list")
	}
}

// statusRow is a dot and a line of words. It is registered under id so a
// reachability result can repaint it.
func (p *connectionsPage) statusRow(id, extra string, dotSize int) (*gtk.Box, *gtk.Box) {
	dot := gtk.NewBox(gtk.OrientationHorizontal, 0)
	dot.AddCSSClass("oss-dot")
	dot.SetVAlign(gtk.AlignCenter)
	dot.SetSizeRequest(dotSize, dotSize)
	label := gtk.NewLabel(extra)
	label.AddCSSClass("oss-status-line")
	label.SetXAlign(0)
	label.SetEllipsize(3)
	row := gtk.NewBox(gtk.OrientationHorizontal, 6)
	row.Append(dot)
	row.Append(label)
	if id != "" {
		p.statuses[id] = append(p.statuses[id], rowStatus{dot: dot, label: label, extra: extra})
	}
	return row, dot
}

// savedExtra is the part of a saved Mac's status line after the state word.
func savedExtra(connection savedConnection, current time.Time) string {
	if connection.LastUsed.IsZero() {
		return connection.Host
	}
	return "last used " + agoText(connection.LastUsed, current)
}

// ---- hero ----

func (p *connectionsPage) heroCard(connection savedConnection, current time.Time) gtk.Widgetter {
	art := p.artFor(connection)

	badge := gtk.NewLabel(lastFrameText(connection.LastUsed, current))
	badge.AddCSSClass("oss-thumb-badge")
	badge.SetHAlign(gtk.AlignStart)
	badge.SetVAlign(gtk.AlignStart)
	badge.SetMarginTop(12)
	badge.SetMarginStart(12)

	overlay := gtk.NewOverlay()
	frame := p.lastFramePicture(connection)
	if frame != nil {
		overlay.SetChild(frame)
	} else {
		empty := gtk.NewBox(gtk.OrientationVertical, 0)
		empty.AddCSSClass("oss-thumb-empty")
		empty.SetHExpand(true)
		empty.SetVExpand(true)
		overlay.SetChild(empty)
		picture := icon(macArtIcon(art), 120)
		picture.SetHAlign(gtk.AlignCenter)
		picture.SetVAlign(gtk.AlignCenter)
		picture.SetOpacity(0.55)
		overlay.AddOverlay(picture)
		badge.SetLabel("NO PICTURE YET")
	}
	overlay.AddOverlay(badge)
	overlay.SetOverflow(gtk.OverflowHidden)
	overlay.AddCSSClass("oss-thumb")
	ratio := gtk.NewAspectFrame(0.5, 0.5, 16.0/9.0, false)
	ratio.SetChild(overlay)
	ratio.SetHExpand(true)
	ratio.SetVAlign(gtk.AlignCenter)
	ratio.SetSizeRequest(200, -1)

	overline := gtk.NewLabel("PICK UP WHERE YOU LEFT OFF")
	overline.AddCSSClass("oss-overline")
	overline.SetXAlign(0)
	name := gtk.NewLabel(connection.displayName())
	name.AddCSSClass("oss-hero-name")
	name.SetXAlign(0)
	name.SetWrap(true)
	status, _ := p.statusRow(connection.target().ID(), connection.Host, 8)
	titleBox := gtk.NewBox(gtk.OrientationVertical, 6)
	titleBox.Append(name)
	titleBox.Append(status)

	chips := gtk.NewBox(gtk.OrientationHorizontal, 8)
	chipCount := 0
	if connection.Last != nil {
		for _, c := range connection.Last.chips() {
			label := gtk.NewLabel(c.Text)
			label.AddCSSClass("oss-chip")
			if c.HDR {
				label.AddCSSClass("hdr")
			}
			chips.Append(label)
			chipCount++
		}
	}
	chips.SetVisible(chipCount > 0)

	connectBox := gtk.NewBox(gtk.OrientationHorizontal, 8)
	connectBox.Append(icon(iconPlay, 18))
	connectBox.Append(gtk.NewLabel("Connect"))
	connect := gtk.NewButton()
	connect.SetChild(connectBox)
	connect.AddCSSClass("oss-primary")
	connect.AddCSSClass("hero")
	connect.ConnectClicked(func() { p.g.openSaved(connection) })
	exclusiveBox := gtk.NewBox(gtk.OrientationHorizontal, 8)
	exclusiveBox.Append(icon(iconExclusive, 18))
	exclusiveBox.Append(gtk.NewLabel("Exclusive mode"))
	exclusive := gtk.NewButton()
	exclusive.SetChild(exclusiveBox)
	exclusive.AddCSSClass("oss-secondary")
	exclusive.ConnectClicked(func() { p.g.openSavedExclusive(connection) })
	buttons := gtk.NewBox(gtk.OrientationHorizontal, 10)
	buttons.SetMarginTop(6)
	buttons.Append(connect)
	buttons.Append(exclusive)

	info := gtk.NewBox(gtk.OrientationVertical, 14)
	info.SetHExpand(true)
	info.SetVAlign(gtk.AlignCenter)
	info.Append(overline)
	info.Append(titleBox)
	info.Append(chips)
	info.Append(buttons)

	hero := gtk.NewBox(gtk.OrientationHorizontal, 28)
	hero.AddCSSClass("oss-hero")
	hero.SetHomogeneous(true)
	hero.Append(ratio)
	hero.Append(info)
	return hero
}

// lastFramePicture shows the saved last frame of a Mac, or nil when there is none.
func (p *connectionsPage) lastFramePicture(connection savedConnection) *gtk.Picture {
	path := p.g.thumbnailFile(connection.target().ID())
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	picture := gtk.NewPictureForFilename(path)
	picture.SetContentFit(gtk.ContentFitCover)
	picture.SetCanShrink(true)
	picture.SetHExpand(true)
	picture.SetVExpand(true)
	return picture
}

// ---- cards ----

// cardShell is the frame every Mac card shares: the picture, the name and the
// status line. The caller appends the button.
func (p *connectionsPage) cardShell(art macArt, name, tooltip string, status *gtk.Box, overflow gtk.Widgetter) *gtk.Box {
	picture := icon(macArtIcon(art), 96)
	picture.SetHAlign(gtk.AlignCenter)
	picture.SetVAlign(gtk.AlignCenter)
	picture.SetVExpand(true)
	artBox := gtk.NewBox(gtk.OrientationVertical, 0)
	artBox.AddCSSClass("oss-art")
	artBox.SetSizeRequest(-1, 110)
	artBox.Append(picture)
	overlay := gtk.NewOverlay()
	overlay.SetChild(artBox)
	if overflow != nil {
		overlay.AddOverlay(overflow)
	}

	title := gtk.NewLabel(name)
	title.AddCSSClass("oss-card-name")
	title.SetXAlign(0)
	title.SetEllipsize(3)
	text := gtk.NewBox(gtk.OrientationVertical, 4)
	text.Append(title)
	text.Append(status)

	card := gtk.NewBox(gtk.OrientationVertical, 14)
	card.AddCSSClass("oss-card")
	card.SetSizeRequest(220, -1)
	card.SetVAlign(gtk.AlignStart)
	card.SetTooltipText(tooltip)
	card.Append(overlay)
	card.Append(text)
	return card
}

func (p *connectionsPage) savedCard(connection savedConnection, current time.Time) gtk.Widgetter {
	status, _ := p.statusRow(connection.target().ID(), savedExtra(connection, current), 7)
	menuButton := p.cardMenu(connection)
	card := p.cardShell(p.artFor(connection), connection.displayName(), connection.subtitle(current), status, menuButton)

	button := gtk.NewButtonWithLabel("Connect")
	if p.reachable[connection.target().Address()] == reachOffline {
		button.AddCSSClass("oss-ghost")
	} else {
		button.AddCSSClass("oss-soft")
	}
	setAccessibleLabel(button, "Connect to "+connection.displayName())
	button.ConnectClicked(func() { p.g.openSaved(connection) })
	card.Append(button)

	// A right click or a long press anywhere on the card opens the same menu.
	click := gtk.NewGestureClick()
	click.SetButton(3)
	click.ConnectPressed(func(int, float64, float64) { menuButton.Popup() })
	card.AddController(click)
	press := gtk.NewGestureLongPress()
	press.ConnectPressed(func(float64, float64) { menuButton.Popup() })
	card.AddController(press)
	return card
}

// cardMenu is the small menu on a saved Mac's card: edit the sign-in, remove the Mac.
func (p *connectionsPage) cardMenu(connection savedConnection) *gtk.MenuButton {
	menu := gio.NewMenu()
	menu.Append("Edit sign-in...", "card.edit")
	menu.Append("Remove", "card.remove")
	group := gio.NewSimpleActionGroup()
	edit := gio.NewSimpleAction("edit", nil)
	edit.ConnectActivate(func(*glib.Variant) { p.g.editSaved(connection) })
	group.AddAction(edit)
	remove := gio.NewSimpleAction("remove", nil)
	remove.ConnectActivate(func(*glib.Variant) { p.confirmRemove(connection) })
	group.AddAction(remove)

	button := gtk.NewMenuButton()
	button.SetIconName(iconMore)
	button.AddCSSClass("oss-icon")
	button.SetHAlign(gtk.AlignEnd)
	button.SetVAlign(gtk.AlignStart)
	button.SetMarginTop(6)
	button.SetMarginEnd(6)
	button.SetTooltipText("More for " + connection.displayName())
	setAccessibleLabel(button, "More for "+connection.displayName())
	button.SetMenuModel(menu)
	button.InsertActionGroup("card", group)
	return button
}

func (p *connectionsPage) confirmRemove(connection savedConnection) {
	dialog := adw.NewAlertDialog("Remove "+connection.displayName()+"?", "The Mac and its saved password are removed from this computer. The Mac itself is not changed.")
	dialog.AddResponse("cancel", "Cancel")
	dialog.AddResponse("remove", "Remove")
	dialog.SetResponseAppearance("remove", adw.ResponseDestructive)
	dialog.SetDefaultResponse("cancel")
	dialog.SetCloseResponse("cancel")
	dialog.ConnectResponse(func(response string) {
		if response != "remove" {
			return
		}
		err := p.g.app.store.Remove(connection)
		if err != nil {
			p.g.showError("The Mac could not be removed", err.Error())
		}
		p.reload()
	})
	dialog.Present(p.g.window)
}

func (p *connectionsPage) foundCard(mac discoveredMac) gtk.Widgetter {
	status, dot := p.statusRow("", "Found on this network", 7)
	dot.AddCSSClass("online")
	card := p.cardShell(resolveArt(mac.Model, mac.Name, mac.Host), mac.Name, mac.subtitle(), status, nil)
	button := gtk.NewButtonWithLabel("Connect")
	button.AddCSSClass("oss-soft")
	setAccessibleLabel(button, "Connect to "+mac.Name)
	button.ConnectClicked(func() { p.g.openDiscovered(mac) })
	card.Append(button)
	return card
}

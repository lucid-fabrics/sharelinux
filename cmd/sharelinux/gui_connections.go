//go:build linux

package main

import (
	"context"
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

// rowStatus are the widgets of a saved Mac's status that change without a rebuild.
type rowStatus struct {
	dot   *gtk.Box
	label *gtk.Label
}

// connectionsPage is the first screen: saved Macs, Macs on the network, and
// the way to add one.
type connectionsPage struct {
	g    *gui
	page *adw.NavigationPage

	searchBar    *gtk.SearchBar
	searchEntry  *gtk.SearchEntry
	searchButton *gtk.ToggleButton
	stack        *gtk.Stack
	savedGroup   *adw.PreferencesGroup
	foundGroup   *adw.PreferencesGroup
	scanSpinner  *gtk.Spinner
	hint         *gtk.Box

	savedRows []*adw.ActionRow
	foundRows []*adw.ActionRow
	statuses  map[string]rowStatus

	saved     []savedConnection
	found     []discoveredMac
	reachable map[string]reachability

	cancel context.CancelFunc
}

func newConnectionsPage(g *gui) *connectionsPage {
	p := &connectionsPage{g: g, statuses: map[string]rowStatus{}, reachable: map[string]reachability{}}

	header := adw.NewHeaderBar()
	add := gtk.NewButtonFromIconName("list-add-symbolic")
	add.SetTooltipText("Add a Mac")
	add.SetActionName("win.new-connection")
	header.PackStart(add)

	menu := gio.NewMenu()
	menu.Append("Preferences", "win.preferences")
	menu.Append("About Screen Sharing", "win.about")
	menu.Append("Quit", "win.quit")
	menuButton := gtk.NewMenuButton()
	menuButton.SetIconName("open-menu-symbolic")
	menuButton.SetTooltipText("Main menu")
	menuButton.SetMenuModel(menu)
	header.PackEnd(menuButton)

	p.searchButton = gtk.NewToggleButton()
	p.searchButton.SetIconName("system-search-symbolic")
	p.searchButton.SetTooltipText("Search")
	header.PackEnd(p.searchButton)

	title := adw.NewWindowTitle(windowName, "")
	header.SetTitleWidget(title)

	p.searchEntry = gtk.NewSearchEntry()
	p.searchEntry.SetPlaceholderText("Search Macs")
	p.searchEntry.SetHExpand(true)
	p.searchBar = gtk.NewSearchBar()
	p.searchBar.SetChild(p.searchEntry)
	p.searchBar.ConnectEntry(p.searchEntry)
	p.searchButton.ConnectToggled(func() { p.searchBar.SetSearchMode(p.searchButton.Active()) })
	p.searchBar.NotifyProperty("search-mode-enabled", func() { p.searchButton.SetActive(p.searchBar.SearchMode()) })
	p.searchEntry.ConnectSearchChanged(p.rebuild)
	p.searchEntry.ConnectStopSearch(func() { p.searchBar.SetSearchMode(false) })

	p.savedGroup = adw.NewPreferencesGroup()
	p.savedGroup.SetTitle("Saved")
	p.foundGroup = adw.NewPreferencesGroup()
	p.foundGroup.SetTitle("On this network")
	p.scanSpinner = gtk.NewSpinner()
	p.scanSpinner.SetTooltipText("Looking for Macs")
	p.foundGroup.SetHeaderSuffix(p.scanSpinner)

	p.hint = keycapHint("Not listed? Press ", []string{"Ctrl", "N"}, " and type its address.")

	column := gtk.NewBox(gtk.OrientationVertical, 28)
	column.SetMarginTop(32)
	column.SetMarginBottom(32)
	column.SetMarginStart(24)
	column.SetMarginEnd(24)
	column.Append(p.savedGroup)
	column.Append(p.foundGroup)
	column.Append(p.hint)
	clamp := adw.NewClamp()
	clamp.SetMaximumSize(680)
	clamp.SetChild(column)
	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetChild(clamp)

	empty := adw.NewStatusPage()
	empty.SetIconName("computer-symbolic")
	empty.SetTitle("No Macs yet")
	empty.SetDescription("Turn on Screen Sharing on a Mac, then add it by its address. Macs on this network show up here by themselves.")
	addFirst := gtk.NewButtonWithLabel("Add a Mac")
	addFirst.AddCSSClass("pill")
	addFirst.AddCSSClass("suggested-action")
	addFirst.SetHAlign(gtk.AlignCenter)
	addFirst.SetActionName("win.new-connection")
	empty.SetChild(addFirst)

	p.stack = gtk.NewStack()
	p.stack.AddNamed(scroll, "list")
	p.stack.AddNamed(empty, "empty")

	toolbar := adw.NewToolbarView()
	toolbar.AddTopBar(header)
	toolbar.AddTopBar(p.searchBar)
	toolbar.SetContent(p.stack)
	p.page = adw.NewNavigationPageWithTag(toolbar, windowName, "connections")
	return p
}

// keycapHint builds a line of text with keys drawn as keycaps.
func keycapHint(before string, keys []string, after string) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 5)
	box.SetHAlign(gtk.AlignCenter)
	first := gtk.NewLabel(before)
	first.AddCSSClass("dim-label")
	box.Append(first)
	for _, key := range keys {
		cap := gtk.NewLabel(key)
		cap.AddCSSClass("oss-keycap")
		box.Append(cap)
	}
	last := gtk.NewLabel(after)
	last.AddCSSClass("dim-label")
	box.Append(last)
	return box
}

func (p *connectionsPage) toggleSearch() {
	p.searchButton.SetActive(!p.searchButton.Active())
	if p.searchButton.Active() {
		p.searchEntry.GrabFocus()
	}
}

// start loads the list and begins looking for Macs and checking on saved ones.
func (p *connectionsPage) start() {
	ctx, cancel := context.WithCancel(p.g.ctx)
	p.cancel = cancel
	p.reload()
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
		onMain(func() {
			p.scanSpinner.SetSpinning(false)
			p.found = found
			p.rebuild()
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(scanEvery):
		}
	}
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
	widgets, ok := p.statuses[connection.target().ID()]
	if !ok {
		return
	}
	state := p.reachable[connection.target().Address()]
	widgets.label.SetLabel(state.String())
	widgets.dot.SetVisible(state != reachUnknown)
	widgets.label.SetVisible(state != reachUnknown)
	if state == reachOnline {
		widgets.dot.AddCSSClass("online")
		widgets.label.AddCSSClass("online")
		widgets.label.AddCSSClass("oss-status")
	} else {
		widgets.dot.RemoveCSSClass("online")
		widgets.label.RemoveCSSClass("online")
		widgets.label.AddCSSClass("dim-label")
	}
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

// rebuild redraws both lists from the saved and found data and the search text.
func (p *connectionsPage) rebuild() {
	for _, row := range p.savedRows {
		p.savedGroup.Remove(row)
	}
	for _, row := range p.foundRows {
		p.foundGroup.Remove(row)
	}
	p.savedRows, p.foundRows = nil, nil
	p.statuses = map[string]rowStatus{}
	query := strings.ToLower(strings.TrimSpace(p.searchEntry.Text()))
	current := now()
	for _, connection := range p.saved {
		if !matchesQuery(query, connection.displayName(), connection.Host, connection.User) {
			continue
		}
		row := p.savedRow(connection, current)
		p.savedGroup.Add(row)
		p.savedRows = append(p.savedRows, row)
		p.paintStatus(connection)
	}
	others := unsaved(p.found, p.saved)
	for _, mac := range others {
		if !matchesQuery(query, mac.Name, mac.Host) {
			continue
		}
		row := p.foundRow(mac)
		p.foundGroup.Add(row)
		p.foundRows = append(p.foundRows, row)
	}
	p.savedGroup.SetVisible(len(p.savedRows) > 0)
	p.foundGroup.SetVisible(len(p.foundRows) > 0 || len(p.saved) == 0 && query == "")
	nothing := len(p.saved) == 0 && len(others) == 0 && query == ""
	if nothing {
		p.stack.SetVisibleChildName("empty")
	} else {
		p.stack.SetVisibleChildName("list")
	}
}

// tile is the coloured square with an icon in it.
func tile(color string, large bool) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 0)
	box.AddCSSClass("oss-tile")
	box.AddCSSClass("tile-" + color)
	if large {
		box.AddCSSClass("large")
	}
	box.SetHAlign(gtk.AlignCenter)
	box.SetVAlign(gtk.AlignCenter)
	box.SetHExpand(false)
	box.SetVExpand(false)
	icon := gtk.NewImageFromIconName("video-display-symbolic")
	if large {
		icon.SetPixelSize(34)
	} else {
		icon.SetPixelSize(22)
	}
	icon.SetHExpand(true)
	icon.SetVExpand(true)
	box.Append(icon)
	return box
}

func (p *connectionsPage) savedRow(connection savedConnection, current time.Time) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(glib.MarkupEscapeText(connection.displayName()))
	row.SetSubtitle(glib.MarkupEscapeText(connection.subtitle(current)))
	row.SetActivatable(true)
	row.AddPrefix(tile(tileColor(connection.target().ID()), false))

	dot := gtk.NewBox(gtk.OrientationHorizontal, 0)
	dot.AddCSSClass("oss-dot")
	dot.SetVAlign(gtk.AlignCenter)
	label := gtk.NewLabel("")
	status := gtk.NewBox(gtk.OrientationHorizontal, 6)
	status.SetVAlign(gtk.AlignCenter)
	status.Append(dot)
	status.Append(label)
	row.AddSuffix(status)
	row.AddSuffix(gtk.NewImageFromIconName("go-next-symbolic"))
	p.statuses[connection.target().ID()] = rowStatus{dot: dot, label: label}

	row.ConnectActivated(func() { p.g.openSaved(connection) })
	p.addContextMenu(row, connection)
	return row
}

// addContextMenu lets a right click or a long press remove a saved Mac.
func (p *connectionsPage) addContextMenu(row *adw.ActionRow, connection savedConnection) {
	menu := gio.NewMenu()
	menu.Append("Remove", "row.remove")
	group := gio.NewSimpleActionGroup()
	remove := gio.NewSimpleAction("remove", nil)
	remove.ConnectActivate(func(*glib.Variant) { p.confirmRemove(connection) })
	group.AddAction(remove)
	row.InsertActionGroup("row", group)
	popover := gtk.NewPopoverMenuFromModel(menu)
	popover.SetParent(row)
	popover.SetHasArrow(false)
	popover.SetHAlign(gtk.AlignStart)
	show := func(x, y float64) {
		rect := gdkRect(int(x), int(y))
		popover.SetPointingTo(rect)
		popover.Popup()
	}
	click := gtk.NewGestureClick()
	click.SetButton(3)
	click.ConnectPressed(func(_ int, x, y float64) { show(x, y) })
	row.AddController(click)
	press := gtk.NewGestureLongPress()
	press.ConnectPressed(func(x, y float64) { show(x, y) })
	row.AddController(press)
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

func (p *connectionsPage) foundRow(mac discoveredMac) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(glib.MarkupEscapeText(mac.Name))
	row.SetSubtitle(glib.MarkupEscapeText(mac.subtitle()))
	row.AddPrefix(tile("slate", false))
	connect := gtk.NewButtonWithLabel("Connect")
	connect.AddCSSClass("pill")
	connect.SetVAlign(gtk.AlignCenter)
	connect.ConnectClicked(func() { p.g.openDiscovered(mac) })
	row.AddSuffix(connect)
	return row
}

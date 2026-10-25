//go:build linux

package main

import (
	"context"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The "Mouse helper" group of Preferences: whether this computer is paired
// with the helper app on a Mac, a button to pair, and the paired Macs with
// Remove. Pairing itself is pairHelperDialog.

// helperGroup is the preferences group; it rebuilds its rows when the list changes.
type helperGroup struct {
	g      *gui
	group  *adw.PreferencesGroup
	status *adw.ActionRow
	macs   []*adw.ActionRow
}

func (g *gui) mouseHelperGroup() *adw.PreferencesGroup {
	h := &helperGroup{g: g, group: adw.NewPreferencesGroup()}
	h.group.SetTitle("Mouse helper")
	h.group.SetDescription("A small app on the Mac that gives games and 3D tools smooth, unlimited mouse movement. Pair this computer with it once.")
	prefs := g.app.preferences()
	h.group.Add(switchRow("Use the mouse helper when available",
		"While the pointer is locked in Exclusive mode and the helper runs on that Mac. Otherwise the mouse works as it always did",
		prefs.MouseHelper, func(on bool) {
			if err := g.app.updatePreferences(func(s *settings) { s.MouseHelper = on }); err != nil {
				g.toast("Preferences were not saved: " + err.Error())
			}
			if g.session != nil {
				g.session.helper.sync()
			}
		}))
	h.status = adw.NewActionRow()
	h.status.SetTitle("Status")
	h.status.SetSubtitle("Checking")
	h.group.Add(h.status)

	pair := adw.NewActionRow()
	pair.SetTitle("Pair with this Mac...")
	pair.SetSubtitle("Needs sharelinux Mouse Helper running on the Mac, with its pairing window open")
	pair.AddSuffix(icon(iconForward, 16))
	pair.SetActivatable(true)
	pair.ConnectActivated(func() { g.pairHelperDialog(h.refresh) })
	h.group.Add(pair)
	h.refresh()
	return h.group
}

// refresh reads the paired Macs from the keyring (off the main thread: the
// keyring can ask for an unlock) and rebuilds the rows.
func (h *helperGroup) refresh() {
	keys := h.g.app.helperKeys()
	go func() {
		macs, err := keys.Macs()
		onMain(func() { h.apply(macs, err) })
	}()
}

func (h *helperGroup) apply(macs []pairedMac, err error) {
	if h.g.session != nil {
		h.g.session.helper.reload()
	}
	h.status.SetSubtitle(glib.MarkupEscapeText(helperStatus(macs, err)))
	for _, row := range h.macs {
		h.group.Remove(row)
	}
	h.macs = nil
	for _, mac := range macs {
		row := adw.NewActionRow()
		row.SetTitle(glib.MarkupEscapeText(mac.Name))
		row.SetSubtitle(glib.MarkupEscapeText(macRowSubtitle(mac)))
		remove := gtk.NewButtonWithLabel("Remove")
		remove.SetVAlign(gtk.AlignCenter)
		remove.AddCSSClass("flat")
		remove.AddCSSClass("destructive-action")
		remove.ConnectClicked(func() { h.confirmRemove(mac) })
		row.AddSuffix(remove)
		h.group.Add(row)
		h.macs = append(h.macs, row)
	}
}

func (h *helperGroup) confirmRemove(mac pairedMac) {
	dialog := adw.NewAlertDialog("Remove "+mac.Name+"?",
		"This computer stops using the mouse helper on that Mac. The Mac keeps this computer in its own list until you remove it there, in sharelinux Mouse Helper's pairing window.")
	dialog.AddResponse("cancel", "Cancel")
	dialog.AddResponse("remove", "Remove")
	dialog.SetResponseAppearance("remove", adw.ResponseDestructive)
	dialog.SetDefaultResponse("cancel")
	dialog.SetCloseResponse("cancel")
	dialog.ConnectResponse(func(response string) {
		if response != "remove" {
			return
		}
		keys := h.g.app.helperKeys()
		go func() {
			err := keys.RemoveMac(mac.Key)
			onMain(func() {
				if err != nil {
					h.g.showError("The Mac could not be removed", pairingProblem(err))
				}
				h.refresh()
			})
		}()
	})
	dialog.Present(h.g.window)
}

// pairDialog is the window that shows the six digits and asks the person to
// compare them with the Mac's screen. Every attempt starts from a click on
// "Start pairing" and nothing else.
type pairDialog struct {
	g       *gui
	dialog  *adw.Dialog
	stack   *gtk.Stack
	changed func()

	address  *adw.EntryRow
	macName  *adw.EntryRow
	selfName *adw.EntryRow
	problem  *gtk.Label
	start    *gtk.Button

	code     *gtk.Label
	codeNote *gtk.Label
	doneText *gtk.Label
	failText *gtk.Label

	cancel       context.CancelFunc
	attempt      *pairingAttempt
	finished     bool
	macNameFinal string
}

func (g *gui) pairHelperDialog(changed func()) {
	p := &pairDialog{g: g, changed: changed}
	p.build()
	p.dialog.Present(g.window)
	p.address.GrabFocus()
}

func page(children ...gtk.Widgetter) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 14)
	box.SetMarginStart(24)
	box.SetMarginEnd(24)
	box.SetMarginTop(6)
	box.SetMarginBottom(24)
	for _, c := range children {
		box.Append(c)
	}
	return box
}

func wrapLabel(text string, classes ...string) *gtk.Label {
	label := gtk.NewLabel(text)
	label.SetWrap(true)
	label.SetMaxWidthChars(46)
	label.SetJustify(gtk.JustifyCenter)
	for _, class := range classes {
		label.AddCSSClass(class)
	}
	return label
}

func pillButton(label string, suggested bool) *gtk.Button {
	button := gtk.NewButtonWithLabel(label)
	button.AddCSSClass("pill")
	if suggested {
		button.AddCSSClass("suggested-action")
	}
	button.SetHAlign(gtk.AlignCenter)
	return button
}

func (p *pairDialog) build() {
	p.dialog = adw.NewDialog()
	p.dialog.SetTitle("Pair with this Mac")
	p.dialog.SetContentWidth(440)
	p.dialog.SetFollowsContentSize(true)

	// 1. the form
	p.address = adw.NewEntryRow()
	p.address.SetTitle("Mac address")
	p.macName = adw.NewEntryRow()
	p.macName.SetTitle("Name for this Mac")
	p.selfName = adw.NewEntryRow()
	p.selfName.SetTitle("This computer's name on the Mac")
	list := gtk.NewListBox()
	list.AddCSSClass("boxed-list")
	list.SetSelectionMode(gtk.SelectionNone)
	list.Append(p.address)
	list.Append(p.macName)
	list.Append(p.selfName)
	p.selfName.SetText(defaultDeviceName())
	if connections, err := p.g.app.store.List(); err == nil {
		host := defaultHelperAddress(connections)
		p.address.SetText(host)
		p.macName.SetText(host)
	}
	p.problem = wrapLabel("", "error")
	p.problem.SetVisible(false)
	p.start = pillButton("Start pairing", true)
	p.start.ConnectClicked(p.begin)
	for _, row := range []*adw.EntryRow{p.address, p.macName, p.selfName} {
		row.ConnectEntryActivated(p.begin)
	}
	form := page(
		wrapLabel("On the Mac, open the sharelinux Mouse Helper menu and choose Pair a Linux device. Then start here. The Mac shows a six-digit code and so will this computer.", "dim-label"),
		list, p.problem, p.start)

	// 2. waiting for the Mac
	spinner := gtk.NewSpinner()
	spinner.SetSpinning(true)
	spinner.SetSizeRequest(32, 32)
	cancelWaiting := pillButton("Cancel", false)
	cancelWaiting.ConnectClicked(func() { p.dialog.Close() })
	waiting := page(spinner, wrapLabel("Waiting for the Mac", "title-3"), cancelWaiting)

	// 3. compare the code
	p.code = wrapLabel("", "numeric")
	p.codeNote = wrapLabel("", "dim-label")
	confirm := pillButton("Confirm", true)
	confirm.ConnectClicked(p.confirm)
	reject := pillButton("Reject", false)
	reject.AddCSSClass("destructive-action")
	reject.ConnectClicked(p.reject)
	buttons := gtk.NewBox(gtk.OrientationHorizontal, 12)
	buttons.SetHAlign(gtk.AlignCenter)
	buttons.Append(reject)
	buttons.Append(confirm)
	compare := page(
		wrapLabel("Do the codes match?", "title-3"),
		wrapLabel("Look at the Mac's screen. Click Confirm here and on the Mac only if both show the same six digits. If they differ, click Reject.", "dim-label"),
		p.code, p.codeNote, buttons)

	// 4. done
	p.doneText = wrapLabel("", "title-3")
	finish := pillButton("Done", true)
	finish.ConnectClicked(func() { p.dialog.Close() })
	mark := gtk.NewImageFromIconName(iconCheck)
	mark.SetPixelSize(48)
	mark.AddCSSClass("success")
	done := page(mark, p.doneText,
		wrapLabel("Click Confirm in the Mac's pairing window as well. The Mac only trusts this computer once you do, and this computer shows Paired after the Mac has accepted it.", "dim-label"), finish)

	// 5. it did not work
	p.failText = wrapLabel("", "error")
	again := pillButton("Back", false)
	again.ConnectClicked(func() { p.stack.SetVisibleChildName("form") })
	failed := page(p.failText, again)

	p.stack = gtk.NewStack()
	p.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	p.stack.SetVhomogeneous(false)
	p.stack.AddNamed(form, "form")
	p.stack.AddNamed(waiting, "waiting")
	p.stack.AddNamed(compare, "compare")
	p.stack.AddNamed(done, "done")
	p.stack.AddNamed(failed, "failed")
	p.stack.SetVisibleChildName("form")

	toolbar := adw.NewToolbarView()
	header := adw.NewHeaderBar()
	toolbar.AddTopBar(header)
	toolbar.SetContent(p.stack)
	p.dialog.SetChild(toolbar)
	p.dialog.SetDefaultWidget(p.start)
	p.dialog.ConnectClosed(func() {
		if p.attempt != nil && !p.finished {
			p.attempt.Cancel()
		}
		if p.cancel != nil {
			p.cancel()
		}
		p.changed()
	})
}

func (p *pairDialog) fail(words string) {
	if words == "" {
		return
	}
	p.failText.SetLabel(words)
	p.stack.SetVisibleChildName("failed")
}

// begin is the one click that starts one attempt.
func (p *pairDialog) begin() {
	if p.attempt != nil && !p.finished {
		return
	}
	address, name, selfName := p.address.Text(), p.macName.Text(), p.selfName.Text()
	p.problem.SetVisible(false)
	if address == "" {
		p.problem.SetLabel("Enter the Mac's address.")
		p.problem.SetVisible(true)
		return
	}
	p.attempt, p.finished = nil, false
	p.macNameFinal = name
	p.stack.SetVisibleChildName("waiting")
	ctx, cancel := context.WithCancel(p.g.ctx)
	p.cancel = cancel
	pairing := p.g.app.helperPairingFlow()
	go func() {
		attempt, err := pairing.Begin(ctx, address, selfName)
		if err != nil {
			onMain(func() {
				p.stack.SetVisibleChildName("form")
				p.problem.SetLabel(pairingProblem(err))
				p.problem.SetVisible(true)
			})
			return
		}
		onMain(func() { p.attempt = attempt; p.finished = false })
		select {
		case code, ok := <-attempt.Code():
			if !ok {
				<-attempt.Done()
				onMain(func() { p.finished = true; p.fail(pairingProblem(attempt.Err())) })
				return
			}
			onMain(func() {
				p.code.SetMarkup(`<span size="300%" weight="bold">` + code[:3] + " " + code[3:] + `</span>`)
				p.codeNote.SetLabel("This computer shows up on the Mac as " + quoteName(selfName) + ".\n" + attempt.FingerprintLines())
				p.stack.SetVisibleChildName("compare")
			})
			<-attempt.Done()
			onMain(func() {
				if !p.finished {
					p.finished = true
					p.fail(pairingProblem(attempt.Err()))
				}
			})
		case <-attempt.Done():
			onMain(func() { p.finished = true; p.fail(pairingProblem(attempt.Err())) })
		}
	}()
}

func (p *pairDialog) confirm() {
	attempt, name := p.attempt, p.macNameFinal
	if attempt == nil || p.finished {
		return
	}
	p.finished = true
	go func() {
		_, err := attempt.Confirm(name)
		onMain(func() {
			if err != nil {
				p.fail(pairingProblem(err))
				return
			}
			p.doneText.SetLabel("Saved. Waiting for the Mac to confirm")
			p.stack.SetVisibleChildName("done")
			p.changed()
		})
	}()
}

func (p *pairDialog) reject() {
	if p.attempt == nil || p.finished {
		return
	}
	p.finished = true
	p.attempt.Reject()
	p.dialog.Close()
}

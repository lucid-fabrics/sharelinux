//go:build linux

package main

import (
	"context"
	"errors"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

// signInDialog collects who to connect as and runs the dial. A wrong password
// keeps it open with the problem shown inline.
type signInDialog struct {
	g      *gui
	dialog *adw.Dialog
	req    *request
	name   string
	vnc    bool

	lead          *gtk.Label
	title         *gtk.Label
	fields        *gtk.Box
	address       *adw.EntryRow
	account       *adw.EntryRow
	password      *adw.PasswordEntryRow
	remember      *adw.SwitchRow
	problem       *gtk.Label
	status        *gtk.Label
	connectButton *gtk.Button
	connectLabel  *gtk.Label
	spinner       *gtk.Spinner
	switchLogin   *gtk.Button

	dial    *dialer
	dialing bool
	done    bool
}

// openSaved starts the sign-in for a saved Mac. With a saved password it
// connects at once.
func (g *gui) openSaved(connection savedConnection) {
	req, err := g.app.newRequest(connection.target(), &connection)
	if err != nil {
		g.showError("This Mac's settings could not be read", err.Error())
		return
	}
	g.app.loadSavedPassword(req, connection)
	g.openSignInNamed(req, connection.displayName(), req.dialPassword() != "")
}

// openDiscovered starts the sign-in for a Mac found on the network.
func (g *gui) openDiscovered(mac discoveredMac) {
	req, err := g.app.newRequest(mac.target(), nil)
	if err != nil {
		g.showError("Could not prepare the connection", err.Error())
		return
	}
	req.remember = true
	g.openSignInNamed(req, mac.Name, false)
}

// openSignIn shows the dialog for a request, or a blank one when req is nil.
func (g *gui) openSignIn(req *request, connectNow bool) {
	name := ""
	if req == nil {
		var err error
		req, err = g.app.newRequest(target{}, nil)
		if err != nil {
			g.showError("Could not prepare the connection", err.Error())
			return
		}
		req.remember = true
	} else if req.target.Host != "" {
		name = req.target.Host
	}
	g.openSignInNamed(req, name, connectNow)
}

func (g *gui) openSignInNamed(req *request, name string, connectNow bool) {
	if g.signIn != nil {
		return
	}
	s := &signInDialog{g: g, req: req, name: name}
	s.vnc = req.opts.VNC
	g.signIn = s
	s.build()
	s.dialog.Present(g.window)
	if connectNow {
		s.connect()
	} else if req.target.Host == "" {
		s.address.GrabFocus()
	} else if req.target.User == "" && !s.vnc {
		s.account.GrabFocus()
	} else {
		s.password.GrabFocus()
	}
}

func (s *signInDialog) build() {
	s.dialog = adw.NewDialog()
	s.dialog.SetContentWidth(440)
	s.dialog.SetFollowsContentSize(true)

	icon := tile(tileColor(s.req.target.ID()), true)
	icon.SetMarginTop(6)
	s.title = gtk.NewLabel("")
	s.title.AddCSSClass("title-2")
	s.title.SetWrap(true)
	s.title.SetJustify(gtk.JustifyCenter)
	s.lead = gtk.NewLabel("")
	s.lead.AddCSSClass("dim-label")
	s.lead.SetWrap(true)
	s.lead.SetMaxWidthChars(44)
	s.lead.SetJustify(gtk.JustifyCenter)

	s.address = adw.NewEntryRow()
	s.address.SetTitle("Address")
	s.account = adw.NewEntryRow()
	s.account.SetTitle("Account name")
	s.password = adw.NewPasswordEntryRow()
	s.password.SetTitle("Password")
	group := gtk.NewListBox()
	group.AddCSSClass("boxed-list")
	group.SetSelectionMode(gtk.SelectionNone)
	group.Append(s.address)
	group.Append(s.account)
	group.Append(s.password)

	s.remember = adw.NewSwitchRow()
	s.remember.SetTitle("Remember this Mac")
	s.remember.SetSubtitle("The password is kept in your system keyring")
	s.remember.SetActive(s.req.remember)
	remember := gtk.NewListBox()
	remember.AddCSSClass("boxed-list")
	remember.SetSelectionMode(gtk.SelectionNone)
	remember.Append(s.remember)

	s.problem = gtk.NewLabel("")
	s.problem.AddCSSClass("error")
	s.problem.SetWrap(true)
	s.problem.SetMaxWidthChars(48)
	s.problem.SetXAlign(0)
	s.problem.SetVisible(false)

	s.spinner = gtk.NewSpinner()
	s.spinner.SetVisible(false)
	s.connectLabel = gtk.NewLabel("Connect")
	content := gtk.NewBox(gtk.OrientationHorizontal, 8)
	content.SetHAlign(gtk.AlignCenter)
	content.Append(s.spinner)
	content.Append(s.connectLabel)
	s.connectButton = gtk.NewButton()
	s.connectButton.SetChild(content)
	s.connectButton.AddCSSClass("pill")
	s.connectButton.AddCSSClass("suggested-action")
	s.connectButton.SetHAlign(gtk.AlignFill)
	s.connectButton.ConnectClicked(s.connect)

	s.status = gtk.NewLabel("")
	s.status.AddCSSClass("dim-label")
	s.status.AddCSSClass("caption")
	s.status.SetWrap(true)
	s.status.SetMaxWidthChars(48)
	s.status.SetVisible(false)

	s.switchLogin = gtk.NewButton()
	s.switchLogin.AddCSSClass("link")
	s.switchLogin.SetHAlign(gtk.AlignCenter)
	s.switchLogin.ConnectClicked(func() {
		// The password typed or loaded belonged to the other kind of login.
		s.vnc = !s.vnc
		s.req.switchLogin(s.vnc)
		s.password.SetText("")
		s.applyLoginKind()
		s.password.GrabFocus()
	})

	s.fields = gtk.NewBox(gtk.OrientationVertical, 16)
	s.fields.Append(group)
	s.fields.Append(remember)
	s.fields.Append(s.problem)

	column := gtk.NewBox(gtk.OrientationVertical, 10)
	column.SetMarginStart(24)
	column.SetMarginEnd(24)
	column.SetMarginBottom(24)
	column.Append(icon)
	column.Append(s.title)
	column.Append(s.lead)
	spacer := gtk.NewBox(gtk.OrientationVertical, 0)
	spacer.SetSizeRequest(-1, 6)
	column.Append(spacer)
	column.Append(s.fields)
	column.Append(s.connectButton)
	column.Append(s.status)
	column.Append(s.switchLogin)

	toolbar := adw.NewToolbarView()
	header := adw.NewHeaderBar()
	header.SetShowTitle(false)
	toolbar.AddTopBar(header)
	toolbar.SetContent(column)
	s.dialog.SetChild(toolbar)
	s.dialog.SetDefaultWidget(s.connectButton)

	if s.req.target.Host != "" {
		if s.req.target.Port == defaultPort {
			s.address.SetText(s.req.target.Host)
		} else {
			s.address.SetText(s.req.target.Address())
		}
	}
	s.account.SetText(s.req.target.User)
	s.password.SetText(s.req.dialPassword())
	s.applyLoginKind()

	s.dialog.ConnectClosed(func() {
		if s.dialing && s.dial != nil {
			s.dial.abort()
		}
		s.done = true
		if s.g.signIn == s {
			s.g.signIn = nil
		}
	})
	for _, row := range []interface {
		ConnectEntryActivated(func()) coreglib.SignalHandle
	}{s.address, s.account, s.password} {
		row.ConnectEntryActivated(s.connect)
	}
}

// applyLoginKind switches the dialog between an account login and a VNC password.
func (s *signInDialog) applyLoginKind() {
	name := s.name
	if name == "" {
		name = "a Mac"
	}
	s.title.SetLabel("Sign in to " + name)
	if s.vnc {
		s.lead.SetLabel("This Mac asks for the VNC password set in its Screen Sharing options.")
		s.account.SetVisible(false)
		s.password.SetTitle("VNC password")
		s.switchLogin.SetLabel("This Mac uses an account name and password")
	} else {
		s.lead.SetLabel("Use an account on that Mac, the same one you would use at its login screen.")
		s.account.SetVisible(true)
		s.password.SetTitle("Password")
		s.switchLogin.SetLabel("This Mac uses a VNC password instead")
	}
}

// fail shows a problem inline and shakes the form.
func (s *signInDialog) fail(message string, shake bool) {
	s.problem.SetLabel(message)
	s.problem.SetVisible(true)
	if shake {
		s.fields.RemoveCSSClass("oss-shake")
		s.fields.AddCSSClass("oss-shake")
		coreglib.TimeoutAdd(500, func() bool {
			s.fields.RemoveCSSClass("oss-shake")
			return false
		})
	}
}

func (s *signInDialog) setDialing(active bool) {
	s.dialing = active
	s.spinner.SetVisible(active)
	s.spinner.SetSpinning(active)
	s.connectLabel.SetLabel(map[bool]string{true: "Connecting", false: "Connect"}[active])
	for _, widget := range []gtk.Widgetter{s.address, s.account, s.password, s.remember, s.switchLogin} {
		gtk.BaseWidget(widget).SetSensitive(!active)
	}
	s.connectButton.SetSensitive(!active)
	s.status.SetVisible(active)
	if active {
		s.status.SetLabel("Connecting")
	}
}

func (s *signInDialog) abort() {
	if s.dialing && s.dial != nil {
		s.dial.abort()
	}
}

// connect reads the form and starts the dial.
func (s *signInDialog) connect() {
	if s.dialing || s.done {
		return
	}
	s.problem.SetVisible(false)
	goal, err := parseTarget(s.address.Text())
	if err != nil {
		s.fail("That address does not look right: "+err.Error(), true)
		s.address.GrabFocus()
		return
	}
	if s.vnc {
		goal.User = ""
	} else if account := s.account.Text(); account != "" {
		goal.User = account
	}
	if !s.vnc && goal.User == "" {
		s.fail("Enter the account name of the Mac.", true)
		s.account.GrabFocus()
		return
	}
	req := *s.req
	req.target = goal
	req.setPassword(s.password.Text(), s.vnc)
	req.remember = s.remember.Active()
	req.opts.VNC = s.vnc
	s.req = &req
	s.setDialing(true)
	dial := s.g.app.beginDial(&req)
	s.dial = dial
	go dial.run(dialEvents{
		State:  func(status string) { onMain(func() { s.status.SetLabel(status) }) },
		Prompt: func(prompt *sessionPrompt) { onMain(func() { s.askSession(prompt) }) },
		Done:   func(live *liveSession, err error) { onMain(func() { s.finished(live, err) }) },
	})
}

// askSession asks which session to share when the Mac has a user logged in.
func (s *signInDialog) askSession(prompt *sessionPrompt) {
	dialog := adw.NewAlertDialog("Which screen do you want?", "The Mac has "+quoteName(prompt.request.ConsoleUser)+" logged in.")
	dialog.AddResponse("cancel", "Cancel")
	dialog.AddResponse("own", "Use your own session")
	dialog.AddResponse("console", "Share their screen")
	dialog.SetResponseAppearance("console", adw.ResponseSuggested)
	dialog.SetDefaultResponse("console")
	dialog.SetCloseResponse("cancel")
	dialog.ConnectResponse(func(response string) {
		switch response {
		case "console":
			prompt.answer <- rfb.SessionShareConsole
		case "own":
			prompt.answer <- rfb.SessionOwn
		default:
			prompt.answer <- rfb.SessionCancel
		}
	})
	dialog.Present(s.dialog)
}

func (s *signInDialog) finished(live *liveSession, failure error) {
	s.setDialing(false)
	s.dial = nil
	if failure != nil {
		if s.done {
			return
		}
		address := s.req.target.Address()
		kind, message := describeError(failure, address, s.vnc)
		s.g.app.logger.Warn("connect failed", "address", address, "kind", int(kind), "error", failure)
		if kind == problemCancelled || errors.Is(failure, context.Canceled) {
			return
		}
		if kind == problemPassword {
			s.password.GrabFocus()
		}
		s.fail(message, true)
		return
	}
	if s.done {
		// The window was closed while the connection finished.
		go func() {
			live.cancel()
			_ = live.client.Close()
			<-live.done
		}()
		return
	}
	s.g.app.remember(s.req, live.client.Info().Name)
	s.g.app.touch(s.req)
	s.done = true
	s.dialog.ForceClose()
	s.g.signIn = nil
	s.g.list.reload()
	s.g.startSession(live)
}

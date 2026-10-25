//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	E "github.com/sagernet/sing/common/exceptions"
)

// applicationID is deliberately neutral: it names this viewer, not the
// project whose protocol code it reuses.
const applicationID = "io.github.sharelinux.ScreenSharing"

const windowName = "sharelinux"

// gui is the one application window and everything that hangs off it.
type gui struct {
	app     *app
	adwApp  *adw.Application
	window  *adw.ApplicationWindow
	nav     *adw.NavigationView
	toasts  *adw.ToastOverlay
	list    *connectionsPage
	session *sessionView
	signIn  *signInDialog
	startup *request

	ctx    context.Context
	cancel context.CancelFunc

	// actions that only make sense on the connection list.
	listActions []*gio.SimpleAction
}

// onMain runs f on the GTK main loop. It is how every goroutine talks to a widget.
func onMain(f func()) {
	coreglib.IdleAdd(f)
}

// runGUI opens the window and runs it until it closes.
func runGUI(a *app) error {
	// GTK must always be called from the one OS thread that started it.
	runtime.LockOSThread()
	start, err := a.startRequest()
	if err != nil {
		return err
	}
	prepareHDR(a)
	g := &gui{app: a, startup: start}
	g.ctx, g.cancel = context.WithCancel(context.Background())
	defer g.cancel()
	g.adwApp = adw.NewApplication(applicationID, gio.ApplicationNonUnique)
	g.adwApp.ConnectActivate(g.activate)
	// GTK must not parse our flags: they were read already.
	status := g.adwApp.Run([]string{os.Args[0]})
	if status != 0 {
		return E.New("the window closed with status ", status)
	}
	return nil
}

func (g *gui) activate() {
	if g.window != nil {
		g.window.Present()
		return
	}
	glib.SetPrgname(applicationID)
	glib.SetApplicationName(windowName)
	// The display is open: find out what it offers for HDR on this, the GTK thread.
	g.app.displayHDR()
	gtk.WindowSetDefaultIconName(applicationID)
	// The look is dark: the design has no light variant.
	adw.StyleManagerGetDefault().SetColorScheme(adw.ColorSchemeForceDark)
	// The design's symbolic icons and Mac art are unpacked next to the stock ones.
	// The rest of the stock icons are Adwaita's; other icon themes draw some of
	// them differently or not at all, so the window pins this one.
	gtk.SettingsGetDefault().SetObjectProperty("gtk-icon-theme-name", "Adwaita")
	if err := installIcons(gdk.DisplayGetDefault()); err != nil {
		g.app.logger.Warn("icons", "error", err)
	}
	provider := gtk.NewCSSProvider()
	provider.LoadFromString(stylesheet)
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), provider, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)

	g.window = adw.NewApplicationWindow(&g.adwApp.Application)
	g.window.SetTitle(windowName)
	g.window.SetDefaultSize(g.app.opts.WindowWidth, g.app.opts.WindowHeight)
	g.window.SetSizeRequest(360, 300)
	if g.app.opts.Fullscreen {
		g.window.Fullscreen()
	}
	g.nav = adw.NewNavigationView()
	g.toasts = adw.NewToastOverlay()
	g.toasts.SetChild(g.nav)
	g.window.SetContent(g.toasts)

	g.list = newConnectionsPage(g)
	g.nav.Add(g.list.page)
	g.list.installBreakpoint()
	g.nav.ConnectPopped(func(page *adw.NavigationPage) {
		if g.session != nil && page.Tag() == sessionPageTag {
			g.session.end()
			g.session = nil
			g.setListActions(true)
		}
	})
	g.installActions()
	g.window.ConnectCloseRequest(func() bool {
		g.shutdown()
		return false
	})
	// GTK has read GDK_DEBUG and GSK_RENDERER once the window is mapped: nothing it starts needs them.
	g.window.ConnectMap(func() { releaseGTKEnv(g.app) })
	g.window.Present()
	g.list.start()

	if g.startup != nil {
		g.openSignIn(g.startup, g.startup.dialPassword() != "")
	}
}

// shutdown stops everything that runs in the background.
func (g *gui) shutdown() {
	if g.session != nil {
		g.session.captureLast()
		g.session.end()
		g.session = nil
	}
	if g.signIn != nil {
		g.signIn.abort()
	}
	g.list.stop()
	g.cancel()
}

// installActions registers the window and application actions and their shortcuts.
func (g *gui) installActions() {
	add := func(name string, accels []string, onList bool, f func()) *gio.SimpleAction {
		action := gio.NewSimpleAction(name, nil)
		action.ConnectActivate(func(*glib.Variant) { f() })
		g.window.AddAction(action)
		if len(accels) > 0 {
			g.adwApp.SetAccelsForAction("win."+name, accels)
		}
		if onList {
			g.listActions = append(g.listActions, action)
		}
		return action
	}
	add("new-connection", []string{"<Control>n"}, true, func() { g.openSignIn(nil, false) })
	add("search", []string{"<Control>f"}, true, func() { g.list.focusSearch() })
	add("preferences", []string{"<Control>comma"}, false, g.showPreferences)
	add("about", nil, false, g.showAbout)
	add("quit", []string{"<Control>q"}, true, func() { g.window.Close() })
}

// setListActions enables the shortcuts that belong to the connection list, and
// turns them off while a session shows so its keys reach the Mac.
func (g *gui) setListActions(enabled bool) {
	for _, action := range g.listActions {
		action.SetEnabled(enabled)
	}
}

// newToast is a toast that shows its text as written. A toast reads its title
// as Pango markup unless told not to, and the text here includes names from
// the outside (folders, Macs, error messages): "a < b" or "<b>" must show as is.
func newToast(message string) *adw.Toast {
	toast := adw.NewToast(message)
	toast.SetUseMarkup(false)
	toast.SetTimeout(4)
	return toast
}

// toast shows a short message over the current page.
func (g *gui) toast(message string) {
	g.toasts.AddToast(newToast(message))
}

// thumbnailFile is where the last frame of a Mac is kept, or "" when the cache
// folder is unknown.
func (g *gui) thumbnailFile(id string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return thumbnailPath(filepath.Join(base, configName), id)
}

func (g *gui) showAbout() {
	about := adw.NewAboutDialog()
	about.SetApplicationName(windowName)
	about.SetApplicationIcon(applicationID)
	about.SetComments("View and control the screen of a Mac from Linux.")
	about.SetLicenseType(gtk.LicenseGPL30)
	about.Present(g.window)
}

// showError tells the person about something that went wrong, in a dialog.
func (g *gui) showError(heading, body string) {
	dialog := adw.NewAlertDialog(heading, body)
	dialog.AddResponse("ok", "OK")
	dialog.SetDefaultResponse("ok")
	dialog.SetCloseResponse("ok")
	dialog.Present(g.window)
}

// lastElement is the last path element.
func lastElement(path string) string {
	for len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '/' && index < len(path)-1 {
			return path[index+1:]
		}
	}
	return path
}

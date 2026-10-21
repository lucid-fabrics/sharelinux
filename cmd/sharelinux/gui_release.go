//go:build linux

package main

import (
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// destroyTree ends the Go side of a widget tree that will never be shown again:
// the widget, its event controllers and every widget below it. Each handler on
// them holds a Go closure, and the closures hold the session, its framebuffer
// and the widgets themselves, which the collector cannot free as a cycle. Destroy
// takes the handlers off and lets the objects finish, so a popped page frees its
// picture instead of staying in memory for the life of the program. The tree
// must not be used afterwards.
func destroyTree(root gtk.Widgetter) {
	var objects []coreglib.Objector
	var collect func(widget gtk.Widgetter)
	collect = func(widget gtk.Widgetter) {
		base := gtk.BaseWidget(widget)
		objects = append(objects, widget)
		controllers := base.ObserveControllers()
		for index := uint(0); index < controllers.NItems(); index++ {
			objects = append(objects, controllers.Item(index))
		}
		// Not FirstChild and NextSibling: past the last child they hand a null
		// pointer to the bindings, which the race detector's pointer checks reject.
		children := base.ObserveChildren()
		for index := uint(0); index < children.NItems(); index++ {
			collect(widgetOf(children.Item(index)))
		}
	}
	collect(root)
	// Children first: a parent that is freed takes its children down with it.
	for index := len(objects) - 1; index >= 0; index-- {
		coreglib.Destroy(objects[index])
	}
}

// widgetOf wraps any object that is a widget as a plain gtk.Widget, filled the
// way the bindings' own code does. Cast would find the exact type but fails the
// race detector's pointer checks inside the bindings; the base type is all the
// walk needs.
func widgetOf(object *coreglib.Object) *gtk.Widget {
	return &gtk.Widget{
		InitiallyUnowned: coreglib.InitiallyUnowned{Object: object},
		Object:           object,
		Accessible:       gtk.Accessible{Object: object},
		Buildable:        gtk.Buildable{Object: object},
		ConstraintTarget: gtk.ConstraintTarget{Object: object},
	}
}

// releaseTimeout is the longest a popped page waits for its transition to
// finish before it is released anyway.
const releaseTimeout = 2000

// releaseAfterTransition runs release once the page has left the screen. The
// popped signal fires when the pop starts, while the page still animates out
// and can still deliver input and drawing callbacks, so tearing the tree down
// there pulls widgets from under live handlers. The hidden signal says the
// transition is over; release then runs from an idle callback, not inside the
// signal that is still being emitted by the page about to be destroyed. A
// timeout covers a page that never reports hidden, for example one in a
// window that is closing. release runs at most once.
func releaseAfterTransition(page *adw.NavigationPage, release func()) {
	done := false
	run := func() {
		if done {
			return
		}
		done = true
		release()
	}
	page.ConnectHidden(func() {
		coreglib.IdleAdd(run)
	})
	coreglib.TimeoutAdd(releaseTimeout, func() bool {
		run()
		return false
	})
}

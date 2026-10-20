//go:build linux

package main

import coreglib "github.com/diamondburned/gotk4/pkg/core/glib"

// handlerOwner is any object a handler can be taken off again.
type handlerOwner interface {
	HandlerDisconnect(handle coreglib.SignalHandle)
}

type scopedHandler struct {
	owner  handlerOwner
	handle coreglib.SignalHandle
}

// signalScope remembers the handlers a session put on objects that outlive it
// (the window, the clipboard, the display). A handler left on such an object
// keeps the session, and the picture it holds, alive and keeps firing.
type signalScope struct {
	handlers []scopedHandler
}

// add records a handler connected to owner.
func (s *signalScope) add(owner handlerOwner, handle coreglib.SignalHandle) {
	s.handlers = append(s.handlers, scopedHandler{owner: owner, handle: handle})
}

// release takes every recorded handler off. It is safe to call twice.
func (s *signalScope) release() {
	for _, item := range s.handlers {
		item.owner.HandlerDisconnect(item.handle)
	}
	s.handlers = nil
}

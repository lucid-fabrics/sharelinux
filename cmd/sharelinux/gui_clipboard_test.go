//go:build linux

package main

import "testing"

// In View mode the person only watches: what they copy locally stays local.
// The gate comes before any read of the clipboard, so a bridge without a
// clipboard or a client survives the call.
func TestClipboardToMacIsGatedByControl(t *testing.T) {
	controlling := false
	bridge := &clipboardBridge{allow: func() bool { return controlling }}
	if bridge.mayCopyToMac() {
		t.Fatal("View mode must not send the local clipboard")
	}
	bridge.toMac() // would dereference the nil clipboard if the gate did not stop it

	controlling = true
	if !bridge.mayCopyToMac() {
		t.Fatal("Control and Exclusive send the local clipboard")
	}

	bridge.closed = true
	if bridge.mayCopyToMac() {
		t.Fatal("a closed bridge sends nothing")
	}
	bridge = &clipboardBridge{}
	if bridge.mayCopyToMac() {
		t.Fatal("without a gate nothing is sent")
	}
}

// What was copied while only viewing stays local, also after switching to
// Control. The next copy goes through.
func TestClipboardCopiedWhileViewingStaysLocal(t *testing.T) {
	controlling := false
	bridge := &clipboardBridge{allow: func() bool { return controlling }}
	if bridge.noteLocalChange() {
		t.Fatal("a copy made in View mode must not be sent")
	}
	controlling = true
	if bridge.mayCopyToMac() {
		t.Fatal("entering Control must not send what was copied in View mode")
	}
	if !bridge.noteLocalChange() || !bridge.mayCopyToMac() {
		t.Fatal("a new copy in Control mode goes to the Mac")
	}
}

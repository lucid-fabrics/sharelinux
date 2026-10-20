package main

import "testing"

func TestClipboardCallbacksCreatedOnce(t *testing.T) {
	data1, cleanup1 := clipboardCallbacks()
	first := newClipboardBridge(nil)
	data2, cleanup2 := clipboardCallbacks()
	second := newClipboardBridge(nil)
	if data1 != data2 || cleanup1 != cleanup2 {
		t.Fatal("purego callbacks are never freed, so they are made once per process")
	}
	if activeBridge.Load() != second {
		t.Fatal("the newest bridge serves the callbacks")
	}
	first.close()
	if activeBridge.Load() != second {
		t.Fatal("closing an old bridge must not unplug the new one")
	}
	second.close()
	if activeBridge.Load() != nil {
		t.Fatal("closed bridge is unplugged")
	}
}

func TestClipboardBridgeServesOffer(t *testing.T) {
	bridge := newClipboardBridge(nil)
	defer bridge.close()
	bridge.offer = map[string][]byte{mimePNG: {1, 2}}
	if got := bridge.data(mimePNG); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if bridge.data(mimeText) != nil {
		t.Fatal("unknown type has no data")
	}
}

package main

import "testing"

func TestTransferTitlePhases(t *testing.T) {
	tests := []struct {
		name  string
		state transferState
		title string
		value string
	}{
		{
			name:  "preparing",
			state: transferState{Name: "oss-drop-test.txt", Phase: phasePreparing},
			title: "Preparing oss-drop-test.txt",
			value: "",
		},
		{
			name:  "copying upload",
			state: transferState{Name: "x", Percent: 40, Upload: true},
			title: "Copying x to the Mac",
			value: "40%",
		},
		{
			name:  "copying download",
			state: transferState{Name: "x", Percent: 40},
			title: "Copying x from the Mac",
			value: "40%",
		},
		{
			name:  "done upload",
			state: transferState{Name: "x", Phase: phaseDone, Upload: true, Folder: "Documents"},
			title: "Sent to the Mac",
			value: "Documents",
		},
		{
			name:  "done download",
			state: transferState{Name: "x", Phase: phaseDone, Folder: "Downloads"},
			title: "Saved to Downloads",
			value: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.title(); got != test.title {
				t.Errorf("title() = %q, want %q", got, test.title)
			}
			if got := test.state.value(); got != test.value {
				t.Errorf("value() = %q, want %q", got, test.value)
			}
		})
	}
}

func TestDropName(t *testing.T) {
	if got, want := dropName([]string{"/a/b/oss-drop-test.txt"}), "oss-drop-test.txt"; got != want {
		t.Errorf("dropName() = %q, want %q", got, want)
	}

	if got, want := dropName([]string{"/a/one", "/b/two", "/c/three"}), "3 items"; got != want {
		t.Errorf("dropName() = %q, want %q", got, want)
	}
}

func TestDropShowsOneIndicator(t *testing.T) {
	transfers := &transfers{handler: newHandler(nil, nil)}
	paths := []string{"/tmp/oss-drop-test.txt"}

	transfers.announceDrop(paths)
	state, changed := transfers.handler.takeTransfer()
	if !changed {
		t.Fatal("takeTransfer() reported no change after announceDrop")
	}
	if !state.Active {
		t.Error("announceDrop() left the transfer card inactive")
	}
	if state.Phase != phasePreparing {
		t.Errorf("phase = %v, want %v", state.Phase, phasePreparing)
	}
	if !state.Upload {
		t.Error("announceDrop() did not mark the transfer as an upload")
	}
	if state.Name != "oss-drop-test.txt" {
		t.Errorf("name = %q, want %q", state.Name, "oss-drop-test.txt")
	}
	if toasts := transfers.handler.takeToasts(); len(toasts) != 0 {
		t.Fatalf("takeToasts() = %v, want no toasts", toasts)
	}

	transfers.finishDrop(paths, "/Users/me/Documents")
	state, changed = transfers.handler.takeTransfer()
	if !changed {
		t.Fatal("takeTransfer() reported no change after finishDrop")
	}
	if state.Phase != phaseDone {
		t.Errorf("phase = %v, want %v", state.Phase, phaseDone)
	}
	if !state.Active {
		t.Error("finishDrop() left the transfer card inactive")
	}
	if state.Folder != "Documents" {
		t.Errorf("folder = %q, want %q", state.Folder, "Documents")
	}
	if state.title() != "Sent to the Mac" {
		t.Errorf("title() = %q, want %q", state.title(), "Sent to the Mac")
	}
	if toasts := transfers.handler.takeToasts(); len(toasts) != 0 {
		t.Fatalf("takeToasts() = %v, want no toasts", toasts)
	}
}

func TestFailDropClearsCardAndToasts(t *testing.T) {
	transfers := &transfers{handler: newHandler(nil, nil)}
	transfers.announceDrop([]string{"/tmp/oss-drop-test.txt"})
	transfers.failDrop("Cannot drop: x")

	state, changed := transfers.handler.takeTransfer()
	if !changed {
		t.Fatal("takeTransfer() reported no change after failDrop")
	}
	if state.Active {
		t.Error("failDrop() left the transfer card active")
	}

	toasts := transfers.handler.takeToasts()
	if len(toasts) != 1 || toasts[0] != "Cannot drop: x" {
		t.Fatalf("takeToasts() = %v, want [%q]", toasts, "Cannot drop: x")
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/filecopy"
	E "github.com/sagernet/sing/common/exceptions"
)

const dropTimeout = 15 * time.Second

// downloadDirectory is where files from the Mac land by default: XDG downloads, else ~/Downloads, else here.
func downloadDirectory() string {
	if directory := os.Getenv("XDG_DOWNLOAD_DIR"); directory != "" {
		return directory
	}
	home, err := os.UserHomeDir()
	if err == nil {
		directory := filepath.Join(home, "Downloads")
		info, statErr := os.Stat(directory)
		if statErr == nil && info.IsDir() {
			return directory
		}
	}
	return "."
}

// dropNames marks folders with a trailing slash, as the Mac's drag helper expects.
func dropNames(paths []string) ([]string, error) {
	named := make([]string, len(paths))
	for index, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		named[index] = path
		if info.IsDir() {
			named[index] += "/"
		}
	}
	return named, nil
}

// transfers runs file copies in the background and reports through toasts.
type transfers struct {
	client  *rfb.Client
	handler *handler
	context context.Context
	// folder returns the folder for files from the Mac.
	folder func() string
	// gesture holds back live pointer events while a drop plays its own presses.
	gesture pointerGate

	access    sync.Mutex
	current   *filecopy.Transfer
	cancelled bool
}

// transferPhase is where a copy is, which decides what the copy card says.
type transferPhase int

const (
	// phaseCopying is the zero value: bytes are moving and Percent counts them.
	phaseCopying transferPhase = iota
	// phasePreparing is a drop waiting for the Mac to say where it lands.
	phasePreparing
	// phaseDone is a finished copy. The card says so for a moment, then goes.
	phaseDone
)

// transferState is what the copy card shows.
type transferState struct {
	Name    string
	Percent int
	Upload  bool
	Phase   transferPhase
	// Folder is where a finished copy went.
	Folder string
	// Active is false once the copy has ended, so the card can go away.
	Active bool
}

// title is the first line of the copy card.
func (s transferState) title() string {
	switch {
	case s.Phase == phasePreparing:
		return "Preparing " + s.Name
	case s.Phase == phaseDone && s.Upload:
		return "Sent to the Mac"
	case s.Phase == phaseDone:
		return "Saved to " + s.Folder
	case s.Upload:
		return "Copying " + s.Name + " to the Mac"
	}
	return "Copying " + s.Name + " from the Mac"
}

// value is the second line of the copy card: the progress, or where a sent copy landed.
func (s transferState) value() string {
	switch {
	case s.Phase == phasePreparing:
		return ""
	case s.Phase == phaseDone && s.Upload:
		return s.Folder
	case s.Phase == phaseDone:
		return ""
	}
	return fmt.Sprintf("%d%%", s.Percent)
}

// dropName is what the card calls the files of a drop.
func dropName(paths []string) string {
	if len(paths) == 1 {
		return filepath.Base(paths[0])
	}
	return pluralize(len(paths), "item")
}

var errCopyCancelled = E.New("the copy was cancelled")

// pointerGate lets one writer own the Mac's pointer for a while. The view skips
// its own pointer events while the gate is held, so they cannot interleave with
// the synthetic ones.
type pointerGate struct {
	held atomic.Bool
}

func (g *pointerGate) Begin()        { g.held.Store(true) }
func (g *pointerGate) End()          { g.held.Store(false) }
func (g *pointerGate) Blocked() bool { return g.held.Load() }

// dropGesture presses, drags a pixel and releases the left button at x, y, which
// is how the Mac learns where the dropped files should land.
func dropGesture(send func(x, y int, buttons uint8) error, wait func(time.Duration), x, y int) {
	_ = send(x, y, rfb.ButtonLeft)
	wait(150 * time.Millisecond)
	_ = send(x+1, y+1, rfb.ButtonLeft)
	wait(150 * time.Millisecond)
	_ = send(x+1, y+1, 0)
}

// progressInterval is the least time between two reports of one copy.
const progressInterval = 100 * time.Millisecond

// newProgressReporter returns the progress callback of one copy. It is never
// nil because the file copy core calls it unconditionally. It reports at most
// once per interval and only while the total is known.
func newProgressReporter(report func(transferState), now func() time.Time, upload bool) func(filecopy.Progress) {
	var last time.Time
	return func(progress filecopy.Progress) {
		if progress.Total <= 0 {
			return
		}
		current := now()
		if progress.Transferred < progress.Total && !last.IsZero() && current.Sub(last) < progressInterval {
			return
		}
		last = current
		report(transferState{
			Name:    filepath.Base(progress.Item),
			Percent: int(min(progress.Transferred*100/progress.Total, 100)),
			Upload:  upload,
			Active:  true,
		})
	}
}

// cancelCopy stops the copy that runs now, and the rest of its batch.
func (t *transfers) cancelCopy() {
	t.access.Lock()
	t.cancelled = true
	current := t.current
	t.access.Unlock()
	if current != nil {
		_ = current.Cancel()
	}
}

func (t *transfers) wasCancelled() bool {
	t.access.Lock()
	defer t.access.Unlock()
	return t.cancelled
}

func (t *transfers) beginBatch() {
	t.access.Lock()
	t.cancelled = false
	t.access.Unlock()
}

// copyOne uploads or downloads one item and waits for it to finish.
func (t *transfers) copyOne(upload bool, source, destination string) error {
	var transfer *filecopy.Transfer
	var err error
	progress := newProgressReporter(t.handler.setTransfer, time.Now, upload)
	if t.wasCancelled() {
		return errCopyCancelled
	}
	if upload {
		transfer, err = t.client.FileCopy().Upload(t.context, filecopy.PathSource(source), destination, progress)
	} else {
		transfer, err = t.client.FileCopy().Download(t.context, source, destination, progress)
	}
	if err != nil {
		return err
	}
	t.access.Lock()
	t.current = transfer
	t.access.Unlock()
	_, err = transfer.Wait(t.context)
	t.access.Lock()
	t.current = nil
	cancelled := t.cancelled
	t.access.Unlock()
	if cancelled {
		return errCopyCancelled
	}
	return err
}

// finishCopies ends the copy card and says how the batch went.
func (t *transfers) finishCopies(message string, err error) {
	t.handler.setTransfer(transferState{})
	switch {
	case errors.Is(err, errCopyCancelled):
		t.handler.toast("Copy cancelled")
	case err != nil:
		t.handler.toast(message + ": " + err.Error())
	}
}

// announceDrop puts the drop on the copy card before anything else happens, so
// the whole action has the one indicator and no toast beside it.
func (t *transfers) announceDrop(paths []string) {
	t.handler.setTransfer(transferState{Name: dropName(paths), Upload: true, Phase: phasePreparing, Active: true})
}

// finishDrop turns the card into the confirmation of a finished drop.
func (t *transfers) finishDrop(paths []string, destination string) {
	t.handler.setTransfer(transferState{Name: dropName(paths), Upload: true, Phase: phaseDone, Folder: filepath.Base(destination), Percent: 100, Active: true})
}

// failDrop takes the card away and says why.
func (t *transfers) failDrop(message string) {
	t.handler.setTransfer(transferState{})
	t.handler.toast(message)
}

// drop sends files that were dropped on the window to the Mac. The Mac decides
// where they land: the drag is started over the pointer position and released,
// and the Mac answers with the folder it was released over.
func (t *transfers) drop(paths []string, x, y int) {
	t.beginBatch()
	t.announceDrop(paths)
	t.gesture.Begin()
	go func() {
		defer t.gesture.End()
		named, err := dropNames(paths)
		if err != nil {
			t.failDrop("Cannot drop: " + err.Error())
			return
		}
		id, err := t.client.SendDrag(rfb.NewFileClipboard(named))
		if err != nil {
			t.failDrop("Cannot drop: " + err.Error())
			return
		}
		dropGesture(t.client.SendPointer, time.Sleep, x, y)
		t.gesture.End()
		destination, err := t.waitDestination(id)
		if err != nil {
			_ = t.client.CancelDrag(id)
			t.failDrop(err.Error())
			return
		}
		for _, path := range paths {
			err = t.copyOne(true, path, destination)
			if err != nil {
				t.finishCopies("Upload failed", err)
				return
			}
		}
		t.finishDrop(paths, destination)
	}()
}

func (t *transfers) waitDestination(id uint32) (string, error) {
	deadline := time.NewTimer(dropTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			path, found := t.handler.takeDestination(id)
			if found {
				return path, nil
			}
		case <-deadline.C:
			return "", E.New("the Mac did not say where the files were dropped")
		case <-t.context.Done():
			return "", t.context.Err()
		}
	}
}

// fetch downloads the files of the Mac's own drag into the downloads folder.
func (t *transfers) fetch(drag *rfb.RemoteDrag) {
	t.beginBatch()
	go func() {
		var files []string
		for _, item := range drag.Items {
			files = append(files, item.Path)
		}
		if len(files) == 0 {
			t.handler.toast("The Mac is not dragging any files")
			return
		}
		directory := t.folder()
		for _, path := range files {
			err := t.copyOne(false, path, directory)
			if err != nil {
				t.finishCopies("Download failed", err)
				return
			}
		}
		t.handler.setTransfer(transferState{Name: pluralize(len(files), "item"), Phase: phaseDone, Folder: filepath.Base(directory), Percent: 100, Active: true})
	}()
}

package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

type countingMemorySecrets struct {
	memorySecrets
	getCalls int
}

func (s *countingMemorySecrets) Get(id string) (string, error) {
	s.getCalls++
	return s.memorySecrets.Get(id)
}

func TestSwitchLoginDropsPassword(t *testing.T) {
	req := &request{target: target{User: "me"}}
	req.setPassword("x", false)

	req.switchLogin(true)

	if req.password != "" {
		t.Fatalf("password was not dropped: %q", req.password)
	}
	if !req.opts.VNC {
		t.Fatal("VNC login was not enabled")
	}
}

func TestDialPasswordNeverCrossesKinds(t *testing.T) {
	accountCrossed := &request{target: target{User: "me"}}
	accountCrossed.setPassword("p", false)
	accountCrossed.opts.VNC = true
	if password := accountCrossed.dialPassword(); password != "" {
		t.Fatalf("account password was used as a VNC password: %q", password)
	}

	vnc := &request{target: target{}}
	vnc.setPassword("v", true)
	if password := vnc.dialPassword(); password != "v" {
		t.Fatalf("VNC password = %q, want %q", password, "v")
	}

	account := &request{target: target{User: "me"}}
	account.setPassword("p", false)
	if password := account.dialPassword(); password != "p" {
		t.Fatalf("account password = %q, want %q", password, "p")
	}
}

func TestRFBOptionsPasswordFollowsKind(t *testing.T) {
	req := &request{
		target: target{User: "me"},
		opts:   options{},
	}
	req.setPassword("secret", false)

	a := &app{opts: &options{}}
	got := a.rfbOptions(req, newHandler(nil, nil)).Password
	want := req.dialPassword()
	if got != want {
		t.Fatalf("RFB password = %q, want dial password %q", got, want)
	}
}

func TestLoadSavedPasswordSkipsOtherKind(t *testing.T) {
	t.Run("other kind", func(t *testing.T) {
		secrets := &countingMemorySecrets{memorySecrets: memorySecrets{}}
		saved := savedConnection{User: "me", Host: "mac", Port: 5900}
		secrets.memorySecrets[saved.target().ID()] = "account-password"

		a := &app{
			opts:  &options{},
			store: newStore(filepath.Join(t.TempDir(), "c.json"), secrets),
		}
		req := &request{
			target: saved.target(),
			opts:   options{VNC: true},
		}

		a.loadSavedPassword(req, saved)

		if req.password != "" {
			t.Fatalf("password for the other login kind was loaded: %q", req.password)
		}
		if secrets.getCalls != 0 {
			t.Fatalf("keyring Get was called %d times", secrets.getCalls)
		}
	})

	t.Run("same account kind", func(t *testing.T) {
		secrets := &countingMemorySecrets{memorySecrets: memorySecrets{}}
		saved := savedConnection{User: "me", Host: "mac", Port: 5900}
		secrets.memorySecrets[saved.target().ID()] = "account-password"

		a := &app{
			opts:  &options{},
			store: newStore(filepath.Join(t.TempDir(), "c.json"), secrets),
		}
		req := &request{
			target: saved.target(),
			opts:   options{VNC: false},
		}

		a.loadSavedPassword(req, saved)

		if password := req.dialPassword(); password != "account-password" {
			t.Fatalf("dial password = %q, want %q", password, "account-password")
		}
	})

	t.Run("same VNC kind", func(t *testing.T) {
		secrets := &countingMemorySecrets{memorySecrets: memorySecrets{}}
		saved := savedConnection{Host: "mac", Port: 5900, VNC: true}
		secrets.memorySecrets[saved.target().ID()] = "vnc-password"

		a := &app{
			opts:  &options{},
			store: newStore(filepath.Join(t.TempDir(), "c.json"), secrets),
		}
		req := &request{
			target: saved.target(),
			opts:   options{VNC: true},
		}

		a.loadSavedPassword(req, saved)

		if password := req.dialPassword(); password != "vnc-password" {
			t.Fatalf("dial password = %q, want %q", password, "vnc-password")
		}
	})
}

// The flaw this pins: an account password typed (or loaded) before the person
// flips to "VNC password" must not reach the wire as a VNC password.
func TestAccountPasswordNeverSentAsVNCPassword(t *testing.T) {
	req := &request{target: target{User: "me", Host: "mac", Port: 5900}}
	req.setPassword("account-secret", false)

	// Whatever flipped the kind without going through switchLogin.
	req.opts.VNC = true
	a := &app{opts: &options{}}
	if got := a.rfbOptions(req, newHandler(nil, nil)).Password; got != "" {
		t.Fatalf("an account password went out as a VNC password: %q", got)
	}

	// Through switchLogin the password is gone and a new VNC one is used.
	req.setPassword("account-secret", false)
	req.switchLogin(true)
	req.setPassword("vnc-secret", true)
	if got := a.rfbOptions(req, newHandler(nil, nil)).Password; got != "vnc-secret" {
		t.Fatalf("the VNC password entered after the switch: %q", got)
	}
}

func TestRememberKeepsTheSavedModeAfterAOneTimeMode(t *testing.T) {
	a := &app{opts: &options{}, store: newStore(filepath.Join(t.TempDir(), "c.json"), memorySecrets{}), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	saved := savedConnection{Name: "Studio", Host: "mac", Port: 5900, VNC: true, Mode: "control"}
	if err := a.store.Save(saved, ""); err != nil {
		t.Fatal(err)
	}
	req, err := a.newRequest(saved.target(), &saved)
	if err != nil {
		t.Fatal(err)
	}
	req.opts.Mode = rfb.ModeExclusiveControl
	req.modeOnce = true
	a.remember(req, "Studio")
	items, _ := a.store.List()
	if len(items) != 1 || items[0].Mode != "control" {
		t.Fatalf("the Exclusive button must not change the saved mode: %+v", items)
	}

	req.modeOnce = false
	a.remember(req, "Studio")
	items, _ = a.store.List()
	if items[0].Mode != rfb.ModeExclusiveControl.String() {
		t.Fatalf("a mode chosen as usual is saved: %+v", items)
	}
}

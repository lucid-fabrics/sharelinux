package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type memorySecrets map[string]string

func (m memorySecrets) Get(id string) (string, error) {
	value, found := m[id]
	if !found {
		return "", errNoSecret
	}
	return value, nil
}
func (m memorySecrets) Set(id, password string) error { m[id] = password; return nil }
func (m memorySecrets) Delete(id string) error        { delete(m, id); return nil }

type failingSecrets struct{ memorySecrets }

func (failingSecrets) Set(string, string) error { return errors.New("locked") }

func TestStoreSaveListRemove(t *testing.T) {
	secrets := memorySecrets{}
	s := newStore(filepath.Join(t.TempDir(), "sub", "connections.json"), secrets)
	if items, err := s.List(); err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	conn := savedConnection{User: "me", Host: "mac", Port: 5900, Mode: "observe"}
	if err := s.Save(conn, "hunter2"); err != nil {
		t.Fatal(err)
	}
	conn.Mode = "control"
	if err := s.Save(conn, ""); err != nil {
		t.Fatal(err)
	}
	items, _ := s.List()
	if len(items) != 1 || items[0].Mode != "control" || items[0].Name != "mac:5900" {
		t.Fatalf("%+v", items)
	}
	password, err := s.Password(items[0])
	if err != nil || password != "hunter2" {
		t.Fatal("an empty password keeps the saved one", password, err)
	}
	found, ok := s.Find("ME@mac:5900")
	if !ok || found.Host != "mac" {
		t.Fatal("find by address")
	}
	if _, ok = s.Find("MAC:5900"); !ok {
		t.Fatal("find by name")
	}
	if err = s.Remove(items[0]); err != nil {
		t.Fatal(err)
	}
	if items, _ = s.List(); len(items) != 0 {
		t.Fatal(items)
	}
	if _, err = s.Password(conn); !errors.Is(err, errNoSecret) {
		t.Fatal("removing forgets the password", err)
	}
}

func TestStoreNeverWritesPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	s := newStore(path, memorySecrets{})
	if err := s.Save(savedConnection{User: "me", Host: "mac", Port: 5900}, "TOPSECRET"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "TOPSECRET") {
		t.Fatal("password reached the config file")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
}

func TestStoreKeyringFailureStillSaves(t *testing.T) {
	s := newStore(filepath.Join(t.TempDir(), "c.json"), failingSecrets{memorySecrets{}})
	err := s.Save(savedConnection{User: "me", Host: "mac", Port: 5900}, "pw")
	if err == nil || !strings.Contains(err.Error(), "keyring") {
		t.Fatal(err)
	}
	if items, _ := s.List(); len(items) != 1 {
		t.Fatal("connection is saved even if the keyring refuses")
	}
}

func TestStoreCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(path, []byte("{nope"), 0o600)
	if _, err := newStore(path, memorySecrets{}).List(); err == nil {
		t.Fatal("corrupt file must be reported")
	}
}

func TestKeyringSecrets(t *testing.T) {
	keyring.MockInit()
	var k keyringSecrets
	if _, err := k.Get("x@y:1"); !errors.Is(err, errNoSecret) {
		t.Fatal(err)
	}
	if err := k.Set("x@y:1", "pw"); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get("x@y:1"); err != nil || got != "pw" {
		t.Fatal(got, err)
	}
	if err := k.Delete("x@y:1"); err != nil {
		t.Fatal(err)
	}
	if err := k.Delete("x@y:1"); err != nil {
		t.Fatal("deleting twice is fine", err)
	}
}

func TestApplyKeepsExplicitFlags(t *testing.T) {
	opts := &options{Mode: 1, Explicit: map[string]bool{"mode": true}, Keyboard: "auto"}
	conn := savedConnection{Mode: "observe", Quality: "low", Keyboard: "iso", VNC: true}
	if err := conn.apply(opts); err != nil {
		t.Fatal(err)
	}
	if opts.Mode != 1 || opts.Quality.String() != "low" || opts.Keyboard != "iso" || !opts.VNC {
		t.Fatalf("%+v", opts)
	}
	bad := savedConnection{Mode: "bogus"}
	if bad.apply(&options{Explicit: map[string]bool{}}) == nil {
		t.Fatal("bad saved mode must fail")
	}
}

func TestConfigDirectoryMigratesTheOldName(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, legacyConfigName)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "connections.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := configDirectoryIn(base)
	if directory != filepath.Join(base, configName) {
		t.Fatalf("%q", directory)
	}
	if _, err := os.Stat(filepath.Join(directory, "connections.json")); err != nil {
		t.Fatal("the saved Macs moved to the new folder")
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("the old folder is gone")
	}
	// A second call changes nothing, and an existing new folder is never overwritten.
	configDirectoryIn(base)
	if _, err := os.Stat(filepath.Join(directory, "connections.json")); err != nil {
		t.Fatal("a second call keeps the folder")
	}
}

func TestStoreSaveKeepsWhatTheHomeScreenLearned(t *testing.T) {
	s := newStore(filepath.Join(t.TempDir(), "connections.json"), memorySecrets{})
	conn := savedConnection{User: "me", Host: "mac", Port: 5900, Model: "MacBookPro18,2", Art: "macbook", Last: &lastSession{Width: 2560, Height: 1600, Codec: "HEVC"}}
	if err := s.Save(conn, ""); err != nil {
		t.Fatal(err)
	}
	// A sign-in saves a fresh record that knows nothing of the model or the last session.
	if err := s.Save(savedConnection{User: "me", Host: "mac", Port: 5900, Mode: "control"}, ""); err != nil {
		t.Fatal(err)
	}
	items, _ := s.List()
	if len(items) != 1 || items[0].Model != "MacBookPro18,2" || items[0].Art != "macbook" || items[0].Last == nil || items[0].Last.Width != 2560 || items[0].Mode != "control" {
		t.Fatalf("%+v", items)
	}
}

func TestStoreUpdate(t *testing.T) {
	secrets := memorySecrets{}
	s := newStore(filepath.Join(t.TempDir(), "connections.json"), secrets)
	conn := savedConnection{User: "me", Host: "mac", Port: 5900}
	if err := s.Save(conn, "pw"); err != nil {
		t.Fatal(err)
	}
	found, err := s.Update(conn.target(), func(c *savedConnection) { c.Model = "iMac20,1" })
	if err != nil || !found {
		t.Fatal(found, err)
	}
	items, _ := s.List()
	if items[0].Model != "iMac20,1" {
		t.Fatalf("%+v", items)
	}
	if secrets[conn.target().ID()] != "pw" {
		t.Fatal("the password must stay")
	}
	found, err = s.Update(target{Host: "other", Port: 5900}, func(*savedConnection) { t.Fatal("called") })
	if err != nil || found {
		t.Fatal("an unknown Mac is not updated", found, err)
	}
}

func TestStoreRemoveDeletesThumbnail(t *testing.T) {
	cache := t.TempDir()
	s := newStore(filepath.Join(t.TempDir(), "c.json"), memorySecrets{}).withCache(cache)
	conn := savedConnection{User: "me", Host: "mac", Port: 5900, Mode: "observe"}
	other := savedConnection{User: "me", Host: "other", Port: 5900, Mode: "observe"}
	for _, c := range []savedConnection{conn, other} {
		if err := s.Save(c, "pw"); err != nil {
			t.Fatal(err)
		}
		if err := writeThumbnail(thumbnailPath(cache, c.target().ID()), []byte("png")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Remove(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(thumbnailPath(cache, conn.target().ID())); !os.IsNotExist(err) {
		t.Fatal("the removed Mac's picture is deleted", err)
	}
	if _, err := os.Stat(thumbnailPath(cache, other.target().ID())); err != nil {
		t.Fatal("another Mac's picture stays", err)
	}
}

func TestStoreUpdateSkipsUnchangedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	s := newStore(path, memorySecrets{})
	conn := savedConnection{User: "me", Host: "mac", Port: 5900, Mode: "observe"}
	if err := s.Save(conn, "pw"); err != nil {
		t.Fatal(err)
	}
	set := func(c *savedConnection) { c.Last = &lastSession{Width: 10, Height: 5} }
	if found, err := s.Update(conn.target(), set); !found || err != nil {
		t.Fatal(found, err)
	}
	old := time.Unix(1000, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if found, err := s.Update(conn.target(), set); !found || err != nil {
		t.Fatal(found, err)
	}
	info, _ := os.Stat(path)
	if !info.ModTime().Equal(old) {
		t.Fatal("an unchanged update rewrote the store")
	}
	_, _ = s.Update(conn.target(), func(c *savedConnection) { c.Last = &lastSession{Width: 11} })
	info, _ = os.Stat(path)
	if info.ModTime().Equal(old) {
		t.Fatal("a changed update must write")
	}
}

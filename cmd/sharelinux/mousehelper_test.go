package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/nekohasekai/open-screen-sharing/internal/machelper"
	"github.com/zalando/go-keyring"
)

// fakeRun is a pairing attempt with no network.
type fakeRun struct {
	code     chan string
	done     chan struct{}
	err      error
	macKey   []byte
	mu       sync.Mutex
	rejected bool
	canceled bool
	confirms int
}

func newFakeRun(macKey []byte) *fakeRun {
	return &fakeRun{code: make(chan string, 1), done: make(chan struct{}), macKey: macKey}
}
func (f *fakeRun) Code() <-chan string   { return f.code }
func (f *fakeRun) Done() <-chan struct{} { return f.done }
func (f *fakeRun) Err() error            { return f.err }
func (f *fakeRun) Confirm() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirms++
	return f.macKey, nil
}
func (f *fakeRun) PeerKey() []byte { return f.macKey }
func (f *fakeRun) Reject()         { f.mu.Lock(); f.rejected = true; f.mu.Unlock() }
func (f *fakeRun) Cancel()         { f.mu.Lock(); f.canceled = true; f.mu.Unlock() }

func testKeys() *helperKeys { return &helperKeys{secrets: memorySecrets{}} }

func macKeyHex(b byte) string { return hex.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func TestClientKeyIsCreatedOnceInTheKeyringAndNeverInAFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	secrets := memorySecrets{}
	keys := &helperKeys{secrets: secrets}
	first, err := keys.ClientKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := (&helperKeys{secrets: secrets}).ClientKey()
	if err != nil || !bytes.Equal(first.Private, second.Private) || !bytes.Equal(first.Public, second.Public) {
		t.Fatalf("the key changed between calls: %v", err)
	}
	if len(first.Public) != 32 || bytes.Equal(first.Public, first.Private) {
		t.Fatal("bad key pair")
	}
	if secrets[helperClientKeyID] != hex.EncodeToString(first.Private) {
		t.Fatal("the key is not in the keyring")
	}
	if err := keys.AddMac(pairedMac{Name: "Studio", Address: "10.0.0.2:5961", Key: macKeyHex(1)}); err != nil {
		t.Fatal(err)
	}
	var files []string
	filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("keys left files behind: %v", files)
	}
}

func TestNoKeyringMeansNoKeyAndNothingSent(t *testing.T) {
	keys := &helperKeys{secrets: failingSecrets{memorySecrets{}}}
	if _, err := keys.ClientKey(); !errors.Is(err, errKeyringUnavailable) {
		t.Fatalf("ClientKey: %v", err)
	}
	pairing := newHelperPairing(keys)
	started := 0
	pairing.start = func(context.Context, string, noise.DHKey, string) (pairingRunner, error) {
		started++
		return newFakeRun(nil), nil
	}
	_, err := pairing.Begin(context.Background(), "mac.local", "Desk PC")
	if !errors.Is(err, errKeyringUnavailable) || started != 0 {
		t.Fatalf("Begin = %v, started %d", err, started)
	}
	if got := pairingProblem(err); !strings.Contains(got, "keyring") {
		t.Fatalf("the words: %q", got)
	}
}

func TestPairedMacsAreAddedReplacedRemovedAndTamperingIsIgnored(t *testing.T) {
	secrets := memorySecrets{}
	keys := &helperKeys{secrets: secrets}
	a := pairedMac{Name: "Studio", Address: "10.0.0.2:5961", Key: macKeyHex(1)}
	b := pairedMac{Name: "Laptop", Address: "10.0.0.3:5961", Key: macKeyHex(2)}
	for _, m := range []pairedMac{a, b} {
		if err := keys.AddMac(m); err != nil {
			t.Fatal(err)
		}
	}
	macs, _ := keys.Macs()
	if len(macs) != 2 || macs[0].Name != "Laptop" {
		t.Fatalf("%v", macs)
	}
	a2 := a
	a2.Name = "Studio Mac"
	if err := keys.AddMac(a2); err != nil {
		t.Fatal(err)
	}
	if macs, _ = keys.Macs(); len(macs) != 2 {
		t.Fatalf("re-pairing duplicated the Mac: %v", macs)
	}
	if err := keys.RemoveMac(a.Key); err != nil {
		t.Fatal(err)
	}
	if macs, _ = keys.Macs(); len(macs) != 1 || macs[0].Name != "Laptop" {
		t.Fatalf("%v", macs)
	}
	if err := keys.RemoveMac(b.Key); err != nil {
		t.Fatal(err)
	}
	if _, found := secrets[helperMacsID]; found {
		t.Fatal("the empty list was kept")
	}
	// A planted entry with a short key or no name is not trusted.
	secrets[helperMacsID] = `[{"name":"Evil","address":"x:1","key":"abcd"},{"name":"","address":"x:1","key":"` + macKeyHex(3) + `"},{"name":"Fine","address":"x:1","key":"` + macKeyHex(4) + `"}]`
	if macs, _ = keys.Macs(); len(macs) != 1 || macs[0].Name != "Fine" {
		t.Fatalf("%v", macs)
	}
	if err := keys.AddMac(pairedMac{Name: "x", Address: "y", Key: "00"}); err == nil {
		t.Fatal("a short key was accepted")
	}
	secrets[helperMacsID] = "not json"
	if _, err := keys.Macs(); err == nil {
		t.Fatal("a damaged list was read")
	}
}

func fakePairing(keys *helperKeys, run *fakeRun, calls *[]string) *helperPairing {
	pairing := newHelperPairing(keys)
	pairing.start = func(_ context.Context, address string, _ noise.DHKey, name string) (pairingRunner, error) {
		*calls = append(*calls, address+"|"+name)
		return run, nil
	}
	return pairing
}

// The Mac window shows the same two lines for the same two keys; the Swift test
// testBothFingerprintsAreShownUnderTheSameTwoLabelsAsOnTheLinuxDialog pins the same string.
func TestFingerprintLinesMatchTheMacWindow(t *testing.T) {
	got := fingerprintLines(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if got != "This Mac: 72cd 6e84 22c4 07fb\nThis computer: 7587 7bb4 1d39 3b5f" {
		t.Fatalf("%q", got)
	}
}

func TestAnAttemptShowsTheMacsKeyAndThisComputersKey(t *testing.T) {
	keys := testKeys()
	macKey := bytes.Repeat([]byte{9}, 32)
	var calls []string
	attempt, err := fakePairing(keys, newFakeRun(macKey), &calls).Begin(context.Background(), "studio.local", "Desk PC")
	if err != nil {
		t.Fatal(err)
	}
	own, err := keys.ClientKey()
	if err != nil {
		t.Fatal(err)
	}
	want := "This Mac: " + keyFingerprint(macKey) + "\nThis computer: " + keyFingerprint(own.Public)
	if got := attempt.FingerprintLines(); got != want {
		t.Fatalf("%q want %q", got, want)
	}
}

func TestConfirmPinsTheMacKeyAndRejectPinsNothing(t *testing.T) {
	keys := testKeys()
	macKey := bytes.Repeat([]byte{9}, 32)
	var calls []string
	run := newFakeRun(macKey)
	pairing := fakePairing(keys, run, &calls)
	attempt, err := pairing.Begin(context.Background(), " studio.local ", "Desk PC")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "studio.local:5961|Desk PC" {
		t.Fatalf("%v", calls)
	}
	mac, err := attempt.Confirm("Studio")
	if err != nil || mac.Key != hex.EncodeToString(macKey) || mac.Address != "studio.local:5961" {
		t.Fatalf("%v %v", mac, err)
	}
	if macs, _ := keys.Macs(); len(macs) != 1 || macs[0].Name != "Studio" {
		t.Fatalf("%v", macs)
	}

	keys2 := testKeys()
	run2 := newFakeRun(macKey)
	attempt2, err := fakePairing(keys2, run2, &calls).Begin(context.Background(), "studio.local", "Desk PC")
	if err != nil {
		t.Fatal(err)
	}
	attempt2.Reject()
	if !run2.rejected || run2.confirms != 0 {
		t.Fatal("reject did not reach the run")
	}
	if macs, _ := keys2.Macs(); len(macs) != 0 {
		t.Fatalf("a rejected Mac was pinned: %v", macs)
	}
}

// A keyring that refuses at the last moment must not leave a half-paired state
// that looks paired.
func TestIfTheKeyringRefusesAtConfirmNothingIsPinned(t *testing.T) {
	secrets := &flakySecrets{memorySecrets: memorySecrets{}}
	keys := &helperKeys{secrets: secrets}
	var calls []string
	pairing := fakePairing(keys, newFakeRun(bytes.Repeat([]byte{9}, 32)), &calls)
	attempt, err := pairing.Begin(context.Background(), "studio.local", "Desk PC")
	if err != nil {
		t.Fatal(err)
	}
	secrets.failSets = true
	if _, err := attempt.Confirm("Studio"); !errors.Is(err, errKeyringUnavailable) {
		t.Fatalf("%v", err)
	}
	secrets.failSets = false
	if macs, _ := keys.Macs(); len(macs) != 0 {
		t.Fatalf("%v", macs)
	}
}

type flakySecrets struct {
	memorySecrets
	failSets bool
}

func (f *flakySecrets) Set(id, v string) error {
	if f.failSets {
		return errors.New("locked")
	}
	return f.memorySecrets.Set(id, v)
}

// One explicit attempt per click: nothing starts a second one, not a failure,
// not time passing, and a second click while one runs is refused.
func TestAnAttemptIsNeverRepeatedByItself(t *testing.T) {
	keys := testKeys()
	var calls []string
	run := newFakeRun(nil)
	pairing := fakePairing(keys, run, &calls)
	if _, err := pairing.Begin(context.Background(), "studio.local", "Desk PC"); err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.Begin(context.Background(), "studio.local", "Desk PC"); !errors.Is(err, errPairingBusy) {
		t.Fatalf("a second click while one runs: %v", err)
	}
	run.err = machelper.ErrNoAnswer
	close(run.done) // the attempt failed
	time.Sleep(300 * time.Millisecond)
	if len(calls) != 1 || pairing.begun != 1 {
		t.Fatalf("something started another attempt: %v", calls)
	}
	// Only the next click does.
	next := newFakeRun(nil)
	pairing.start = func(context.Context, string, noise.DHKey, string) (pairingRunner, error) {
		calls = append(calls, "again")
		return next, nil
	}
	if _, err := pairing.Begin(context.Background(), "studio.local", "Desk PC"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("%v", calls)
	}
}

func TestProblemWordsAreHumanAndHaveNoEmDashes(t *testing.T) {
	errs := []error{
		machelper.ErrNoAnswer, machelper.ErrRefused, machelper.ErrBadAddress, machelper.ErrBadDeviceName,
		errKeyringUnavailable, errPairingBusy, machelper.ErrPairingMessage, errors.New("boom"),
	}
	for _, err := range errs {
		words := pairingProblem(err)
		if words == "" || strings.ContainsRune(words, 0x2014) || strings.Contains(strings.ToLower(words), "noise") {
			t.Errorf("%v: %q", err, words)
		}
	}
	if pairingProblem(machelper.ErrCancelled) != "" || pairingProblem(nil) != "" {
		t.Error("a cancel is not a problem")
	}
	if helperStatus(nil, nil) != "Not paired with any Mac" ||
		helperStatus([]pairedMac{{Name: "Studio"}}, nil) != "Paired with Studio" ||
		helperStatus(make([]pairedMac, 3), nil) != "Paired with 3 Macs" {
		t.Error("status words")
	}
}

func TestDefaultsForTheDialog(t *testing.T) {
	now := time.Now()
	list := []savedConnection{{Host: "old.local", LastUsed: now.Add(-time.Hour)}, {Host: "new.local", LastUsed: now}}
	if got := defaultHelperAddress(list); got != "new.local" {
		t.Fatal(got)
	}
	if defaultHelperAddress(nil) != "" {
		t.Fatal("no Macs, no default")
	}
	if _, err := machelper.NormalizeDeviceName(defaultDeviceName()); err != nil {
		t.Fatalf("the default name is not allowed on the Mac: %v", err)
	}
}

// A program that put an item under the legacy service name must not be able to
// supply this computer's key or a trusted Mac: helper items never read it.
func TestHelperItemsAreNeverReadFromOrDeletedInTheLegacyService(t *testing.T) {
	keyring.MockInit()
	secrets := keyringSecrets{}
	foreignKey := hex.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	foreignMacs := `[{"name":"Evil Mac","address":"203.0.113.9:5961","key":"` + macKeyHex(9) + `"}]`
	if err := keyring.Set(legacyKeyringService, helperClientKeyID, foreignKey); err != nil {
		t.Fatal(err)
	}
	if err := keyring.Set(legacyKeyringService, helperMacsID, foreignMacs); err != nil {
		t.Fatal(err)
	}

	if _, err := secrets.Get(helperClientKeyID); !errors.Is(err, errNoSecret) {
		t.Fatalf("the legacy client key was served: %v", err)
	}
	keys := &helperKeys{secrets: secrets}
	macs, err := keys.Macs()
	if err != nil || len(macs) != 0 {
		t.Fatalf("a Mac from the legacy service was trusted: %v %v", macs, err)
	}
	key, err := keys.ClientKey()
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(key.Private) == foreignKey {
		t.Fatal("the client key came from the legacy service")
	}
	if stored, err := keyring.Get(keyringService, helperClientKeyID); err != nil || stored != hex.EncodeToString(key.Private) {
		t.Fatalf("the fresh key was not stored under the current service: %v", err)
	}
	// Removing a helper item leaves the legacy service alone.
	if err := secrets.Delete(helperClientKeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(legacyKeyringService, helperClientKeyID); err != nil {
		t.Fatalf("a helper delete touched the legacy service: %v", err)
	}

	// Saved connection passwords still fall back and are cleaned up in both places.
	if err := keyring.Set(legacyKeyringService, "user@host:5900", "old-password"); err != nil {
		t.Fatal(err)
	}
	if got, err := secrets.Get("user@host:5900"); err != nil || got != "old-password" {
		t.Fatalf("a saved password lost its legacy fallback: %q %v", got, err)
	}
	if err := secrets.Delete("user@host:5900"); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(legacyKeyringService, "user@host:5900"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("the legacy password survived a delete: %v", err)
	}
}

func TestKeyFingerprintIsTheSameStringTheMacShows(t *testing.T) {
	// Pinned in the Swift tests (DeviceIdentityTests) for 32 bytes of 0x01.
	if got := keyFingerprint(bytes.Repeat([]byte{1}, 32)); got != "72cd 6e84 22c4 07fb" {
		t.Fatalf("fingerprint %q", got)
	}
	if got := fingerprintOfHex(macKeyHex(1)); got != "72cd 6e84 22c4 07fb" {
		t.Fatalf("hex fingerprint %q", got)
	}
	if fingerprintOfHex("zz") != "" || fingerprintOfHex("abcd") != "" {
		t.Fatal("a bad key got a fingerprint")
	}
	if keyFingerprint(bytes.Repeat([]byte{2}, 32)) == keyFingerprint(bytes.Repeat([]byte{1}, 32)) {
		t.Fatal("two keys share a fingerprint")
	}
}

// The pairing protocol has no message from the Mac to this side after the codes were compared,
// so a Mac this side saved is NOT known to have confirmed. It is saved as pending and shown as
// waiting until a session proves the Mac trusts this computer.
func TestAConfirmedMacIsPendingUntilItAnswersASession(t *testing.T) {
	keys := testKeys()
	macKey := bytes.Repeat([]byte{9}, 32)
	var calls []string
	attempt, err := fakePairing(keys, newFakeRun(macKey), &calls).Begin(context.Background(), "studio.local", "Desk PC")
	if err != nil {
		t.Fatal(err)
	}
	mac, err := attempt.Confirm("Studio")
	if err != nil || !mac.Pending {
		t.Fatalf("%+v %v", mac, err)
	}
	macs, _ := keys.Macs()
	if len(macs) != 1 || !macs[0].Pending {
		t.Fatalf("saved as paired before the Mac answered: %+v", macs)
	}
	if got := helperStatus(macs, nil); got != "Waiting for the Mac to confirm" || strings.Contains(got, "Paired") {
		t.Fatalf("%q", got)
	}
	if got := macRowSubtitle(macs[0]); !strings.HasPrefix(got, "Waiting for the Mac to confirm") {
		t.Fatalf("%q", got)
	}

	if err := keys.PromoteMac(hex.EncodeToString(bytes.Repeat([]byte{1}, 32))); err != nil {
		t.Fatal(err)
	}
	if macs, _ := keys.Macs(); !macs[0].Pending {
		t.Fatal("another key's session promoted this Mac")
	}
	if err := keys.PromoteMac(mac.Key); err != nil {
		t.Fatal(err)
	}
	macs, _ = keys.Macs()
	if len(macs) != 1 || macs[0].Pending || helperStatus(macs, nil) != "Paired with Studio" {
		t.Fatalf("%+v", macs)
	}
	if got := macRowSubtitle(macs[0]); strings.Contains(got, "Waiting") {
		t.Fatalf("%q", got)
	}
	if err := keys.PromoteMac(mac.Key); err != nil { // second time: nothing to do
		t.Fatal(err)
	}
}

func TestStatusWithMixedPairedAndWaitingMacs(t *testing.T) {
	macs := []pairedMac{{Name: "A"}, {Name: "B", Pending: true}, {Name: "C", Pending: true}}
	if got := helperStatus(macs, nil); got != "Paired with A \u00b7 waiting for 2 more Macs to confirm" {
		t.Fatalf("%q", got)
	}
	if got := helperStatus(macs[1:], nil); got != "Waiting for 2 Macs to confirm" {
		t.Fatalf("%q", got)
	}
	if got := helperStatus(macs[:2], nil); got != "Paired with A \u00b7 waiting for another Mac to confirm" {
		t.Fatalf("%q", got)
	}
}

// Macs saved before the pending flag existed were never proven to trust this computer, so they
// load as pending; only an entry written as confirmed loads as paired.
func TestAMacSavedBeforeThePendingFlagLoadsAsPending(t *testing.T) {
	secrets := memorySecrets{}
	keys := &helperKeys{secrets: secrets}
	secrets[helperMacsID] = `[{"name":"Old","address":"old:1","key":"` + macKeyHex(5) + `"},` +
		`{"name":"Legacy","address":"l:1","key":"` + macKeyHex(6) + `","pending":false},` +
		`{"name":"Proven","address":"p:1","key":"` + macKeyHex(7) + `","confirmed":true}]`
	macs, err := keys.Macs()
	if err != nil || len(macs) != 3 {
		t.Fatalf("%+v %v", macs, err)
	}
	got := map[string]bool{}
	for _, m := range macs {
		got[m.Name] = m.Pending
	}
	if !got["Old"] || !got["Legacy"] || got["Proven"] {
		t.Fatalf("pending by name: %+v", got)
	}
	if err := keys.PromoteMac(macKeyHex(5)); err != nil {
		t.Fatal(err)
	}
	macs, _ = keys.Macs()
	for _, m := range macs {
		if m.Name == "Old" && m.Pending {
			t.Fatal("promotion did not persist")
		}
	}
}

func TestHelperCard(t *testing.T) {
	title, body, ready := helperCard(nil, nil)
	if title != "Mouse helper not paired" || ready || body == "" {
		t.Fatal(title, body, ready)
	}
	title, body, ready = helperCard([]pairedMac{{Name: "MacBook Pro"}}, nil)
	if title != "Mouse helper paired" || !ready || body != "Raw mouse is ready for games and 3D tools on MacBook Pro." {
		t.Fatal(title, body, ready)
	}
	title, _, ready = helperCard([]pairedMac{{Name: "A", Pending: true}}, nil)
	if title != "Mouse helper waiting" || ready {
		t.Fatal(title, ready)
	}
	_, body, ready = helperCard([]pairedMac{{Name: "A"}, {Name: "B"}, {Name: "C", Pending: true}}, nil)
	if ready != true || body != "Raw mouse is ready for games and 3D tools on 2 Macs." {
		t.Fatal(body, ready)
	}
	title, _, ready = helperCard(nil, errors.New("keyring locked"))
	if title != "Mouse helper not ready" || ready {
		t.Fatal(title, ready)
	}
}

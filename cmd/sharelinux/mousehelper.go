package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/flynn/noise"
	"github.com/nekohasekai/open-screen-sharing/internal/machelper"
	E "github.com/sagernet/sing/common/exceptions"
	"golang.org/x/crypto/curve25519"
)

// The Mac mouse helper: pairing from this side. Everything secret lives in the
// system keyring (Secret Service), never in a file:
//
//	mouse-helper/client-key   this computer's static private key (hex)
//	mouse-helper/macs         the pinned Mac keys with their names (JSON)
//
// Pairing is always one explicit attempt per click. Nothing in this file, and
// nothing it calls, retries a pairing by itself.
const (
	helperClientKeyID = helperSecretPrefix + "client-key"
	helperMacsID      = helperSecretPrefix + "macs"
	maxPairedMacs     = 16
)

// keyFingerprint is the short fingerprint of a public key, the same string the
// Mac helper shows for this computer: the first 8 bytes of SHA-256 in four
// groups of four hex digits ("72cd 6e84 22c4 07fb"). It lets a person tell
// two devices with the same or a look-alike name apart.
func keyFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	h := hex.EncodeToString(sum[:8])
	return h[0:4] + " " + h[4:8] + " " + h[8:12] + " " + h[12:16]
}

// fingerprintOfHex is keyFingerprint for a key kept as hex; "" when it is not one.
func fingerprintOfHex(key string) string {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return ""
	}
	return keyFingerprint(raw)
}

// pairedMac is one Mac this computer trusts.
type pairedMac struct {
	// Name is what the person calls it.
	Name string `json:"name"`
	// Address is where its helper listens (host:port).
	Address string `json:"address"`
	// Key is the Mac helper's static public key, hex, 32 bytes. It was pinned
	// after the person compared the codes.
	Key string `json:"key"`
	// Pending is true from the moment this side saved the Mac until the Mac proved it
	// accepted this computer. The pairing protocol carries no message from the Mac back
	// to this side after the codes were compared, so the Mac's own Confirm is invisible
	// here. The first authenticated session (the Mac answered under this computer's key,
	// which it only does once it trusts it) clears the flag.
	//
	// It is stored inverted, as "confirmed", and a stored entry without that field is
	// pending: Macs saved before the flag existed were never proven, so they load as
	// pending, not paired.
	Pending bool `json:"-"`
}

type pairedMacJSON struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	Key       string `json:"key"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

func (m pairedMac) MarshalJSON() ([]byte, error) {
	return json.Marshal(pairedMacJSON{Name: m.Name, Address: m.Address, Key: m.Key, Confirmed: !m.Pending})
}

func (m *pairedMac) UnmarshalJSON(data []byte) error {
	var raw pairedMacJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = pairedMac{Name: raw.Name, Address: raw.Address, Key: raw.Key, Pending: !raw.Confirmed}
	return nil
}

func (m pairedMac) valid() bool {
	key, err := hex.DecodeString(m.Key)
	return err == nil && len(key) == 32 && strings.TrimSpace(m.Name) != "" && m.Address != ""
}

var errKeyringUnavailable = E.New("the system keyring is not available")

// helperKeys keeps this computer's key and the pinned Mac keys in the keyring.
type helperKeys struct {
	secrets secretStore
	mu      sync.Mutex
}

// ClientKey returns this computer's static key, creating it in the keyring the
// first time. If the keyring refuses, nothing is created or sent anywhere.
func (h *helperKeys) ClientKey() (noise.DHKey, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stored, err := h.secrets.Get(helperClientKeyID)
	if err == nil {
		if private, decodeErr := hex.DecodeString(stored); decodeErr == nil && len(private) == 32 {
			return keyFromPrivate(private)
		}
		return noise.DHKey{}, E.New("the saved client key is damaged; remove it from the keyring (", helperClientKeyID, ") to start again")
	}
	if !errors.Is(err, errNoSecret) {
		return noise.DHKey{}, keyringProblem(err)
	}
	pair, err := machelper.GenerateKeyPair()
	if err != nil {
		return noise.DHKey{}, err
	}
	if err := h.secrets.Set(helperClientKeyID, hex.EncodeToString(pair.Private)); err != nil {
		return noise.DHKey{}, keyringProblem(err)
	}
	return pair, nil
}

func keyFromPrivate(private []byte) (noise.DHKey, error) {
	public, err := curve25519.X25519(private, curve25519.Basepoint)
	if err != nil {
		return noise.DHKey{}, err
	}
	return noise.DHKey{Private: append([]byte(nil), private...), Public: public}, nil
}

func keyringProblem(err error) error {
	return E.Cause(errKeyringUnavailable, err.Error())
}

// Macs lists the pinned Macs. An entry that is not what this program wrote
// (bad key, empty name) is ignored, not trusted.
func (h *helperKeys) Macs() ([]pairedMac, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.macsLocked()
}

func (h *helperKeys) macsLocked() ([]pairedMac, error) {
	stored, err := h.secrets.Get(helperMacsID)
	if errors.Is(err, errNoSecret) {
		return nil, nil
	}
	if err != nil {
		return nil, keyringProblem(err)
	}
	var all []pairedMac
	if err := json.Unmarshal([]byte(stored), &all); err != nil {
		return nil, E.New("the saved list of paired Macs is damaged")
	}
	var macs []pairedMac
	for _, m := range all {
		if m.valid() {
			macs = append(macs, m)
		}
	}
	sort.SliceStable(macs, func(i, j int) bool { return macs[i].Name < macs[j].Name })
	return macs, nil
}

// AddMac pins a Mac. The same key again replaces its entry (re-pairing).
func (h *helperKeys) AddMac(mac pairedMac) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !mac.valid() {
		return E.New("not a usable Mac key")
	}
	macs, err := h.macsLocked()
	if err != nil {
		return err
	}
	kept := macs[:0:0]
	for _, m := range macs {
		if m.Key != mac.Key {
			kept = append(kept, m)
		}
	}
	if len(kept) >= maxPairedMacs {
		return E.New("too many paired Macs; remove one first")
	}
	return h.writeLocked(append(kept, mac))
}

// PromoteMac clears Pending for the Mac with this key: it answered a session, so it
// trusts this computer. A Mac that is not saved, or not pending, changes nothing.
func (h *helperKeys) PromoteMac(key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	macs, err := h.macsLocked()
	if err != nil {
		return err
	}
	changed := false
	for i := range macs {
		if macs[i].Key == key && macs[i].Pending {
			macs[i].Pending = false
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return h.writeLocked(macs)
}

// RemoveMac forgets a pinned key. The Mac itself keeps this computer in its
// own list until its owner removes it there.
func (h *helperKeys) RemoveMac(key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	macs, err := h.macsLocked()
	if err != nil {
		return err
	}
	kept := macs[:0:0]
	for _, m := range macs {
		if m.Key != key {
			kept = append(kept, m)
		}
	}
	return h.writeLocked(kept)
}

func (h *helperKeys) writeLocked(macs []pairedMac) error {
	if len(macs) == 0 {
		if err := h.secrets.Delete(helperMacsID); err != nil {
			return keyringProblem(err)
		}
		return nil
	}
	data, err := json.Marshal(macs)
	if err != nil {
		return err
	}
	if err := h.secrets.Set(helperMacsID, string(data)); err != nil {
		return keyringProblem(err)
	}
	return nil
}

// pairingRunner is machelper.PairingRun, so the flow can be tested without a network.
type pairingRunner interface {
	Code() <-chan string
	Done() <-chan struct{}
	Err() error
	Confirm() ([]byte, error)
	// PeerKey is the Mac's static key; valid once the code has been delivered.
	PeerKey() []byte
	Reject()
	Cancel()
}

type pairingStarter func(ctx context.Context, address string, static noise.DHKey, deviceName string) (pairingRunner, error)

func startRealPairing(ctx context.Context, address string, static noise.DHKey, deviceName string) (pairingRunner, error) {
	run, err := machelper.StartPairing(ctx, address, static, deviceName, machelper.PairingOptions{})
	if err != nil {
		return nil, err
	}
	return run, nil
}

var errPairingBusy = E.New("a pairing is already in progress")

// helperPairing starts pairing attempts, one at a time and only when asked.
type helperPairing struct {
	keys  *helperKeys
	start pairingStarter

	mu     sync.Mutex
	active *pairingAttempt
	// begun counts the attempts started so far (tests assert it does not move by itself).
	begun int
}

func newHelperPairing(keys *helperKeys) *helperPairing {
	return &helperPairing{keys: keys, start: startRealPairing}
}

// pairingAttempt is one explicit try: it sends message 1 once and is dead
// after any outcome.
type pairingAttempt struct {
	owner   *helperPairing
	run     pairingRunner
	address string
	keys    *helperKeys
	// own is this computer's public key.
	own []byte
}

// Begin starts one attempt. It is called from a click and from nowhere else.
// The key is taken from (or created in) the keyring first: with no keyring
// nothing is sent.
func (h *helperPairing) Begin(ctx context.Context, address, deviceName string) (*pairingAttempt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active != nil {
		select {
		case <-h.active.run.Done():
			h.active = nil
		default:
			return nil, errPairingBusy
		}
	}
	static, err := h.keys.ClientKey()
	if err != nil {
		return nil, err
	}
	normalized, err := machelper.NormalizeHelperAddress(strings.TrimSpace(address))
	if err != nil {
		return nil, err
	}
	run, err := h.start(ctx, normalized, static, deviceName)
	if err != nil {
		return nil, err
	}
	h.begun++
	a := &pairingAttempt{owner: h, run: run, address: normalized, keys: h.keys, own: static.Public}
	h.active = a
	return a, nil
}

// Code delivers the six digits to compare.
func (a *pairingAttempt) Code() <-chan string { return a.run.Code() }

// Done is closed when the attempt is over.
func (a *pairingAttempt) Done() <-chan struct{} { return a.run.Done() }

// Err is why it failed, nil if it did not.
func (a *pairingAttempt) Err() error { return a.run.Err() }

// FingerprintLines is both keys, worded exactly as the Mac's pairing window
// words them: "This Mac" is the Mac, "This computer" is this one. The person
// reads the two lines here and on the Mac and sees they are the same.
func (a *pairingAttempt) FingerprintLines() string {
	return fingerprintLines(a.run.PeerKey(), a.own)
}

func fingerprintLines(mac, computer []byte) string {
	return "This Mac: " + keyFingerprint(mac) + "\nThis computer: " + keyFingerprint(computer)
}

// Confirm is the person saying the codes match: the Mac's key is pinned in the keyring.
func (a *pairingAttempt) Confirm(name string) (pairedMac, error) {
	key, err := a.run.Confirm()
	if err != nil {
		return pairedMac{}, err
	}
	mac := pairedMac{Name: strings.TrimSpace(name), Address: a.address, Key: hex.EncodeToString(key), Pending: true}
	if mac.Name == "" {
		mac.Name = hostOf(a.address)
	}
	if err := a.keys.AddMac(mac); err != nil {
		return pairedMac{}, err
	}
	return mac, nil
}

// Reject is the person saying the codes differ. Nothing is pinned.
func (a *pairingAttempt) Reject() { a.run.Reject() }

// Cancel ends the attempt without a decision (the dialog was closed).
func (a *pairingAttempt) Cancel() { a.run.Cancel() }

func hostOf(address string) string {
	if i := strings.LastIndex(address, ":"); i > 0 {
		address = address[:i]
	}
	return strings.Trim(address, "[]")
}

// pairingProblem says in plain words why an attempt did not work.
func pairingProblem(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, machelper.ErrCancelled):
		return ""
	case errors.Is(err, machelper.ErrNoAnswer):
		return "The Mac did not answer. Check the address, that sharelinux Mouse Helper is running on the Mac, and that its pairing window is open (menu bar icon, then Pair a Linux device)."
	case errors.Is(err, machelper.ErrRefused):
		return "Nothing is listening at that address. Is sharelinux Mouse Helper running on the Mac?"
	case errors.Is(err, machelper.ErrBadAddress):
		return "That address does not look right."
	case errors.Is(err, machelper.ErrBadDeviceName):
		return "This computer's name cannot be shown on the Mac. Use plain letters, numbers and single spaces."
	case errors.Is(err, errKeyringUnavailable):
		return "Your system keyring is not available, so the keys cannot be kept safely. Unlock it, or install GNOME Keyring or KWallet, and try again."
	case errors.Is(err, errPairingBusy):
		return "A pairing is already in progress."
	case errors.Is(err, machelper.ErrPairingMessage), errors.Is(err, machelper.ErrPairingState):
		return "The Mac answered in a way this program does not understand. Make sure both programs are the same version."
	}
	return "Pairing did not work: " + strings.TrimSpace(err.Error())
}

// helperStatus is the one line under "Mouse helper" in Preferences.
func helperStatus(macs []pairedMac, err error) string {
	switch {
	case err != nil:
		return pairingProblem(err)
	case len(macs) == 0:
		return "Not paired with any Mac"
	}
	var paired []pairedMac
	waiting := 0
	for _, m := range macs {
		if m.Pending {
			waiting++
		} else {
			paired = append(paired, m)
		}
	}
	var line string
	switch {
	case len(paired) == 1:
		line = "Paired with " + paired[0].Name
	case len(paired) > 1:
		line = "Paired with " + strconv.Itoa(len(paired)) + " Macs"
	case waiting == 1:
		return "Waiting for the Mac to confirm"
	default:
		return "Waiting for " + strconv.Itoa(waiting) + " Macs to confirm"
	}
	switch waiting {
	case 0:
		return line
	case 1:
		return line + " \u00b7 waiting for another Mac to confirm"
	}
	return line + " \u00b7 waiting for " + strconv.Itoa(waiting) + " more Macs to confirm"
}

// defaultHelperAddress is where the pairing dialog starts: the Mac used most recently.
func defaultHelperAddress(connections []savedConnection) string {
	var best *savedConnection
	for i := range connections {
		if best == nil || connections[i].LastUsed.After(best.LastUsed) {
			best = &connections[i]
		}
	}
	if best == nil {
		return ""
	}
	return best.Host
}

// defaultDeviceName is this computer's name as the Mac will show it.
func defaultDeviceName() string {
	host, err := os.Hostname()
	if err != nil {
		return "Linux computer"
	}
	host = strings.TrimSpace(host)
	if name, err := machelper.NormalizeDeviceName(host); err == nil {
		return name
	}
	return "Linux computer"
}

// macRowSubtitle is the second line of a Mac's row in Preferences.
func macRowSubtitle(mac pairedMac) string {
	line := mac.Address + " \u00b7 " + fingerprintOfHex(mac.Key)
	if mac.Pending {
		return "Waiting for the Mac to confirm \u00b7 " + line
	}
	return line
}

// helperCard is the title and the line of the status card at the foot of the
// home sidebar. ready says the helper can be used now (a green dot).
func helperCard(macs []pairedMac, err error) (title, body string, ready bool) {
	if err != nil {
		return "Mouse helper not ready", pairingProblem(err), false
	}
	var paired []pairedMac
	for _, mac := range macs {
		if !mac.Pending {
			paired = append(paired, mac)
		}
	}
	switch {
	case len(macs) == 0:
		return "Mouse helper not paired", "Pair it once for smooth mouse movement in games and 3D tools.", false
	case len(paired) == 0:
		return "Mouse helper waiting", "Confirm the pairing on the Mac to finish.", false
	case len(paired) == 1:
		return "Mouse helper paired", "Raw mouse is ready for games and 3D tools on " + paired[0].Name + ".", true
	}
	return "Mouse helper paired", "Raw mouse is ready for games and 3D tools on " + strconv.Itoa(len(paired)) + " Macs.", true
}

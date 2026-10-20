package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/zalando/go-keyring"
)

const keyringService = "open-screen-sharing"

// savedConnection is one entry of connections.json. It never holds a password.
type savedConnection struct {
	Name     string `json:"name"`
	User     string `json:"user,omitempty"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	VNC      bool   `json:"vnc,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Quality  string `json:"quality,omitempty"`
	Keyboard string `json:"keyboard,omitempty"`
}

func (c savedConnection) target() target {
	return target{User: c.User, Host: c.Host, Port: c.Port}
}

// secretStore keeps passwords outside the config file.
type secretStore interface {
	Get(id string) (string, error)
	Set(id, password string) error
	Delete(id string) error
}

var errNoSecret = E.New("no saved password")

// keyringSecrets uses the Secret Service (GNOME Keyring, KWallet) on Linux and
// the Keychain on macOS.
type keyringSecrets struct{}

func (keyringSecrets) Get(id string) (string, error) {
	password, err := keyring.Get(keyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errNoSecret
	}
	return password, err
}

func (keyringSecrets) Set(id, password string) error {
	return keyring.Set(keyringService, id, password)
}

func (keyringSecrets) Delete(id string) error {
	err := keyring.Delete(keyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// store is the list of saved connections plus their passwords.
type store struct {
	path    string
	secrets secretStore

	access sync.Mutex
}

func newStore(path string, secrets secretStore) *store {
	return &store{path: path, secrets: secrets}
}

// defaultStorePath is $XDG_CONFIG_HOME/open-screen-sharing/connections.json.
func defaultStorePath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", E.Cause(err, "find the config directory")
	}
	return filepath.Join(directory, "open-screen-sharing", "connections.json"), nil
}

func (s *store) List() ([]savedConnection, error) {
	s.access.Lock()
	defer s.access.Unlock()
	return s.load()
}

func (s *store) load() ([]savedConnection, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, E.Cause(err, "read saved connections")
	}
	var connections []savedConnection
	err = json.Unmarshal(data, &connections)
	if err != nil {
		return nil, E.Cause(err, "parse ", s.path)
	}
	return connections, nil
}

func (s *store) write(connections []savedConnection) error {
	err := os.MkdirAll(filepath.Dir(s.path), 0o700)
	if err != nil {
		return E.Cause(err, "create the config directory")
	}
	data, err := json.MarshalIndent(connections, "", "\t")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".connections-*")
	if err != nil {
		return E.Cause(err, "write saved connections")
	}
	defer os.Remove(temporary.Name())
	_, err = temporary.Write(data)
	if err == nil {
		err = temporary.Chmod(0o600)
	}
	closeErr := temporary.Close()
	if err = E.Errors(err, closeErr); err != nil {
		return E.Cause(err, "write saved connections")
	}
	return os.Rename(temporary.Name(), s.path)
}

// Save adds the connection, or replaces the one with the same address and user.
// A non-empty password goes to the keyring; if the keyring fails the connection
// is still saved and the error says the password was not.
func (s *store) Save(connection savedConnection, password string) error {
	s.access.Lock()
	defer s.access.Unlock()
	connections, err := s.load()
	if err != nil {
		return err
	}
	id := connection.target().ID()
	index := slices.IndexFunc(connections, func(other savedConnection) bool {
		return other.target().ID() == id
	})
	if connection.Name == "" {
		connection.Name = connection.target().Address()
	}
	if index >= 0 {
		connections[index] = connection
	} else {
		connections = append(connections, connection)
	}
	err = s.write(connections)
	if err != nil {
		return err
	}
	if password == "" {
		return nil
	}
	err = s.secrets.Set(id, password)
	if err != nil {
		return E.Cause(err, "the connection was saved but the keyring refused the password")
	}
	return nil
}

// Remove deletes a connection and its password.
func (s *store) Remove(connection savedConnection) error {
	s.access.Lock()
	defer s.access.Unlock()
	connections, err := s.load()
	if err != nil {
		return err
	}
	id := connection.target().ID()
	connections = slices.DeleteFunc(connections, func(other savedConnection) bool {
		return other.target().ID() == id
	})
	err = s.write(connections)
	if err != nil {
		return err
	}
	return s.secrets.Delete(id)
}

// Password returns the saved password, or errNoSecret.
func (s *store) Password(connection savedConnection) (string, error) {
	return s.secrets.Get(connection.target().ID())
}

// Find looks a connection up by name or by address, ignoring case.
func (s *store) Find(query string) (savedConnection, bool) {
	connections, err := s.List()
	if err != nil {
		return savedConnection{}, false
	}
	for _, connection := range connections {
		if strings.EqualFold(connection.Name, query) || strings.EqualFold(connection.target().String(), query) {
			return connection, true
		}
	}
	return savedConnection{}, false
}

func (c *savedConnection) apply(opts *options) error {
	var err error
	if c.Mode != "" && !opts.Explicit["mode"] {
		opts.Mode, err = parseMode(c.Mode)
		if err != nil {
			return err
		}
	}
	if c.Quality != "" && !opts.Explicit["quality"] {
		opts.Quality, err = rfb.ParseQuality(c.Quality)
		if err != nil {
			return err
		}
	}
	if c.Keyboard != "" && !opts.Explicit["keyboard"] {
		opts.Keyboard, err = parseKeyboard(c.Keyboard)
	}
	opts.VNC = opts.VNC || c.VNC
	return err
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

func main() {
	opts, err := parseArgs(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fatal(err)
	}
	level := slog.LevelInfo
	if opts.Verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	path, err := defaultStorePath()
	if err != nil {
		fatal(err)
	}
	connections := newStore(path, keyringSecrets{})
	if opts.List {
		listConnections(connections)
		return
	}
	if opts.Forget != "" {
		forgetConnection(connections, opts.Forget)
		return
	}
	prefsPath, err := defaultSettingsPath()
	if err != nil {
		fatal(err)
	}
	prefs := &settingsStore{path: prefsPath}
	current, err := prefs.Load()
	if err != nil {
		logger.Warn("preferences", "error", err)
	}
	viewer := &app{opts: opts, logger: logger, store: connections, prefs: prefs, settings: current}
	err = runGUI(viewer)
	if err != nil {
		fatal(err)
	}
}

func listConnections(connections *store) {
	items, err := connections.List()
	if err != nil {
		fatal(err)
	}
	for _, item := range items {
		fmt.Printf("%s\t%s\n", item.Name, item.target())
	}
}

func forgetConnection(connections *store, query string) {
	connection, found := connections.Find(query)
	if !found {
		fatal(E.New("no saved connection ", strconv.Quote(query)))
	}
	err := connections.Remove(connection)
	if err != nil {
		fatal(err)
	}
	fmt.Println("forgot", connection.Name)
}

func fatal(err error) {
	message := err.Error()
	fmt.Fprintln(os.Stderr, "sharelinux:", strings.TrimSpace(message))
	os.Exit(1)
}

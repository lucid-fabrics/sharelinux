//go:build !linux

package main

// runGUI is only available on Linux, where GTK 4 and libadwaita are the toolkit.
func runGUI(*app) error {
	return errNoWindow
}

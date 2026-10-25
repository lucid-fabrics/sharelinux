//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	E "github.com/sagernet/sing/common/exceptions"
)

// iconTheme* are the names of the design's symbolic icons, as the icon theme knows them.
const (
	iconFullscreen   = "sharelinux-fullscreen-symbolic"
	iconExclusive    = "sharelinux-exclusive-symbolic"
	iconSound        = "sharelinux-sound-symbolic"
	iconSoundMuted   = "sharelinux-sound-muted-symbolic"
	iconClipboard    = "sharelinux-clipboard-symbolic"
	iconSendFile     = "sharelinux-send-file-symbolic"
	iconDisplays     = "sharelinux-displays-symbolic"
	iconStats        = "sharelinux-stats-symbolic"
	iconNetwork      = "sharelinux-network-symbolic"
	iconPaired       = "sharelinux-paired-symbolic"
	iconDisconnect   = "sharelinux-disconnect-symbolic"
	iconMacs         = "sharelinux-macs-symbolic"
	iconRecent       = "sharelinux-recent-symbolic"
	iconMouseHelper  = "sharelinux-mouse-helper-symbolic"
	iconPreferences  = "sharelinux-preferences-symbolic"
	iconSearch       = "sharelinux-search-symbolic"
	iconAdd          = "sharelinux-add-symbolic"
	iconMore         = "sharelinux-more-symbolic"
	iconView         = "sharelinux-view-symbolic"
	iconKeyboard     = "sharelinux-keyboard-symbolic"
	iconClose        = "sharelinux-close-symbolic"
	iconForward      = "sharelinux-forward-symbolic"
	iconCheck        = "sharelinux-check-symbolic"
	iconPlay         = "sharelinux-play-symbolic"
	macArtIconPrefix = "sharelinux-mac-"
)

// iconDirectory is where the embedded icons are unpacked for the icon theme.
func iconDirectory() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", E.Cause(err, "find the cache directory")
	}
	return filepath.Join(base, configName, "icons"), nil
}

// installIcons unpacks the symbolic icons and the Mac art into the cache and
// adds that folder to the icon theme, so images are named like any other icon.
func installIcons(display *gdk.Display) error {
	root, err := iconDirectory()
	if err != nil {
		return err
	}
	return installIconsAt(display, root)
}

func installIconsAt(display *gdk.Display, root string) error {
	actions := filepath.Join(root, "hicolor", "scalable", "actions")
	apps := filepath.Join(root, "hicolor", "scalable", "apps")
	for _, directory := range []string{actions, apps} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return E.Cause(err, "create the icon folder")
		}
	}
	for _, name := range iconNames() {
		data, err := assetFiles.ReadFile("data/icons/" + name + ".svg")
		if err != nil {
			return err
		}
		if err = writeIfChanged(filepath.Join(actions, name+".svg"), data); err != nil {
			return err
		}
	}
	appIcon, err := assetFiles.ReadFile("data/" + applicationID + ".svg")
	if err != nil {
		return err
	}
	if err = writeIfChanged(filepath.Join(apps, applicationID+".svg"), appIcon); err != nil {
		return err
	}
	for _, art := range allMacArt {
		data, err := macArtSVG(art)
		if err != nil {
			return err
		}
		if err = writeIfChanged(filepath.Join(apps, macArtIconPrefix+string(art)+".svg"), data); err != nil {
			return err
		}
	}
	gtk.IconThemeGetForDisplay(display).AddSearchPath(root)
	return nil
}

// writeIfChanged writes a file unless it already holds these bytes.
func writeIfChanged(path string, data []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) {
		return nil
	}
	err = os.WriteFile(path, data, 0o644)
	if err != nil {
		return E.Cause(err, "write ", path)
	}
	return nil
}

// macArtIcon is the icon name of a Mac's illustration.
func macArtIcon(art macArt) string { return macArtIconPrefix + string(art) }

// icon is a symbolic icon at a size.
func icon(name string, size int) *gtk.Image {
	image := gtk.NewImageFromIconName(name)
	image.SetPixelSize(size)
	return image
}

// iconButton is a flat icon-only button with the name screen readers say.
func iconButton(name, label string, size int) *gtk.Button {
	button := gtk.NewButton()
	button.SetChild(icon(name, size))
	button.SetTooltipText(label)
	setAccessibleLabel(button, label)
	return button
}

// setAccessibleLabel gives a widget the name a screen reader says.
func setAccessibleLabel(widget interface {
	UpdateProperty([]gtk.AccessibleProperty, []coreglib.Value)
}, label string) {
	widget.UpdateProperty([]gtk.AccessibleProperty{gtk.AccessiblePropertyLabel}, []coreglib.Value{*coreglib.NewValue(label)})
}

package main

import (
	"embed"
	"io/fs"
	"strings"
)

// assetFiles holds the symbolic icons and the Mac illustrations of the design.
// They travel inside the binary, so the viewer looks the same installed from the
// Flatpak or run from a build folder.
//
//go:embed data/icons/*.svg data/mac/*.svg data/io.github.sharelinux.ScreenSharing.svg
var assetFiles embed.FS

// iconNames lists the embedded symbolic icons by their theme name, such as
// "sharelinux-sound-symbolic".
func iconNames() []string {
	entries, err := fs.ReadDir(assetFiles, "data/icons")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".svg"))
	}
	return names
}

// macArtSVG returns the illustration for a kind of Mac.
func macArtSVG(art macArt) ([]byte, error) {
	return assetFiles.ReadFile("data/mac/" + string(art) + ".svg")
}

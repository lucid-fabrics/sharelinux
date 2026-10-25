package main

import (
	"strings"
)

// macArt names the illustration drawn on a Mac's card.
type macArt string

const (
	artMacBook macArt = "macbook"
	artIMac    macArt = "imac"
	artMini    macArt = "macmini"
	artStudio  macArt = "macstudio"
	artPro     macArt = "macpro"
	// artDisplay is a plain screen, for a Mac that did not say what it is.
	artDisplay macArt = "display"
)

// allMacArt lists every illustration, for tests and for drawing.
var allMacArt = []macArt{artMacBook, artIMac, artMini, artStudio, artPro, artDisplay}

// deviceInfoService is what a Mac announces next to Screen Sharing; its text
// record carries the model identifier.
const deviceInfoService = "_device-info._tcp"

// modelFromText reads the model identifier ("MacBookPro18,2") out of the text
// record of a device-info announcement. It returns "" when there is none.
func modelFromText(records []string) string {
	for _, record := range records {
		key, value, found := strings.Cut(record, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), "model") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// appleSiliconModels are the "MacNN,N" identifiers Apple uses from 2022 on,
// which no longer carry the family in their name. Only identifiers known for
// sure are listed; any other falls back to the plain screen.
var appleSiliconModels = map[string]macArt{
	// Mac Studio.
	"Mac13,1": artStudio, "Mac13,2": artStudio, "Mac14,13": artStudio, "Mac14,14": artStudio,
	"Mac15,14": artStudio, "Mac16,9": artStudio,
	// Mac mini.
	"Mac14,3": artMini, "Mac14,12": artMini, "Mac16,10": artMini, "Mac16,11": artMini,
	// Mac Pro.
	"Mac14,8": artPro,
	// MacBook Air and MacBook Pro.
	"Mac14,2": artMacBook, "Mac14,5": artMacBook, "Mac14,6": artMacBook, "Mac14,7": artMacBook,
	"Mac14,9": artMacBook, "Mac14,10": artMacBook, "Mac14,15": artMacBook,
	"Mac15,3": artMacBook, "Mac15,6": artMacBook, "Mac15,7": artMacBook, "Mac15,8": artMacBook,
	"Mac15,9": artMacBook, "Mac15,10": artMacBook, "Mac15,11": artMacBook, "Mac15,12": artMacBook,
	"Mac15,13": artMacBook,
	"Mac16,1":  artMacBook, "Mac16,5": artMacBook, "Mac16,6": artMacBook, "Mac16,7": artMacBook,
	"Mac16,8": artMacBook, "Mac16,12": artMacBook, "Mac16,13": artMacBook,
	// iMac.
	"Mac15,4": artIMac, "Mac15,5": artIMac, "Mac16,2": artIMac, "Mac16,3": artIMac,
}

// artForModel picks the illustration for a model identifier.
func artForModel(model string) macArt {
	model = strings.TrimSpace(model)
	switch {
	case model == "":
		return artDisplay
	case strings.HasPrefix(model, "MacBook"):
		return artMacBook
	case strings.HasPrefix(model, "iMac"):
		return artIMac
	case strings.HasPrefix(model, "Macmini"):
		return artMini
	case strings.HasPrefix(model, "MacPro"):
		return artPro
	}
	if art, ok := appleSiliconModels[model]; ok {
		return art
	}
	return artDisplay
}

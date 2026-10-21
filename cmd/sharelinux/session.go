package main

import (
	"fmt"
	"strings"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

var qualities = []rfb.Quality{rfb.QualityAdaptive, rfb.QualityFull, rfb.QualityHigh, rfb.QualityMedium, rfb.QualityLow}

// qualityLabel is the menu word for a quality.
func qualityLabel(quality rfb.Quality) string {
	name := quality.String()
	return strings.ToUpper(name[:1]) + name[1:]
}

// qualityHint explains a quality in the preferences.
func qualityHint(quality rfb.Quality) string {
	switch quality {
	case rfb.QualityAdaptive:
		return "Adaptive lowers detail on slow networks to stay smooth"
	case rfb.QualityFull:
		return "Full keeps every detail and needs a fast network"
	case rfb.QualityHigh:
		return "High keeps text sharp and uses a lot of bandwidth"
	case rfb.QualityMedium:
		return "Medium trades some detail for a lighter load"
	}
	return "Low is for slow networks and loses detail"
}

// allowedMode is the mode a server accepts: a server that only allows viewing
// never leaves observe mode, whatever was asked for.
func allowedMode(mode rfb.Mode, flags uint32) rfb.Mode {
	if flags&rfb.ServerFlagObserveOnly != 0 {
		return rfb.ModeObserve
	}
	return mode
}

// displayChoice is one entry of the Display menu.
type displayChoice struct {
	ID    uint32
	Label string
}

// displayChoices lists what the Display menu offers: every display together,
// then each display the Mac reported.
func displayChoices(layout *rfb.DisplayLayout) []displayChoice {
	choices := []displayChoice{{ID: 0, Label: "All displays"}}
	if layout == nil {
		return choices
	}
	for index, display := range layout.Displays {
		label := fmt.Sprint("Display ", index+1)
		switch {
		case display.Flags&rfb.DisplayFlagVirtual != 0:
			label = "Virtual display"
		case display.Flags&rfb.DisplayFlagMain != 0:
			label = "Main display"
		}
		choices = append(choices, displayChoice{ID: display.ID, Label: label})
	}
	return choices
}

// currentDisplay is the id of the choice that matches the layout's current display.
func currentDisplay(layout *rfb.DisplayLayout) uint32 {
	if layout == nil || layout.Current == rfb.AllDisplays {
		return 0
	}
	return layout.Current
}

// displayLabel is the name on the Display button.
func displayLabel(layout *rfb.DisplayLayout) string {
	current := currentDisplay(layout)
	for _, choice := range displayChoices(layout) {
		if choice.ID == current {
			return choice.Label
		}
	}
	return "All displays"
}

// videoDisplayReady says whether the Mac's picture is one display, which is all its video
// stream can carry: with several displays shown together (the framebuffer is all of them)
// the video would show one and leave the rest frozen, so the tiles stay in charge. A Mac
// with a single display, or one display chosen, can use the video. Before the Mac has
// sent its layout nothing is known and the answer is no.
func videoDisplayReady(layout *rfb.DisplayLayout) bool {
	if layout == nil {
		return false
	}
	return currentDisplay(layout) != 0 || len(layout.Displays) <= 1
}

// mainDisplay returns the id of the Mac's main display.
func mainDisplay(layout *rfb.DisplayLayout) (uint32, bool) {
	if layout == nil {
		return 0, false
	}
	for _, display := range layout.Displays {
		if display.Flags&rfb.DisplayFlagMain != 0 {
			return display.ID, true
		}
	}
	return 0, false
}

// codecLabel names the encoding that carried the last picture.
func codecLabel(encoding rfb.Encoding) string {
	switch encoding {
	case rfb.EncodingMVS, rfb.EncodingMediaStream:
		return "Video"
	case rfb.EncodingZRLE:
		return "ZRLE"
	case rfb.EncodingRaw:
		return "Raw"
	case rfb.EncodingHextile:
		return "Hextile"
	case rfb.EncodingZlib:
		return "Zlib"
	case rfb.EncodingCopyRect:
		return "CopyRect"
	}
	return encoding.String()
}

// statRow is one line of the stats card.
type statRow struct {
	Label, Value string
}

// statRows are the lines of the stats card. Bandwidth is left out when the
// server does not report it.
func statRows(info rfb.ServerInfo, frames float64, latencyMS float64, bitsPS float64, encoding rfb.Encoding, width, height int) []statRow {
	rows := []statRow{
		{"Frame rate", fmt.Sprintf("%.0f fps", frames)},
		{"Latency", fmt.Sprintf("%.0f ms", latencyMS)},
	}
	if info.Dialect == rfb.DialectApple {
		rows = append(rows, statRow{"Bandwidth", formatBits(bitsPS)})
	}
	rows = append(rows,
		statRow{"Codec", codecLabel(encoding)},
		statRow{"Resolution", fmt.Sprintf("%d×%d", width, height)},
	)
	return rows
}

// connectedText is the green subtitle of the session header.
func connectedText(frames float64) string {
	if frames < 1 {
		return "Connected"
	}
	return fmt.Sprintf("Connected · %.0f fps", frames)
}

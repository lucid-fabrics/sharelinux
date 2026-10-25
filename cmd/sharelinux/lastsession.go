package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// lastSession is what the home screen remembers of the most recent session
// with a Mac: the picture size, how the picture was sent and whether it had sound.
type lastSession struct {
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Codec is the plain name of the picture stream, such as "HEVC 4:4:4".
	Codec string `json:"codec,omitempty"`
	HDR   bool   `json:"hdr,omitempty"`
	Sound bool   `json:"sound,omitempty"`
}

// chip is one small label under the name of the last used Mac.
type chip struct {
	Text string
	// HDR draws the label in the HDR colour.
	HDR bool
}

// chips are the labels of the last session, in the order the design shows them.
func (l lastSession) chips() []chip {
	var result []chip
	if l.Width > 0 && l.Height > 0 {
		result = append(result, chip{Text: fmt.Sprintf("%d × %d", l.Width, l.Height)})
	}
	if l.Codec != "" {
		result = append(result, chip{Text: l.Codec})
	}
	if l.HDR {
		result = append(result, chip{Text: "HDR", HDR: true})
	}
	if l.Sound {
		result = append(result, chip{Text: "Sound on"})
	}
	return result
}

// thumbnailPath is where the last frame of a Mac is kept. The name is a hash of
// the connection id, so no host or account name lands in a file name.
func thumbnailPath(cacheDir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(cacheDir, "thumbnails", hex.EncodeToString(sum[:8])+".png")
}

// lastFrameText is the badge over the last frame, such as "LAST FRAME, 2 MIN AGO".
func lastFrameText(then, current time.Time) string {
	if then.IsZero() {
		return "LAST FRAME"
	}
	return "LAST FRAME, " + strings.ToUpper(shortAgo(then, current))
}

// shortAgo is agoText with minutes shortened, for a badge.
func shortAgo(then, current time.Time) string {
	text := agoText(then, current)
	text = strings.Replace(text, " minutes ago", " min ago", 1)
	text = strings.Replace(text, " minute ago", " min ago", 1)
	return text
}

// codecChipText names the stream for the chip: the codec, with 4:4:4 when the
// picture keeps full colour detail.
func codecChipText(codec string, fullColor bool) string {
	if codec == "" {
		return ""
	}
	if fullColor {
		return codec + " 4:4:4"
	}
	return codec
}

// lastUsedMac picks the Mac the hero card shows: the one used most recently.
// It returns false when none was ever used.
func lastUsedMac(saved []savedConnection) (savedConnection, bool) {
	best := -1
	for index, connection := range saved {
		if connection.LastUsed.IsZero() {
			continue
		}
		if best < 0 || connection.LastUsed.After(saved[best].LastUsed) {
			best = index
		}
	}
	if best < 0 {
		return savedConnection{}, false
	}
	return saved[best], true
}

// recentMacs are the saved Macs that were ever used, newest first.
func recentMacs(saved []savedConnection) []savedConnection {
	var result []savedConnection
	for _, connection := range saved {
		if !connection.LastUsed.IsZero() {
			result = append(result, connection)
		}
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].LastUsed.After(result[j-1].LastUsed); j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

// summarizeSession describes a session that is ending: the picture size, how it
// was sent (a video stream or the plain screen updates), HDR and sound.
func summarizeSession(width, height int, video videoStats, encoding rfb.Encoding, muted bool) lastSession {
	last := lastSession{Width: width, Height: height}
	if !video.Active {
		last.Codec = codecLabel(encoding)
		return last
	}
	last.Codec = codecChipText(codecName(video.Codec), is444TenBit(video.Color) || is444EightBit(video.Color))
	last.HDR = is444TenBit(video.Color) && (video.Color.Transfer == media.TransferPQ || video.Color.Transfer == media.TransferHLG)
	last.Sound = video.SoundOn && !muted
	return last
}

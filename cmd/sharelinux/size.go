package main

import (
	"strconv"
	"strings"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

// parseSize reads a "WxH" pair such as 1920x1080.
func parseSize(value string) (int, int, error) {
	widthText, heightText, found := strings.Cut(value, "x")
	if !found {
		return 0, 0, E.New("size ", strconv.Quote(value), " is not WxH")
	}
	width, err := strconv.Atoi(widthText)
	if err != nil {
		return 0, 0, err
	}
	height, err := strconv.Atoi(heightText)
	if err != nil {
		return 0, 0, err
	}
	return width, height, nil
}

// parseQuality maps a quality name to its rfb value.
func parseQuality(name string) (rfb.Quality, error) {
	for quality := rfb.QualityAdaptive; quality <= rfb.QualityLow; quality++ {
		if quality.String() == name {
			return quality, nil
		}
	}
	return 0, E.New("unknown quality ", strconv.Quote(name))
}

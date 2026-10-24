package main

import (
	"strconv"
	"strings"

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

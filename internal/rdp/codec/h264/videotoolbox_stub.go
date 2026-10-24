//go:build !darwin || !cgo

package h264

import E "github.com/sagernet/sing/common/exceptions"

type videoSession struct{}

func Available() bool {
	return false
}

func (s *videoSession) decode(units [][]byte, width, height int) (picture, bool, error) {
	return picture{}, false, E.New("h264: hardware decoding is not supported on this platform")
}

func (s *videoSession) release() {}

func (s *videoSession) close() {}

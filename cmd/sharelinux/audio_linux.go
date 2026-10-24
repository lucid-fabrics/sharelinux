//go:build linux

package main

/*
#cgo pkg-config: libavcodec libavutil libpulse-simple glib-2.0
#include <stdlib.h>
#include "audio.h"
*/
import "C"

import (
	"time"
	"unsafe"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
	E "github.com/sagernet/sing/common/exceptions"
)

func init() {
	soundDecoderAvailable = func() bool { return C.oss_audio_decoder_available() != 0 }
}

// cDecoder is a libavcodec AAC decoder.
type cDecoder struct {
	c        *C.OssAudioDecoder
	channels int
	frames   int
	buffer   []int16
}

func openAudioDecoder(format media.AudioFormat) (*cDecoder, error) {
	config := format.AudioSpecificConfig
	if len(config) == 0 {
		return nil, E.New("the sound came without a decoder configuration")
	}
	cConfig := C.CBytes(config)
	defer C.free(cConfig)
	var message [200]C.char
	handle := C.oss_audio_open(C.int(format.SampleRate), C.int(format.Channels), (*C.guint8)(cConfig), C.int(len(config)), &message[0], C.int(len(message)))
	if handle == nil {
		return nil, E.New(C.GoString(&message[0]))
	}
	frames := format.FramesPerPacket
	if frames <= 0 {
		frames = audioFramesPerPacket
	}
	return &cDecoder{c: handle, channels: format.Channels, frames: frames, buffer: make([]int16, frames*format.Channels)}, nil
}

// Decode returns the samples of one packet. The slice is reused by the next call.
func (d *cDecoder) Decode(data []byte) ([]int16, error) {
	if len(data) == 0 {
		return nil, E.New("empty sound packet")
	}
	n := int(C.oss_audio_decode(d.c, (*C.guint8)(unsafe.Pointer(&data[0])), C.gsize(len(data)), (*C.gint16)(unsafe.Pointer(&d.buffer[0])), C.int(d.frames)))
	if n < 0 {
		return nil, E.New("sound decoder error ", n)
	}
	return d.buffer[:n*d.channels], nil
}

func (d *cDecoder) Close() {
	if d.c != nil {
		C.oss_audio_close(d.c)
		d.c = nil
	}
}

// cDevice is a playback stream on the sound server.
type cDevice struct {
	c        *C.OssAudioOut
	channels int
}

func openAudioDevice(format media.AudioFormat) (*cDevice, error) {
	name := C.CString("Screen Sharing")
	defer C.free(unsafe.Pointer(name))
	var message [200]C.char
	handle := C.oss_audio_out_open(C.int(format.SampleRate), C.int(format.Channels), C.int(audioDeviceLatency.Milliseconds()), name, &message[0], C.int(len(message)))
	if handle == nil {
		return nil, E.New(C.GoString(&message[0]))
	}
	return &cDevice{c: handle, channels: format.Channels}, nil
}

func (d *cDevice) Write(samples []int16) error {
	if len(samples) == 0 {
		return nil
	}
	if code := int(C.oss_audio_out_write(d.c, (*C.gint16)(unsafe.Pointer(&samples[0])), C.gsize(len(samples)/d.channels))); code < 0 {
		return E.New("sound server write error ", code)
	}
	return nil
}

func (d *cDevice) Flush() { C.oss_audio_out_flush(d.c) }

func (d *cDevice) Latency() time.Duration {
	us := int64(C.oss_audio_out_latency_us(d.c))
	if us < 0 {
		return 0
	}
	return time.Duration(us) * time.Microsecond
}

func (d *cDevice) Close() {
	if d.c != nil {
		C.oss_audio_out_close(d.c)
		d.c = nil
	}
}

// openSoundPlayer starts a player for the format the Mac announced. The decoder and the device are
// opened on the player's goroutine.
func openSoundPlayer(format media.AudioFormat, failed func(error)) *audioPlayer {
	frames := format.FramesPerPacket
	if frames <= 0 {
		frames = audioFramesPerPacket
	}
	return newAudioPlayer(format.Channels, frames, func() (audioDecoder, audioDevice, error) {
		decoder, err := openAudioDecoder(format)
		if err != nil {
			return nil, nil, err
		}
		device, err := openAudioDevice(format)
		if err != nil {
			decoder.Close()
			return nil, nil, err
		}
		return decoder, device, nil
	}, failed)
}

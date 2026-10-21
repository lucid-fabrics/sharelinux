//go:build linux

package main

import (
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

// The GPU video processor of a display goes when the decoder that opened the display closes, whatever
// other decoders are alive and in whatever order they close; a decoder that never made it leaves it be.
func TestVideoProcessorGoesWithItsDecoder(t *testing.T) {
	clip := builtinClip(t)
	format := clipFormat(clip)

	owner, err := openDecoder(decoderOptions{
		Codec:   media.CodecHEVC,
		Kind:    decoderSoftware,
		Threads: 1,
	}, format)
	if err != nil {
		t.Fatalf("open owner decoder: %v", err)
	}
	defer owner.close()

	other, err := openDecoder(decoderOptions{
		Codec:   media.CodecHEVC,
		Kind:    decoderSoftware,
		Threads: 1,
	}, format)
	if err != nil {
		t.Fatalf("open other decoder: %v", err)
	}
	defer other.close()

	claimVideoProcessorForTest(owner, 0x1000)
	if got := videoProcessorOwnerForTest(); got != 0x1000 {
		t.Fatalf("video processor owner is %#x, want %#x", got, 0x1000)
	}

	other.close()
	if got := videoProcessorOwnerForTest(); got != 0x1000 {
		t.Fatalf("video processor owner after closing other decoder is %#x, want %#x", got, 0x1000)
	}

	third, err := openDecoder(decoderOptions{
		Codec:   media.CodecHEVC,
		Kind:    decoderSoftware,
		Threads: 1,
	}, format)
	if err != nil {
		t.Fatalf("open third decoder: %v", err)
	}
	defer third.close()

	owner.close()
	if got := videoProcessorOwnerForTest(); got != 0 {
		t.Fatalf("video processor owner after closing owner decoder is %#x, want %#x", got, 0)
	}

	third.close()
	if got := videoProcessorOwnerForTest(); got != 0 {
		t.Fatalf("video processor owner at the end is %#x", got)
	}
}

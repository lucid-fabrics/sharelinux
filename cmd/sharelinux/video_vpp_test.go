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

// A 10 bit RGB picture that failed because the video processor could not make the surface (its context
// or surface failed) falls back for that picture only; only the display's own refusal of the format, after
// the processor made it, locks the process to P010.
func TestRGB10RefusalNeedsTheDisplayToTurnItDown(t *testing.T) {
	const (
		ok          = 0
		unsupported = 1
		failed      = 2
	)
	defer rgb10OutcomeForTest(true, false, ok)
	if rgb10OutcomeForTest(true, false, unsupported) {
		t.Fatal("a processor that could not make the surface locked HDR to P010")
	}
	if rgb10OutcomeForTest(false, true, failed) {
		t.Fatal("a failed import locked HDR to P010")
	}
	if rgb10OutcomeForTest(false, true, ok) {
		t.Fatal("a good picture locked HDR to P010")
	}
	if !rgb10OutcomeForTest(false, true, unsupported) {
		t.Fatal("the display's refusal of the format was not remembered")
	}
	if !rgb10OutcomeForTest(false, false, unsupported) {
		t.Fatal("the refusal was forgotten by a later transient failure")
	}
}

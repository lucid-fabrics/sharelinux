package main

import (
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func TestParseSize(t *testing.T) {
	w, h, err := parseSize("1920x1080")
	if err != nil || w != 1920 || h != 1080 {
		t.Fatalf("got %d %d %v", w, h, err)
	}
	for _, bad := range []string{"1920", "ax2", "2xb", ""} {
		if _, _, err := parseSize(bad); err == nil {
			t.Fatalf("%q should fail", bad)
		}
	}
}

func TestParseQualityRoundTrips(t *testing.T) {
	for quality := rfb.QualityAdaptive; quality <= rfb.QualityLow; quality++ {
		got, err := parseQuality(quality.String())
		if err != nil || got != quality {
			t.Fatalf("%v: got %v %v", quality, got, err)
		}
	}
	if _, err := parseQuality("ultra"); err == nil {
		t.Fatal("unknown name should fail")
	}
}

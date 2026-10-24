package main

import "testing"

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

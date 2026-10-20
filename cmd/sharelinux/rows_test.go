package main

import (
	"testing"
	"time"
)

func TestAgoText(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{time.Minute, "1 minute ago"},
		{45 * time.Minute, "45 minutes ago"},
		{2 * time.Hour, "2 hours ago"},
		{30 * time.Hour, "yesterday"},
		{3 * 24 * time.Hour, "3 days ago"},
		{65 * 24 * time.Hour, "2 months ago"},
		{800 * 24 * time.Hour, "2 years ago"},
	}
	for _, c := range cases {
		if got := agoText(now.Add(-c.ago), now); got != c.want {
			t.Errorf("%v: %q, want %q", c.ago, got, c.want)
		}
	}
	if agoText(time.Time{}, now) != "never used" {
		t.Fatal("zero time")
	}
}

func TestSavedSubtitle(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	mac := savedConnection{User: "wassim", Host: "192.168.1.218", Port: 5900, LastUsed: now.Add(-2 * time.Hour)}
	if got := mac.subtitle(now); got != "wassim on 192.168.1.218 · last used 2 hours ago" {
		t.Fatal(got)
	}
	vnc := savedConnection{Host: "mac.local", Port: 5901}
	if got := vnc.subtitle(now); got != "VNC password on mac.local:5901" {
		t.Fatal(got)
	}
}

func TestDisplayNameFallsBackToAddress(t *testing.T) {
	if (savedConnection{Name: "Studio", Host: "h", Port: 5900}).displayName() != "Studio" {
		t.Fatal("name")
	}
	if (savedConnection{Host: "h", Port: 5900}).displayName() != "h:5900" {
		t.Fatal("address")
	}
}

func TestTileColorIsStable(t *testing.T) {
	if tileColor("a@b:5900") != tileColor("a@b:5900") {
		t.Fatal("same identity, same colour")
	}
	known := map[string]bool{}
	for _, name := range tilePalette {
		known[name] = true
	}
	for _, id := range []string{"", "x", "y@z:1", "wassim@192.168.1.218:5900"} {
		if !known[tileColor(id)] {
			t.Errorf("%q: unknown colour", id)
		}
	}
}

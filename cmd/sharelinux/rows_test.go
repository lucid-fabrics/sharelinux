package main

import (
	"strings"
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
	mac := savedConnection{User: "alice", Host: "192.0.2.218", Port: 5900, LastUsed: now.Add(-2 * time.Hour)}
	if got := mac.subtitle(now); got != "alice on 192.0.2.218 · last used 2 hours ago" {
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

func TestExclusiveTooltipIsHonestWhereThePointerIsNotHeld(t *testing.T) {
	none := exclusiveTooltip(lockNone)
	if !strings.Contains(none, "Captures the keyboard only") {
		t.Fatalf("without a lock only the keyboard is captured: %q", none)
	}
	if strings.Contains(none, "keeps the pointer") || strings.Contains(none, "locks the pointer") {
		t.Fatalf("must not promise what it cannot do: %q", none)
	}
	if !strings.Contains(exclusiveAnnouncement(lockNone), "keyboard only") {
		t.Fatal(exclusiveAnnouncement(lockNone))
	}
}

func TestExclusiveTooltipDropsTheKeyboardOnlyWordingWhereTheLockWorks(t *testing.T) {
	for _, backend := range []lockBackend{lockConfine, lockX11, lockWayland} {
		if strings.Contains(exclusiveTooltip(backend), "keyboard only") || strings.Contains(exclusiveTooltip(backend), "Captures the keyboard") {
			t.Errorf("backend %d: %q", backend, exclusiveTooltip(backend))
		}
		if exclusiveAnnouncement(backend) != "" {
			t.Errorf("backend %d: the hint pill speaks, no toast: %q", backend, exclusiveAnnouncement(backend))
		}
	}
	if !strings.Contains(exclusiveTooltip(lockConfine), "keeps the pointer inside") {
		t.Fatal("barriers keep the pointer inside the window")
	}
	for _, backend := range []lockBackend{lockX11, lockWayland} {
		if !strings.Contains(exclusiveTooltip(backend), "locks the pointer") {
			t.Fatalf("backend %d: %q", backend, exclusiveTooltip(backend))
		}
	}
}

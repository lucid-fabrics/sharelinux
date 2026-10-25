package main

import (
	"fmt"
	"strings"
	"time"
)

// agoText says how long ago a moment was, in the plain words of the connection list.
func agoText(then, now time.Time) string {
	if then.IsZero() {
		return "never used"
	}
	elapsed := now.Sub(then)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return unitsAgo(int(elapsed/time.Minute), "minute")
	case elapsed < 24*time.Hour:
		return unitsAgo(int(elapsed/time.Hour), "hour")
	case elapsed < 48*time.Hour:
		return "yesterday"
	case elapsed < 30*24*time.Hour:
		return unitsAgo(int(elapsed/(24*time.Hour)), "day")
	case elapsed < 365*24*time.Hour:
		return unitsAgo(int(elapsed/(30*24*time.Hour)), "month")
	}
	return unitsAgo(int(elapsed/(365*24*time.Hour)), "year")
}

func unitsAgo(count int, unit string) string {
	if count == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", count, unit)
}

// subtitle is the second line of a saved Mac: who, where and when.
func (c savedConnection) subtitle(now time.Time) string {
	where := c.Host
	if c.Port != defaultPort {
		where = c.target().Address()
	}
	who := "VNC password on " + where
	if c.User != "" {
		who = c.User + " on " + where
	}
	if c.LastUsed.IsZero() {
		return who
	}
	return who + " · last used " + agoText(c.LastUsed, now)
}

// displayName is what the list calls a connection.
func (c savedConnection) displayName() string {
	if strings.TrimSpace(c.Name) != "" {
		return c.Name
	}
	return c.target().Address()
}

// reachability is the status dot of a saved Mac.
type reachability int

const (
	reachUnknown reachability = iota
	reachOnline
	reachOffline
)

func (r reachability) String() string {
	switch r {
	case reachOnline:
		return "Online"
	case reachOffline:
		return "Offline"
	}
	return ""
}

// exclusiveTooltip says what Exclusive mode really does on this display server,
// so the button never promises a lock the server would not give.
func exclusiveTooltip(backend lockBackend) string {
	switch {
	case backend.relative():
		return "Sends every key, Super and Alt+Tab included, to the Mac and locks the pointer to this window so it can move past the edge. Ctrl+Alt+Esc releases it"
	case backend.holds():
		return "Sends every key, Super and Alt+Tab included, to the Mac and keeps the pointer inside this window"
	}
	return "Captures the keyboard only: Super and Alt+Tab go to the Mac. This display server does not let an app hold the pointer, so it can still leave the window"
}

// exclusiveAnnouncement is the toast shown when Exclusive mode starts. It is
// empty where the pointer is held: the hint pill already says it all. Only the
// keyboard-only case needs words, because the pill cannot promise a lock.
func exclusiveAnnouncement(backend lockBackend) string {
	if backend.holds() {
		return ""
	}
	return "Exclusive: keyboard only, the pointer can leave the window"
}

// lockFailedAnnouncement is the toast shown when the pointer could not be held
// although the display server should allow it.
const lockFailedAnnouncement = "Exclusive: the pointer could not be held, so only the keyboard is captured"

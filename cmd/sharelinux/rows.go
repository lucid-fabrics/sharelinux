package main

import (
	"fmt"
	"hash/fnv"
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

// tilePalette is the Adwaita palette the icon tiles draw from.
var tilePalette = []string{"blue", "purple", "green", "orange", "teal", "pink", "slate"}

// tileColor picks a stable tile colour from a connection's identity.
func tileColor(id string) string {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(id))
	return tilePalette[int(sum.Sum32())%len(tilePalette)]
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

//go:build linux

package main

import "testing"

func TestArtWithKeepsPersistedArt(t *testing.T) {
	saved := savedConnection{Name: "Alice's Mac", Host: "10.0.0.5", Port: 5900, Art: "macstudio"}
	if got := artWith(saved, nil); got != artStudio {
		t.Errorf("kept art lost: %s", got)
	}
	saved.Art = "bogus"
	if got := artWith(saved, nil); got != artDisplay {
		t.Errorf("bogus kept art: %s", got)
	}
	saved.Name = "cicd's iMac Pro"
	if got := artWith(saved, nil); got != artIMac {
		t.Errorf("name hint: %s", got)
	}
}

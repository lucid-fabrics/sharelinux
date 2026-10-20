package main

import "testing"

func TestClickFilterAllowsExpectedEvents(t *testing.T) {
	const millisecond uint64 = 1_000_000

	var lone clickFilter
	if !lone.allow(1, true, millisecond) {
		t.Fatal("a lone press should be allowed")
	}
	if !lone.allow(1, false, 2*millisecond) {
		t.Fatal("a lone release should be allowed")
	}

	var duplicate clickFilter
	if !duplicate.allow(1, true, millisecond) {
		t.Fatal("the first press should be allowed")
	}
	if !duplicate.allow(1, false, 2*millisecond) {
		t.Fatal("the first release should be allowed")
	}
	if duplicate.allow(1, true, 2_100_000) {
		t.Fatal("the echoed press should be rejected")
	}
	if !duplicate.allow(1, false, 2_200_000) {
		t.Fatal("the second release should be allowed")
	}

	var delayed clickFilter
	if !delayed.allow(1, false, 10*millisecond) {
		t.Fatal("the release should be allowed")
	}
	if !delayed.allow(1, true, 60*millisecond) {
		t.Fatal("a press 50ms after a release should be allowed")
	}

	var independent clickFilter
	if !independent.allow(1, false, millisecond) {
		t.Fatal("the first button release should be allowed")
	}
	if !independent.allow(2, true, 1_100_000) {
		t.Fatal("a press of another button should be allowed")
	}
	if independent.allow(1, true, 1_100_000) {
		t.Fatal("the first button's echoed press should be rejected")
	}

	var unsupported clickFilter
	if !unsupported.allow(5, true, millisecond) {
		t.Fatal("button 5 press should always be allowed")
	}
	if !unsupported.allow(5, false, 2*millisecond) {
		t.Fatal("button 5 release should always be allowed")
	}
}

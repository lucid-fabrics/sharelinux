package main

import "testing"

func TestHostKey(t *testing.T) {
	tests := []struct {
		name  string
		first string
		other string
	}{
		{
			name:  "case and local suffix",
			first: "Mac.LOCAL.",
			other: "mac",
		},
		{
			name:  "bracketed IPv6",
			first: "[2001:DB8::5]",
			other: "2001:db8::5",
		},
		{
			name:  "IPv6 zone",
			first: "fe80::1%eth0",
			other: "fe80::1",
		},
		{
			name:  "IPv4-mapped IPv6",
			first: "::ffff:192.168.1.5",
			other: "192.168.1.5",
		},
		{
			name:  "surrounding whitespace",
			first: " 192.168.1.5 ",
			other: "192.168.1.5",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := hostKey(test.first)
			other := hostKey(test.other)
			if first != other {
				t.Fatalf("hostKey(%q) = %q, want %q", test.first, first, other)
			}
		})
	}
}

func TestMatchesSavedMac(t *testing.T) {
	mac := discoveredMac{
		Name:      "Studio",
		Host:      "studio.local",
		Addresses: []string{"192.168.1.5", "2001:db8::5"},
		Port:      5900,
	}

	tests := []struct {
		name  string
		saved savedConnection
		want  bool
	}{
		{
			name:  "by host",
			saved: savedConnection{Host: "STUDIO.local.", Port: 5900},
			want:  true,
		},
		{
			name:  "by IPv4",
			saved: savedConnection{Host: "192.168.1.5", Port: 5900},
			want:  true,
		},
		{
			name:  "by IPv6",
			saved: savedConnection{Host: "2001:0db8:0:0:0:0:0:5", Port: 5900},
			want:  true,
		},
		{
			name:  "by name",
			saved: savedConnection{Name: "studio", Port: 5900},
			want:  true,
		},
		{
			name:  "different port",
			saved: savedConnection{Host: "studio.local", Port: 5901},
			want:  false,
		},
		{
			name:  "different Mac",
			saved: savedConnection{Name: "Other", Host: "other", Port: 5900},
			want:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mac.matches(test.saved); got != test.want {
				t.Fatalf("matches(%+v) = %t, want %t", test.saved, got, test.want)
			}
		})
	}
}

func TestIPv6OnlyAnswerStillHiddenWhenSavedByIPv4(t *testing.T) {
	found := []discoveredMac{
		{
			Name:      "Studio",
			Host:      "studio.local",
			Addresses: []string{"2001:db8::5"},
			Port:      5900,
		},
	}

	t.Run("matching name hides Mac", func(t *testing.T) {
		saved := []savedConnection{
			{Name: "Studio", Host: "192.168.1.5", Port: 5900},
		}

		got := unsaved(found, saved)
		if len(got) != 0 {
			t.Fatalf("unsaved returned %d Macs, want none: %+v", len(got), got)
		}
	})

	t.Run("unrelated name does not hide Mac", func(t *testing.T) {
		saved := []savedConnection{
			{Name: "Something else", Host: "192.168.1.5", Port: 5900},
		}

		got := unsaved(found, saved)
		if len(got) != 1 {
			t.Fatalf("unsaved returned %d Macs, want one: %+v", len(got), got)
		}
		if got[0].Name != "Studio" {
			t.Fatalf("unsaved returned %q, want %q", got[0].Name, "Studio")
		}
	})
}

func TestUnsavedKeepsUnknownMacs(t *testing.T) {
	t.Run("keeps unknown Mac", func(t *testing.T) {
		found := []discoveredMac{
			{Name: "Zulu", Host: "zulu.local", Port: 5900},
			{Name: "Alpha", Host: "alpha.local", Port: 5900},
		}
		saved := []savedConnection{
			{Name: "Alpha", Host: "alpha.local", Port: 5900},
		}

		got := unsaved(found, saved)
		if len(got) != 1 {
			t.Fatalf("unsaved returned %d Macs, want one: %+v", len(got), got)
		}
		if got[0].Name != "Zulu" {
			t.Fatalf("unsaved returned %q, want %q", got[0].Name, "Zulu")
		}
	})

	t.Run("sorts unknown Macs by name", func(t *testing.T) {
		found := []discoveredMac{
			{Name: "Zulu", Host: "zulu.local", Port: 5900},
			{Name: "Beta", Host: "beta.local", Port: 5900},
			{Name: "Alpha", Host: "alpha.local", Port: 5900},
		}
		saved := []savedConnection{
			{Name: "Beta", Host: "beta.local", Port: 5900},
		}

		got := unsaved(found, saved)
		if len(got) != 2 {
			t.Fatalf("unsaved returned %d Macs, want two: %+v", len(got), got)
		}
		if got[0].Name != "Alpha" || got[1].Name != "Zulu" {
			t.Fatalf("unsaved returned names %q, %q; want %q, %q", got[0].Name, got[1].Name, "Alpha", "Zulu")
		}
	})
}

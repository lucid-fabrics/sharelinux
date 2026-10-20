package main

import "testing"

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		want target
		bad  bool
	}{
		{in: "mac.local", want: target{Host: "mac.local", Port: 5900}},
		{in: "me@mac.local", want: target{User: "me", Host: "mac.local", Port: 5900}},
		{in: "me@mac.local:5901", want: target{User: "me", Host: "mac.local", Port: 5901}},
		{in: "vnc://me@10.0.0.2/", want: target{User: "me", Host: "10.0.0.2", Port: 5900}},
		{in: "me@example.com@mac:5902", want: target{User: "me@example.com", Host: "mac", Port: 5902}},
		{in: "[::1]", want: target{Host: "::1", Port: 5900}},
		{in: "u@[fe80::1]:5905", want: target{User: "u", Host: "fe80::1", Port: 5905}},
		{in: "::1", want: target{Host: "::1", Port: 5900}},
		{in: "", bad: true},
		{in: "me@", bad: true},
		{in: "host:abc", bad: true},
		{in: "host:70000", bad: true},
		{in: "[::1", bad: true},
		{in: "[::1]x", bad: true},
	}
	for _, c := range cases {
		got, err := parseTarget(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%q: expected an error, got %+v", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %+v, %v want %+v", c.in, got, err, c.want)
		}
	}
}

func TestTargetAddress(t *testing.T) {
	if got := (target{User: "a", Host: "::1", Port: 5900}).Address(); got != "[::1]:5900" {
		t.Fatal(got)
	}
	if got := (target{User: "a", Host: "h", Port: 1}).ID(); got != "a@h:1" {
		t.Fatal(got)
	}
}

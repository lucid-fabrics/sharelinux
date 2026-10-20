package main

import "testing"

func TestLoginFormResult(t *testing.T) {
	form := newLoginForm(target{}, false)
	if form.Focus != fieldHost {
		t.Fatal("empty form starts on the host")
	}
	form.Type("mac.local:5901")
	form.Next()
	form.Type("me")
	form.Next()
	form.Type("pw\x00 1")
	got, password, err := form.Result()
	if err != nil {
		t.Fatal(err)
	}
	if got != (target{User: "me", Host: "mac.local", Port: 5901}) || password != "pw 1" {
		t.Fatalf("%+v %q", got, password)
	}
	form.Fields[fieldHost].Value = nil
	if _, _, err = form.Result(); err == nil {
		t.Fatal("empty host must fail")
	}
}

func TestLoginFormSeedAndEdit(t *testing.T) {
	form := newLoginForm(target{User: "me", Host: "mac", Port: 5900}, true)
	if form.Focus != fieldPassword || form.Fields[fieldHost].text() != "mac" || !form.Remember {
		t.Fatalf("%+v", form)
	}
	form.Type("abc")
	if form.Fields[fieldPassword].display() != "***" {
		t.Fatal("password is masked")
	}
	form.Backspace()
	form.Backspace()
	form.Backspace()
	form.Backspace()
	if len(form.Fields[fieldPassword].Value) != 0 {
		t.Fatal("backspace on empty is a no-op")
	}
	form.Previous()
	if form.Focus != fieldUser {
		t.Fatal(form.Focus)
	}
	form.Next()
	form.Next()
	if form.Focus != fieldHost {
		t.Fatal("focus wraps")
	}
	seeded := newLoginForm(target{Host: "mac", Port: 5901}, false)
	if seeded.Fields[fieldHost].text() != "mac:5901" || seeded.Focus != fieldUser {
		t.Fatalf("%+v", seeded)
	}
}

func TestPicker(t *testing.T) {
	list := newPicker([]savedConnection{{Name: "a"}, {Name: "b"}})
	if list.Rows() != 3 {
		t.Fatal(list.Rows())
	}
	list.Move(-1)
	if _, saved := list.Current(); saved {
		t.Fatal("wraps to the New connection row")
	}
	if _, removed := list.Remove(); removed {
		t.Fatal("cannot remove the new row")
	}
	list.Move(1)
	list.Move(1)
	removed, ok := list.Remove()
	if !ok || removed.Name != "b" || len(list.Items) != 1 {
		t.Fatal(removed, ok, list.Items)
	}
	if list.Selected != 1 {
		t.Fatal("selection stays in range", list.Selected)
	}
}

func TestEllipsizeAndWrap(t *testing.T) {
	if ellipsize("abcdef", 4) != "abc~" || ellipsize("abc", 4) != "abc" || ellipsize("abc", 0) != "" {
		t.Fatal("ellipsize")
	}
	lines := wrapText("one two three four", 10)
	if len(lines) != 2 || lines[0] != "one two" || lines[1] != "three four" {
		t.Fatalf("%q", lines)
	}
	lines = wrapText("aaaaaaaaaaaaaaaaaaaa", 8)
	if len(lines) != 3 || lines[0] != "aaaaaaaa" {
		t.Fatalf("%q", lines)
	}
	lines = wrapText("a\nb", 8)
	if len(lines) != 2 {
		t.Fatalf("%q", lines)
	}
}

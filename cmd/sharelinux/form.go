package main

import (
	"strings"
	"unicode/utf8"
)

// field is one line of the login form.
type field struct {
	Label  string
	Value  []rune
	Secret bool
}

func (f *field) text() string {
	return string(f.Value)
}

func (f *field) display() string {
	if f.Secret {
		return strings.Repeat("*", len(f.Value))
	}
	return f.text()
}

// loginForm is the host, user and password a connection needs, plus whether to remember it.
type loginForm struct {
	Fields   []field
	Focus    int
	Remember bool
	Error    string
}

const (
	fieldHost = iota
	fieldUser
	fieldPassword
	fieldCount
)

func newLoginForm(t target, remember bool) *loginForm {
	form := &loginForm{
		Fields:   make([]field, fieldCount),
		Remember: remember,
	}
	form.Fields[fieldHost] = field{Label: "Host"}
	form.Fields[fieldUser] = field{Label: "Mac user (empty for a VNC password)"}
	form.Fields[fieldPassword] = field{Label: "Password", Secret: true}
	if t.Host != "" {
		form.Fields[fieldHost].Value = []rune(t.Address())
		if t.Port == defaultPort {
			form.Fields[fieldHost].Value = []rune(t.Host)
		}
	}
	form.Fields[fieldUser].Value = []rune(t.User)
	if t.Host != "" {
		form.Focus = fieldPassword
		if t.User == "" {
			form.Focus = fieldUser
		}
	}
	return form
}

func (f *loginForm) Type(text string) {
	field := &f.Fields[f.Focus]
	for _, character := range text {
		if character >= ' ' && character != 0x7f {
			field.Value = append(field.Value, character)
		}
	}
}

func (f *loginForm) Backspace() {
	field := &f.Fields[f.Focus]
	if len(field.Value) > 0 {
		field.Value = field.Value[:len(field.Value)-1]
	}
}

func (f *loginForm) Next() {
	f.Focus = (f.Focus + 1) % len(f.Fields)
}

func (f *loginForm) Previous() {
	f.Focus = (f.Focus + len(f.Fields) - 1) % len(f.Fields)
}

// Result reads the form back into a target and a password.
func (f *loginForm) Result() (target, string, error) {
	user := strings.TrimSpace(f.Fields[fieldUser].text())
	value := strings.TrimSpace(f.Fields[fieldHost].text())
	if user != "" && !strings.Contains(value, "@") {
		value = user + "@" + value
	}
	result, err := parseTarget(value)
	if err != nil {
		return target{}, "", err
	}
	if result.User == "" {
		result.User = user
	}
	return result, f.Fields[fieldPassword].text(), nil
}

// picker is the list of saved connections with a trailing "new connection" row.
type picker struct {
	Items    []savedConnection
	Selected int
}

func newPicker(items []savedConnection) *picker {
	return &picker{Items: items}
}

// Rows is the number of selectable rows: every connection and "New connection".
func (p *picker) Rows() int {
	return len(p.Items) + 1
}

func (p *picker) Move(delta int) {
	p.Selected = (p.Selected + delta + p.Rows()) % p.Rows()
}

// Current is the selected connection, or false on the "New connection" row.
func (p *picker) Current() (savedConnection, bool) {
	if p.Selected >= len(p.Items) {
		return savedConnection{}, false
	}
	return p.Items[p.Selected], true
}

// Remove drops the selected connection from the list and keeps the selection in range.
func (p *picker) Remove() (savedConnection, bool) {
	connection, found := p.Current()
	if !found {
		return connection, false
	}
	p.Items = append(p.Items[:p.Selected], p.Items[p.Selected+1:]...)
	p.Selected = min(p.Selected, p.Rows()-1)
	return connection, true
}

// ellipsize shortens text to width characters for the fixed-width debug font.
func ellipsize(text string, width int) string {
	if utf8.RuneCountInString(text) <= width {
		return text
	}
	runes := []rune(text)
	if width <= 1 {
		return string(runes[:max(width, 0)])
	}
	return string(runes[:width-1]) + "~"
}

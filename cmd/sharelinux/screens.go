package main

import (
	"fmt"

	"github.com/Zyko0/go-sdl3/sdl"
)

const uiScale = 2

// nextEvent waits for an event, or returns nil after the timeout.
func nextEvent(timeoutMS int32) *sdl.Event {
	var event sdl.Event
	if sdl.WaitEventTimeout(&event, timeoutMS) {
		return &event
	}
	return nil
}

func isQuit(event *sdl.Event) bool {
	return event.Type == sdl.EVENT_QUIT || event.Type == sdl.EVENT_WINDOW_CLOSE_REQUESTED
}

// pickerResult is what the picker screen chose.
type pickerResult struct {
	Connection savedConnection
	Saved      bool
}

// runPicker lists saved connections and returns the one chosen, or Saved false
// for "New connection". It returns errQuit when the window closes.
func (a *app) runPicker(items []savedConnection, notice string) (pickerResult, error) {
	list := newPicker(items)
	for {
		a.drawPicker(list, notice)
		event := nextEvent(250)
		if event == nil {
			continue
		}
		if isQuit(event) {
			return pickerResult{}, errQuit
		}
		switch event.Type {
		case sdl.EVENT_KEY_DOWN:
			key := event.KeyboardEvent()
			switch key.Key {
			case sdl.K_UP:
				list.Move(-1)
			case sdl.K_DOWN:
				list.Move(1)
			case sdl.K_RETURN, sdl.K_KP_ENTER:
				connection, saved := list.Current()
				return pickerResult{Connection: connection, Saved: saved}, nil
			case sdl.K_DELETE, sdl.K_BACKSPACE:
				connection, removed := list.Remove()
				if removed {
					err := a.store.Remove(connection)
					notice = "Removed " + connection.Name
					if err != nil {
						notice = "Removed, but: " + err.Error()
					}
				}
			case sdl.K_ESCAPE:
				return pickerResult{}, errQuit
			}
		case sdl.EVENT_MOUSE_BUTTON_DOWN:
			click := event.MouseButtonEvent()
			row := a.pickerRowAt(click.Y)
			if row >= 0 && row < list.Rows() {
				list.Selected = row
				connection, saved := list.Current()
				return pickerResult{Connection: connection, Saved: saved}, nil
			}
		}
	}
}

const (
	pickerTop    = 72
	pickerRowGap = 20
)

// pickerRowAt maps a window y coordinate to a picker row.
func (a *app) pickerRowAt(y float32) int {
	scale := a.canvas.density() * uiScale
	logical := y * a.canvas.density() / scale
	if logical < pickerTop {
		return -1
	}
	return int((logical - pickerTop) / pickerRowGap)
}

func (a *app) drawPicker(list *picker, notice string) {
	c := a.canvas
	width, height := c.begin(uiScale)
	columns := glyphs(width) - 4
	c.text(16, 16, colorAccent, "Open Screen Sharing")
	c.text(16, 36, colorMuted, "Pick a Mac. Enter connects, Delete forgets it, Esc quits.")
	for index := range list.Rows() {
		y := float32(pickerTop + index*pickerRowGap)
		textColor := colorText
		if index == list.Selected {
			c.rect(8, y-4, width-16, pickerRowGap-2, colorPanel)
			textColor = colorAccent
		}
		if index < len(list.Items) {
			item := list.Items[index]
			c.text(16, y, textColor, ellipsize(fmt.Sprintf("%s  (%s)", item.Name, item.target()), columns))
		} else {
			c.text(16, y, textColor, "+ New connection")
		}
	}
	if notice != "" {
		c.text(16, height-24, colorMuted, ellipsize(notice, columns))
	}
	_ = c.renderer.Present()
}

// formResult is a submitted login form.
type formResult struct {
	Target   target
	Password string
	Remember bool
}

// runForm asks for the address and credentials. The form keeps what was typed
// across failed attempts; the caller passes the error to show.
func (a *app) runForm(form *loginForm) (formResult, error) {
	_ = a.canvas.window.StartTextInput()
	defer func() { _ = a.canvas.window.StopTextInput() }()
	for {
		a.drawForm(form)
		event := nextEvent(250)
		if event == nil {
			continue
		}
		if isQuit(event) {
			return formResult{}, errQuit
		}
		switch event.Type {
		case sdl.EVENT_TEXT_INPUT:
			form.Type(event.TextInputEvent().Text)
			form.Error = ""
		case sdl.EVENT_KEY_DOWN:
			key := event.KeyboardEvent()
			switch key.Key {
			case sdl.K_ESCAPE:
				return formResult{}, errQuit
			case sdl.K_BACKSPACE:
				form.Backspace()
			case sdl.K_TAB:
				if key.Mod&sdl.KMOD_SHIFT != 0 {
					form.Previous()
				} else {
					form.Next()
				}
			case sdl.K_DOWN:
				form.Next()
			case sdl.K_UP:
				form.Previous()
			case sdl.K_V:
				if key.Mod&sdl.KMOD_CTRL != 0 {
					text, err := sdl.GetClipboardText()
					if err == nil {
						form.Type(text)
					}
				}
			case sdl.K_RETURN, sdl.K_KP_ENTER:
				result, password, err := form.Result()
				if err != nil {
					form.Error = err.Error()
					continue
				}
				return formResult{Target: result, Password: password, Remember: form.Remember}, nil
			case sdl.K_F2:
				form.Remember = !form.Remember
			}
		}
	}
}

func (a *app) drawForm(form *loginForm) {
	c := a.canvas
	width, height := c.begin(uiScale)
	columns := glyphs(width) - 4
	c.text(16, 16, colorAccent, "Connect to a Mac")
	c.text(16, 36, colorMuted, "Tab moves between fields, Enter connects, F2 remembers it, Esc quits.")
	for index, field := range form.Fields {
		y := float32(72 + index*52)
		labelColor := colorMuted
		if index == form.Focus {
			labelColor = colorAccent
		}
		c.text(16, y, labelColor, field.Label)
		c.rect(12, y+14, width-24, 22, colorPanel)
		shown := field.display()
		if index == form.Focus {
			shown += "_"
		}
		if len(shown) > columns {
			shown = shown[len(shown)-columns:]
		}
		c.text(18, y+21, colorText, shown)
	}
	remember := "[ ]"
	if form.Remember {
		remember = "[x]"
	}
	c.text(16, 72+fieldCount*52, colorText, remember+" Remember this Mac (password goes to the keyring)")
	if form.Error != "" {
		c.text(16, height-40, colorError, ellipsize(form.Error, columns))
	}
	_ = c.renderer.Present()
}

// showMessage shows a message and waits for a key. It returns the key pressed:
// Enter, Escape, or another key code the caller listed in accept.
func (a *app) showMessage(title, body string, accept ...sdl.Keycode) (sdl.Keycode, error) {
	for {
		a.drawMessage(title, body)
		event := nextEvent(250)
		if event == nil {
			continue
		}
		if isQuit(event) {
			return 0, errQuit
		}
		if event.Type == sdl.EVENT_KEY_DOWN {
			key := event.KeyboardEvent().Key
			if key == sdl.K_RETURN || key == sdl.K_ESCAPE {
				return key, nil
			}
			for _, wanted := range accept {
				if key == wanted {
					return key, nil
				}
			}
		}
	}
}

func (a *app) drawMessage(title, body string) {
	c := a.canvas
	width, _ := c.begin(uiScale)
	columns := glyphs(width) - 4
	c.text(16, 16, colorAccent, ellipsize(title, columns))
	y := float32(48)
	for _, line := range wrapText(body, columns) {
		c.text(16, y, colorText, line)
		y += 14
	}
	_ = c.renderer.Present()
}

// wrapText breaks text into lines of at most width characters at spaces.
func wrapText(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var lines []string
	line := ""
	word := ""
	flush := func() {
		if word == "" {
			return
		}
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
		for len(line) > width {
			lines = append(lines, line[:width])
			line = line[width:]
		}
		word = ""
	}
	for _, character := range text {
		switch character {
		case ' ':
			flush()
		case '\n':
			flush()
			lines = append(lines, line)
			line = ""
		default:
			word += string(character)
		}
	}
	flush()
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

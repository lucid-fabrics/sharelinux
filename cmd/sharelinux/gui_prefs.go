//go:build linux

package main

import (
	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// showPreferences opens the preferences dialog. Every change applies at once and is saved.
func (g *gui) showPreferences() {
	prefs := g.app.preferences()
	change := func(f func(*settings)) {
		err := g.app.updatePreferences(f)
		if err != nil {
			g.toast("Preferences were not saved: " + err.Error())
		}
	}
	dialog := adw.NewPreferencesDialog()
	dialog.SetTitle("Preferences")
	dialog.Add(g.generalPage(prefs, change))
	dialog.Add(g.keyboardPage(prefs, change))
	dialog.Add(g.displaysPage(prefs, change))
	dialog.Present(g.window)
}

func switchRow(title, subtitle string, active bool, set func(bool)) *adw.SwitchRow {
	row := adw.NewSwitchRow()
	row.SetTitle(title)
	row.SetSubtitle(subtitle)
	row.SetActive(active)
	row.NotifyProperty("active", func() { set(row.Active()) })
	return row
}

func comboRow(title, subtitle string, options []string, selected int, set func(int)) *adw.ComboRow {
	row := adw.NewComboRow()
	row.SetTitle(title)
	row.SetSubtitle(subtitle)
	row.SetModel(gtk.NewStringList(options))
	row.SetSelected(uint(selected))
	row.NotifyProperty("selected", func() { set(int(row.Selected())) })
	return row
}

func (g *gui) generalPage(prefs settings, change func(func(*settings))) *adw.PreferencesPage {
	page := adw.NewPreferencesPage()
	page.SetTitle("General")
	page.SetIconName("preferences-system-symbolic")

	picture := adw.NewPreferencesGroup()
	picture.SetTitle("Picture")
	var names []string
	selected := 0
	for index, quality := range qualities {
		names = append(names, qualityLabel(quality))
		if quality.String() == prefs.Quality {
			selected = index
		}
	}
	var qualityRow *adw.ComboRow
	qualityRow = comboRow("Quality", qualityHint(qualities[selected]), names, selected, func(index int) {
		change(func(s *settings) { s.Quality = qualities[index].String() })
		qualityRow.SetSubtitle(qualityHint(qualities[index]))
	})
	picture.Add(qualityRow)
	var videoNames []string
	videoSelected := 0
	for index, mode := range videoModes {
		videoNames = append(videoNames, videoModeLabel(mode))
		if string(mode) == prefs.Video {
			videoSelected = index
		}
	}
	picture.Add(comboRow("Video", "Uses the Mac's video stream when this computer can decode it, for one display at a time. "+videoHint(g.app.videoCapabilities())+". Applies to the next connection", videoNames, videoSelected, func(index int) {
		change(func(s *settings) { s.Video = string(videoModes[index]) })
	}))
	picture.Add(switchRow("Fit the Mac to the window", "Off shows the screen at its real size and scrolls", prefs.FitWindow, func(on bool) {
		change(func(s *settings) { s.FitWindow = on })
	}))
	picture.Add(switchRow("Show connection stats", "Frame rate, latency and bandwidth in a corner", prefs.ShowStats, func(on bool) {
		change(func(s *settings) { s.ShowStats = on })
		if g.session != nil {
			g.session.setStats(on)
		}
	}))
	page.Add(picture)

	sharing := adw.NewPreferencesGroup()
	sharing.SetTitle("Sharing")
	sharing.Add(switchRow("Shared clipboard", "Copy on one side, paste on the other", prefs.SharedClipboard, func(on bool) {
		change(func(s *settings) { s.SharedClipboard = on })
	}))
	folder := adw.NewActionRow()
	folder.SetTitle("Files from the Mac")
	folder.SetSubtitle("Where files dragged out of the Mac are saved")
	choose := gtk.NewButtonWithLabel(folderName(prefs.downloadFolder()))
	choose.SetVAlign(gtk.AlignCenter)
	choose.AddCSSClass("flat")
	choose.ConnectClicked(func() {
		dialog := gtk.NewFileDialog()
		dialog.SetTitle("Choose a folder for files from the Mac")
		dialog.SelectFolder(g.ctx, &g.window.Window, func(result gio.AsyncResulter) {
			file, err := dialog.SelectFolderFinish(result)
			if err != nil || file == nil {
				return
			}
			path := file.Path()
			change(func(s *settings) { s.DownloadDir = path })
			choose.SetLabel(folderName(path))
		})
	})
	folder.AddSuffix(choose)
	folder.SetActivatableWidget(choose)
	sharing.Add(folder)
	page.Add(sharing)
	return page
}

func (g *gui) keyboardPage(prefs settings, change func(func(*settings))) *adw.PreferencesPage {
	page := adw.NewPreferencesPage()
	page.SetTitle("Keyboard")
	page.SetIconName("input-keyboard-symbolic")
	group := adw.NewPreferencesGroup()
	group.SetTitle("Keyboard and mouse")

	selected := 0
	if prefs.CommandKey == commandKeyAlt {
		selected = 1
	}
	group.Add(comboRow("Command key", "Which key on this keyboard acts as ⌘ on the Mac", []string{"Super", "Alt"}, selected, func(index int) {
		change(func(s *settings) { s.CommandKey = []string{commandKeySuper, commandKeyAlt}[index] })
	}))

	release := adw.NewActionRow()
	release.SetTitle("Release keyboard and mouse")
	release.SetSubtitle("Leaves Exclusive mode and gives them back to Linux")
	release.AddSuffix(keycaps("Ctrl", "Alt", "Esc"))
	group.Add(release)

	group.Add(switchRow("Natural scrolling", "Follows the Mac's own setting when on", prefs.NaturalScrolling, func(on bool) {
		change(func(s *settings) { s.NaturalScrolling = on })
	}))
	page.Add(group)
	page.Add(g.mouseHelperGroup())
	return page
}

// keycaps draws a shortcut as a row of keys.
func keycaps(keys ...string) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 4)
	box.SetVAlign(gtk.AlignCenter)
	for _, key := range keys {
		cap := gtk.NewLabel(key)
		cap.AddCSSClass("oss-keycap")
		box.Append(cap)
	}
	return box
}

func folderName(path string) string {
	if path == "" {
		return "Downloads"
	}
	return lastElement(path)
}

func (g *gui) displaysPage(prefs settings, change func(func(*settings))) *adw.PreferencesPage {
	page := adw.NewPreferencesPage()
	page.SetTitle("Displays")
	page.SetIconName("video-display-symbolic")

	start := adw.NewPreferencesGroup()
	start.SetTitle("Display")
	selected := 0
	if prefs.MainDisplay {
		selected = 1
	}
	start.Add(comboRow("Display to show", "Which screen of the Mac a new session starts with", []string{"All displays", "Main display"}, selected, func(index int) {
		change(func(s *settings) { s.MainDisplay = index == 1 })
	}))
	page.Add(start)

	virtual := adw.NewPreferencesGroup()
	virtual.SetTitle("Virtual display")
	virtual.SetDescription("A virtual display gives the Mac an extra screen shaped like this window.")
	sizeIndex := 0
	for index, size := range virtualSizes {
		if size == prefs.VirtualSize {
			sizeIndex = index
		}
	}
	sizeNames := []string{"Match the window", "1920×1080", "2560×1440", "3840×2160"}
	sizeRow := comboRow("Size", "In points, before HiDPI", sizeNames, sizeIndex, func(index int) {
		change(func(s *settings) { s.VirtualSize = virtualSizes[index] })
	})
	hidpi := switchRow("HiDPI", "Draw the virtual display at twice the density", prefs.HiDPI, func(on bool) {
		change(func(s *settings) { s.HiDPI = on })
	})
	sizeRow.SetSensitive(prefs.VirtualDisplay)
	hidpi.SetSensitive(prefs.VirtualDisplay)
	virtual.Add(switchRow("Create a virtual display", "Ask the Mac for an extra screen when a session starts", prefs.VirtualDisplay, func(on bool) {
		change(func(s *settings) { s.VirtualDisplay = on })
		sizeRow.SetSensitive(on)
		hidpi.SetSensitive(on)
	}))
	virtual.Add(sizeRow)
	virtual.Add(hidpi)
	virtual.Add(g.hdrRow(prefs, change))
	page.Add(virtual)
	return page
}

// hdrRow is the switch for HDR video. It is off and dimmed, with the reason underneath, where
// this desktop cannot show HDR.
func (g *gui) hdrRow(prefs settings, change func(func(*settings))) *adw.SwitchRow {
	env := g.app.displayHDR()
	look := hdrRowFor(prefs.HDR, g.app.hdrState(), env)
	var row *adw.SwitchRow
	row = switchRow("HDR", look.Subtitle, look.Active, func(on bool) {
		change(func(s *settings) { s.HDR = on })
		now := hdrRowFor(on, g.app.hdrState(), env)
		row.SetSubtitle(now.Subtitle)
	})
	row.SetSensitive(look.Sensitive)
	return row
}

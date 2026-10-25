package main

import (
	"testing"
)

func TestArtForModel(t *testing.T) {
	cases := map[string]macArt{
		"MacBookPro18,2": artMacBook,
		"MacBookAir10,1": artMacBook,
		"MacBook10,1":    artMacBook,
		"iMac20,1":       artIMac,
		"iMacPro1,1":     artIMac,
		"Macmini9,1":     artMini,
		"MacPro7,1":      artPro,
		"Mac13,1":        artStudio,
		"Mac14,14":       artStudio,
		"Mac16,9":        artStudio,
		"Mac14,3":        artMini,
		"Mac16,11":       artMini,
		"Mac14,8":        artPro,
		"Mac14,6":        artMacBook,
		"Mac15,3":        artMacBook,
		"Mac15,4":        artIMac,
		"Mac16,3":        artIMac,
		"  Mac13,2 ":     artStudio,
		"":               artDisplay,
		"Mac99,1":        artDisplay,
		"VirtualMac2,1":  artDisplay,
		"Xserve3,1":      artDisplay,
	}
	for model, want := range cases {
		if got := artForModel(model); got != want {
			t.Errorf("artForModel(%q) = %s, want %s", model, got, want)
		}
	}
}

func TestModelFromText(t *testing.T) {
	cases := []struct {
		records []string
		want    string
	}{
		{[]string{"osxvers=24", "model=MacBookPro18,2"}, "MacBookPro18,2"},
		{[]string{"MODEL=iMac20,1"}, "iMac20,1"},
		{[]string{"model = Macmini9,1 "}, "Macmini9,1"},
		{[]string{"osxvers=24"}, ""},
		{nil, ""},
		{[]string{"model"}, ""},
	}
	for _, c := range cases {
		if got := modelFromText(c.records); got != c.want {
			t.Errorf("modelFromText(%q) = %q, want %q", c.records, got, c.want)
		}
	}
}

func TestEveryArtExists(t *testing.T) {
	for _, art := range allMacArt {
		data, err := macArtSVG(art)
		if err != nil || len(data) == 0 {
			t.Errorf("%s: %v", art, err)
		}
	}
	// Every table entry must map to art that exists.
	for model, art := range appleSiliconModels {
		if _, err := macArtSVG(art); err != nil {
			t.Errorf("%s: %v", model, err)
		}
	}
}

func TestIconNames(t *testing.T) {
	names := map[string]bool{}
	for _, name := range iconNames() {
		names[name] = true
	}
	for _, want := range []string{
		"sharelinux-fullscreen-symbolic", "sharelinux-exclusive-symbolic", "sharelinux-sound-symbolic",
		"sharelinux-sound-muted-symbolic", "sharelinux-clipboard-symbolic", "sharelinux-send-file-symbolic",
		"sharelinux-displays-symbolic", "sharelinux-stats-symbolic", "sharelinux-network-symbolic",
		"sharelinux-paired-symbolic", "sharelinux-disconnect-symbolic", "sharelinux-macs-symbolic",
		"sharelinux-recent-symbolic", "sharelinux-mouse-helper-symbolic", "sharelinux-preferences-symbolic",
	} {
		if !names[want] {
			t.Errorf("missing icon %s", want)
		}
	}
}

func TestArtForNames(t *testing.T) {
	cases := []struct {
		names []string
		want  macArt
	}{
		{[]string{"MacBook Pro m3 Max"}, artMacBook},
		{[]string{"MacBook-Pro-m3-Max.local."}, artMacBook},
		{[]string{"cicd's iMac Pro"}, artIMac},
		{[]string{"cicds-iMac-Pro.local"}, artIMac},
		{[]string{"Anna's MacBook Air"}, artMacBook},
		{[]string{"MACBOOKPRO"}, artMacBook},
		{[]string{"Office iMac"}, artIMac},
		{[]string{"Mac mini"}, artMini},
		{[]string{"Mac-mini.local"}, artMini},
		{[]string{"macmini9"}, artMini},
		{[]string{"build mini"}, artMini},
		{[]string{"Mac Studio"}, artStudio},
		{[]string{"macstudio"}, artStudio},
		{[]string{"Render studio"}, artStudio},
		{[]string{"Mac Pro"}, artPro},
		{[]string{"macpro"}, artPro},
		{[]string{"Edit-Mac-Pro.local"}, artPro},
		{[]string{"Pro", "Mac"}, artDisplay},
		{[]string{"administrator"}, artDisplay},
		{[]string{"Alice's Mac"}, artDisplay},
		{[]string{"192.168.1.20"}, artDisplay},
		{[]string{"", ""}, artDisplay},
		{nil, artDisplay},
		{[]string{"Alice's Mac", "MacBook-Air.local"}, artMacBook},
	}
	for _, c := range cases {
		if got := artForNames(c.names...); got != c.want {
			t.Errorf("artForNames(%q) = %s, want %s", c.names, got, c.want)
		}
	}
}

func TestResolveArtModelWins(t *testing.T) {
	if got := resolveArt("Macmini9,1", "MacBook Pro m3 Max"); got != artMini {
		t.Errorf("a model beats the name: got %s", got)
	}
	if got := resolveArt("Mac99,1", "cicd's iMac Pro"); got != artIMac {
		t.Errorf("an unknown model falls to the name: got %s", got)
	}
	if got := resolveArt("", "Alice's Mac"); got != artDisplay {
		t.Errorf("nothing known: got %s", got)
	}
}

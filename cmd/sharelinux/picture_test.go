package main

import (
	"image"
	"testing"
)

func TestPlanPicture(t *testing.T) {
	big := image.Rect(0, 0, 200, 100)
	small := image.Rect(0, 0, 80, 40)
	cases := []struct {
		name   string
		buffer image.Rectangle
		shown  image.Point
		dirty  image.Rectangle
		full   bool
		resize bool
		area   image.Rectangle
	}{
		{"same size, changed area", big, image.Pt(200, 100), image.Rect(10, 10, 50, 50), false, false, image.Rect(10, 10, 50, 50)},
		{"same size, full", big, image.Pt(200, 100), image.Rectangle{}, true, false, big},
		{"area outside the buffer is cut", big, image.Pt(200, 100), image.Rect(150, 50, 400, 400), false, false, image.Rect(150, 50, 200, 100)},
		{"server shrank: resize and send all, whatever was dirty", small, image.Pt(200, 100), image.Rect(150, 50, 200, 100), false, true, small},
		{"server grew", big, image.Pt(80, 40), image.Rectangle{}, false, true, big},
		{"empty buffer does nothing", image.Rectangle{}, image.Pt(200, 100), image.Rect(0, 0, 5, 5), true, false, image.Rectangle{}},
	}
	for _, c := range cases {
		resize, area := planPicture(c.buffer, c.shown, c.dirty, c.full)
		if resize != c.resize || area != c.area {
			t.Errorf("%s: resize %v area %v, want %v %v", c.name, resize, area, c.resize, c.area)
		}
	}
}

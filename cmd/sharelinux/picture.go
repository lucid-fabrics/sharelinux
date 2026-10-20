package main

import "image"

// planPicture says what to do with the widget's tiles for a framebuffer. The
// size comes from the framebuffer itself, read under its lock: the size the
// client reports can change between that call and the copy, and a smaller
// buffer then holds fewer pixels than the widget expects. A different size means
// resize the widget and send the whole picture; otherwise the changed area is
// cut to the buffer.
func planPicture(buffer image.Rectangle, shown image.Point, dirty image.Rectangle, full bool) (resize bool, area image.Rectangle) {
	if buffer.Empty() {
		return false, image.Rectangle{}
	}
	if buffer.Size() != shown {
		return true, buffer
	}
	if full {
		return false, buffer
	}
	return false, dirty.Intersect(buffer)
}

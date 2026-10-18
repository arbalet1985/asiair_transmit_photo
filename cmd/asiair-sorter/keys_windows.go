package main

import (
	"github.com/lxn/walk"
)

// Keys of the table «Кадры» for looking at the frames: ← → the previous and
// the next frame, the space starts and stops the blink, + − zoom, 0 fits
// the frame in, 1 shows it at 100%, Del or X marks the frame bad.

const (
	vkSpace    = 0x20
	vkLeft     = 0x25
	vkRight    = 0x27
	vkDelete   = 0x2E
	vkX        = 0x58
	vk0        = 0x30
	vk1        = 0x31
	vkNumpad0  = 0x60
	vkNumpad1  = 0x61
	vkAdd      = 0x6B
	vkSubtract = 0x6D
	vkOEMPlus  = 0xBB
	vkOEMMinus = 0xBD
)

// frameKey handles a key of the table; false leaves it to the table.
func (w *window) frameKey(vk uintptr, repeat bool) bool {
	if walk.ControlDown() || walk.AltDown() {
		return false
	}
	switch vk {
	case vkLeft:
		w.step(-1)
	case vkRight:
		w.step(1)
	case vkSpace:
		if !repeat {
			w.togglePlay()
		}
	case vkAdd, vkOEMPlus:
		w.zoom.zoomIn()
	case vkSubtract, vkOEMMinus:
		w.zoom.zoomOut()
	case vk0, vkNumpad0:
		w.zoom.fitAll()
	case vk1, vkNumpad1:
		w.zoom.actual()
	case vkDelete, vkX:
		if !repeat {
			w.toggleReject()
		}
	default:
		return false
	}
	return true
}

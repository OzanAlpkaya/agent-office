// Package icons draws agent-office's app icons (ADR 0040): a desk with a
// terminal, pixel art on a 16x16 grid, original to this repository like the
// office art (web/src/office/ART.md). The PNGs in web/public/icons are its
// output; TestCommittedIcons checks they match (go test ./web/icons -update
// rewrites them).
package icons

import (
	"image"
	"image/color"
)

// art is the 16x16 icon. Every character is a palette entry.
var art = [16]string{
	"................",
	"................",
	"...MMMMMMMMMM...",
	"...MSSSSSSSSM...",
	"...MSGSSSSSSM...",
	"...MSSGSSSSSM...",
	"...MSGSSGGGSM...",
	"...MSSSSSSSSM...",
	"...MMMMMMMMMM...",
	"......MMMM......",
	".DDDDDDDDDDDDDD.",
	".dddddddddddddd.",
	"..d..........d..",
	"..d..........d..",
	"..d..........d..",
	"................",
}

var palette = map[byte]color.NRGBA{
	'.': {0x0f, 0x11, 0x15, 0xff}, // the UI's background
	'M': {0x3a, 0x41, 0x52, 0xff}, // monitor frame
	'S': {0x1d, 0x2a, 0x44, 0xff}, // screen
	'G': {0x7b, 0xd8, 0x8f, 0xff}, // prompt, the UI's "idle" green
	'D': {0x8a, 0x5a, 0x3c, 0xff}, // desk top
	'd': {0x5e, 0x3d, 0x29, 0xff}, // desk front and legs
}

// File is one icon the manifest and index.html reference.
type File struct {
	Name     string
	Size     int
	Maskable bool
}

// Files are the icons in web/public/icons.
var Files = []File{
	{"icon-192.png", 192, false},
	{"icon-512.png", 512, false},
	{"icon-maskable-512.png", 512, true},
	{"apple-touch-icon.png", 180, false},
}

// Render draws the icon at size×size pixels with whole pixels per grid cell.
// A maskable icon keeps the art inside the central safe zone (a circle of 80%
// of the size), since launchers crop it to their own shape.
func Render(size int, maskable bool) *image.NRGBA {
	frac := 0.875
	if maskable {
		frac = 0.56
	}
	cell := int(float64(size) * frac / 16)
	if cell < 1 {
		cell = 1
	}
	off := (size - 16*cell) / 2
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	bg := palette['.']
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetNRGBA(x, y, bg)
		}
	}
	for gy, row := range art {
		for gx := 0; gx < 16; gx++ {
			c := palette[row[gx]]
			for y := 0; y < cell; y++ {
				for x := 0; x < cell; x++ {
					img.SetNRGBA(off+gx*cell+x, off+gy*cell+y, c)
				}
			}
		}
	}
	return img
}

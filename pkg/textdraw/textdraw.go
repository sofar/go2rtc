// Package textdraw provides TrueType text rendering onto Go images
// using the Go font family and golang.org/x/image/font/opentype.
package textdraw

import (
	"image"
	"image/color"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	regularFont *opentype.Font
	boldFont    *opentype.Font
	initOnce    sync.Once
)

func initFonts() {
	initOnce.Do(func() {
		var err error
		regularFont, err = opentype.Parse(goregular.TTF)
		if err != nil {
			panic("textdraw: parse regular font: " + err.Error())
		}
		boldFont, err = opentype.Parse(gobold.TTF)
		if err != nil {
			panic("textdraw: parse bold font: " + err.Error())
		}
	})
}

// face returns a font.Face for the given size in pixels and weight.
func face(size float64, bold bool) (font.Face, error) {
	initFonts()
	f := regularFont
	if bold {
		f = boldFont
	}
	return opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

// DrawString renders text at (x, y) on an RGBA image. The y coordinate
// is the baseline position.
func DrawString(img *image.RGBA, x, y int, text string, col color.Color, size float64) {
	DrawStringBold(img, x, y, text, col, size, false)
}

// DrawStringBold renders text with optional bold weight.
func DrawStringBold(img *image.RGBA, x, y int, text string, col color.Color, size float64, bold bool) {
	fc, err := face(size, bold)
	if err != nil {
		return
	}
	defer fc.Close()

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: fc,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)
}

// MeasureString returns the width in pixels of the rendered text.
func MeasureString(text string, size float64, bold bool) int {
	fc, err := face(size, bold)
	if err != nil {
		return len(text) * int(size)
	}
	defer fc.Close()

	d := &font.Drawer{Face: fc}
	return d.MeasureString(text).Ceil()
}

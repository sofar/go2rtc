package offline

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"

	"github.com/AlexxIT/go2rtc/pkg/textdraw"
)

const (
	placeholderWidth  = 640
	placeholderHeight = 360
)

// RenderPlaceholder generates a JPEG "Camera Offline" placeholder image
// using TrueType font rendering.
func RenderPlaceholder(cameraName, reason string) []byte {
	img := image.NewRGBA(image.Rect(0, 0, placeholderWidth, placeholderHeight))

	// Dark background
	bg := color.RGBA{R: 30, G: 30, B: 30, A: 255}
	for y := range placeholderHeight {
		for x := range placeholderWidth {
			img.Set(x, y, bg)
		}
	}

	white := color.RGBA{R: 220, G: 220, B: 220, A: 255}
	red := color.RGBA{R: 220, G: 60, B: 60, A: 255}

	lines := []struct {
		text string
		col  color.Color
		size float64
		bold bool
	}{
		{cameraName, white, 24, true},
		{"OFFLINE", red, 36, true},
	}
	if reason != "" {
		lines = append(lines, struct {
			text string
			col  color.Color
			size float64
			bold bool
		}{reason, white, 16, false})
	}

	// Vertically center the text block
	totalH := 0
	for _, l := range lines {
		totalH += int(l.size * 1.4)
	}
	y := (placeholderHeight - totalH) / 2

	for _, l := range lines {
		lineH := int(l.size * 1.4)
		w := textdraw.MeasureString(l.text, l.size, l.bold)
		x := (placeholderWidth - w) / 2
		if x < 10 {
			x = 10
		}
		textdraw.DrawStringBold(img, x, y+int(l.size), l.text, l.col, l.size, l.bold)
		y += lineH
	}

	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

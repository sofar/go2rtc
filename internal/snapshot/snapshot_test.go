package snapshot

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Fill with a gradient so cropping is visually verifiable
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}

func TestPolygonBounds(t *testing.T) {
	tests := []struct {
		name    string
		polygon [][2]int
		expect  image.Rectangle
	}{
		{
			"simple rect",
			[][2]int{{10, 20}, {100, 20}, {100, 80}, {10, 80}},
			image.Rect(10, 20, 100, 80),
		},
		{
			"triangle",
			[][2]int{{50, 10}, {90, 90}, {10, 90}},
			image.Rect(10, 10, 90, 90),
		},
		{
			"single point",
			[][2]int{{42, 42}},
			image.Rect(42, 42, 42, 42),
		},
		{
			"empty",
			nil,
			image.Rectangle{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, polygonBounds(tt.polygon))
		})
	}
}

func TestCropToRegion(t *testing.T) {
	src := makeTestJPEG(640, 480)
	region := &camera.Region{
		Polygon: [][2]int{{100, 100}, {300, 100}, {300, 300}, {100, 300}},
		Detect:  []string{"person"},
	}

	cropped, err := cropToRegion(src, region)
	require.NoError(t, err)
	require.NotEmpty(t, cropped)

	// Verify the cropped image dimensions match the bounding box
	img, err := jpeg.Decode(bytes.NewReader(cropped))
	require.NoError(t, err)
	bounds := img.Bounds()
	assert.Equal(t, 200, bounds.Dx()) // 300-100
	assert.Equal(t, 200, bounds.Dy()) // 300-100
}

func TestCropToRegion_InvalidJPEG(t *testing.T) {
	_, err := cropToRegion([]byte("not a jpeg"), &camera.Region{
		Polygon: [][2]int{{0, 0}, {10, 0}, {10, 10}},
	})
	require.Error(t, err)
}

func TestDrawRect(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	green := color.RGBA{R: 0, G: 255, B: 0, A: 255}

	drawRect(img, 10, 10, 50, 50, green, 1)

	// Check corners are drawn
	assert.Equal(t, green, img.At(10, 10))
	assert.Equal(t, green, img.At(50, 10))
	assert.Equal(t, green, img.At(10, 50))
	assert.Equal(t, green, img.At(50, 50))

	// Check middle of top edge
	assert.Equal(t, green, img.At(30, 10))

	// Check inside is NOT drawn
	r, g, b, a := img.At(30, 30).RGBA()
	assert.Equal(t, uint32(0), r+g+b+a) // transparent/black
}

func TestDrawRect_OutOfBounds(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 50, 50))
	green := color.RGBA{R: 0, G: 255, B: 0, A: 255}

	// Should not panic when rect extends beyond image
	drawRect(img, -10, -10, 60, 60, green, 2)
}

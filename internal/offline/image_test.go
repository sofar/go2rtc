package offline

import (
	"bytes"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderPlaceholder_ValidJPEG(t *testing.T) {
	data := RenderPlaceholder("front_porch", "connection timeout")
	require.NotEmpty(t, data)

	// JPEG magic bytes
	assert.Equal(t, byte(0xFF), data[0])
	assert.Equal(t, byte(0xD8), data[1])
	assert.Equal(t, byte(0xFF), data[2])

	// Decodable as JPEG
	img, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)

	bounds := img.Bounds()
	assert.Equal(t, placeholderWidth, bounds.Dx())
	assert.Equal(t, placeholderHeight, bounds.Dy())
}

func TestRenderPlaceholder_NoReason(t *testing.T) {
	data := RenderPlaceholder("cam1", "")
	require.NotEmpty(t, data)

	_, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)
}

func TestRenderPlaceholder_LongName(t *testing.T) {
	data := RenderPlaceholder("this_is_a_very_long_camera_name_that_might_overflow", "error text here")
	require.NotEmpty(t, data)

	_, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)
}

func TestRenderPlaceholder_DifferentInputsDifferentOutput(t *testing.T) {
	a := RenderPlaceholder("cam_a", "error 1")
	b := RenderPlaceholder("cam_b", "error 2")

	assert.NotEqual(t, a, b, "different inputs should produce different images")
}

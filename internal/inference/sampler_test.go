package inference

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPointInPolygon(t *testing.T) {
	// Triangle
	tri := [][2]int{{0, 0}, {100, 0}, {50, 100}}

	assert.True(t, pointInPolygon(50, 50, tri))   // center
	assert.True(t, pointInPolygon(30, 10, tri))    // inside
	assert.False(t, pointInPolygon(0, 100, tri))   // outside corner
	assert.False(t, pointInPolygon(150, 50, tri))  // far outside

	// L-shaped polygon
	lshape := [][2]int{{0, 0}, {100, 0}, {100, 50}, {50, 50}, {50, 100}, {0, 100}}

	assert.True(t, pointInPolygon(25, 25, lshape))  // top-left area
	assert.True(t, pointInPolygon(75, 25, lshape))  // top-right area
	assert.True(t, pointInPolygon(25, 75, lshape))  // bottom-left area
	assert.False(t, pointInPolygon(75, 75, lshape)) // bottom-right notch — outside
}

func TestRegionFiltering_FullFrame(t *testing.T) {
	// Detection on a 200x200 image. The polygon is a triangle in the
	// top-left corner: (0,0)-(100,0)-(0,100). Anything where x+y > 100
	// is outside the triangle.

	polygon := [][2]int{{0, 0}, {100, 0}, {0, 100}}

	// Detection with center at pixel (80, 80) in the full 200x200 image.
	// Normalized bbox: center at (0.4, 0.4) → pixel (80, 80).
	// 80+80 = 160 > 100 → outside the triangle.
	outsideBBox := [4]float32{0.3, 0.3, 0.5, 0.5}
	cx := float64(outsideBBox[0]+outsideBBox[2]) / 2 * 200
	cy := float64(outsideBBox[1]+outsideBBox[3]) / 2 * 200
	assert.False(t, pointInPolygon(cx, cy, polygon), "detection outside polygon should be rejected")

	// Detection with center at pixel (15, 15) in the full 200x200 image.
	// Normalized bbox: center at (0.075, 0.075) → pixel (15, 15).
	// 15+15 = 30 < 100 → inside the triangle.
	insideBBox := [4]float32{0.025, 0.025, 0.125, 0.125}
	cx = float64(insideBBox[0]+insideBBox[2]) / 2 * 200
	cy = float64(insideBBox[1]+insideBBox[3]) / 2 * 200
	assert.True(t, pointInPolygon(cx, cy, polygon), "detection inside polygon should be accepted")
}

func TestBackground_ContinuousSuppression(t *testing.T) {
	phantom := Detection{Class: "car", Confidence: 0.94, BBox: [4]float32{0.3, 0.4, 0.6, 0.7}}
	realCar := Detection{Class: "car", Confidence: 0.90, BBox: [4]float32{0.1, 0.1, 0.3, 0.3}}

	s := &Sampler{}

	// Phantom not yet stale
	for i := 0; i < bgStaleFrames-1; i++ {
		s.bgUpdate([]Detection{phantom})
	}
	assert.False(t, s.isBackground(phantom), "should not be background before threshold")

	// One more frame tips it over
	s.bgUpdate([]Detection{phantom})
	assert.True(t, s.isBackground(phantom), "should be background after threshold")
	assert.False(t, s.isBackground(realCar), "different location should not be background")

	// Slightly shifted should still match
	shifted := Detection{Class: "car", Confidence: 0.93, BBox: [4]float32{0.31, 0.41, 0.61, 0.71}}
	assert.True(t, s.isBackground(shifted), "slightly shifted phantom should match")

	// Different class should not match
	person := Detection{Class: "person", Confidence: 0.80, BBox: [4]float32{0.3, 0.4, 0.6, 0.7}}
	assert.False(t, s.isBackground(person), "different class should not match")
}

func TestBackground_ReleasesWhenGone(t *testing.T) {
	phantom := Detection{Class: "car", Confidence: 0.94, BBox: [4]float32{0.3, 0.4, 0.6, 0.7}}

	s := &Sampler{}

	// Make it stale
	for i := 0; i < bgStaleFrames; i++ {
		s.bgUpdate([]Detection{phantom})
	}
	assert.True(t, s.isBackground(phantom))

	// Disappears for bgAbsentFrames — should be released
	for i := 0; i < bgAbsentFrames; i++ {
		s.bgUpdate(nil)
	}
	assert.False(t, s.isBackground(phantom), "should be released after disappearing")
}

func TestBackground_IntermittentNotSuppressed(t *testing.T) {
	det := Detection{Class: "car", Confidence: 0.85, BBox: [4]float32{0.2, 0.2, 0.5, 0.5}}

	s := &Sampler{}

	// Appears for 10 frames, disappears for 10, repeat — never reaches threshold
	for cycle := 0; cycle < 5; cycle++ {
		for i := 0; i < 10; i++ {
			s.bgUpdate([]Detection{det})
		}
		for i := 0; i < 10; i++ {
			s.bgUpdate(nil)
		}
	}
	assert.False(t, s.isBackground(det), "intermittent detection should not be background")
}

type mockBackend struct {
	detections []Detection
	err        error
	calls      int
}

func (m *mockBackend) Detect(jpeg []byte) ([]Detection, error) {
	m.calls++
	return m.detections, m.err
}

func (m *mockBackend) Close() error { return nil }

var _ Backend = (*mockBackend)(nil)

package inference

import (
	"testing"

	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/stretchr/testify/assert"
)

func TestMatchesRegion_ClassAndPosition(t *testing.T) {
	// Region: 100x100 square at origin (pixel coords)
	region := &camera.Region{
		Polygon: [][2]int{{0, 0}, {100, 0}, {100, 100}, {0, 100}},
		Detect:  []string{"person", "car"},
	}
	imgW, imgH := 200, 200 // image is 200x200

	tests := []struct {
		name  string
		det   Detection
		match bool
	}{
		{"person inside", Detection{Class: "person", BBox: [4]float32{0.1, 0.1, 0.3, 0.3}}, true},
		{"car inside", Detection{Class: "car", BBox: [4]float32{0.0, 0.0, 0.4, 0.4}}, true},
		{"person outside", Detection{Class: "person", BBox: [4]float32{0.6, 0.6, 0.9, 0.9}}, false},
		{"car outside", Detection{Class: "car", BBox: [4]float32{0.7, 0.7, 0.9, 0.9}}, false},
		{"wrong class inside", Detection{Class: "dog", BBox: [4]float32{0.1, 0.1, 0.3, 0.3}}, false},
		{"person on edge", Detection{Class: "person", BBox: [4]float32{0.2, 0.2, 0.6, 0.6}}, true},  // center at 0.4,0.4 = 80,80 px — inside
		{"person just outside", Detection{Class: "person", BBox: [4]float32{0.5, 0.5, 0.8, 0.8}}, false}, // center at 0.65,0.65 = 130,130 px — outside
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.match, matchesRegion(tt.det, region, imgW, imgH))
		})
	}
}

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

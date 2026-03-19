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

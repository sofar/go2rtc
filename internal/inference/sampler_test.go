package inference

import (
	"testing"

	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/stretchr/testify/assert"
)

func TestMatchesRegion(t *testing.T) {
	region := &camera.Region{
		Polygon: [][2]int{{0, 0}, {100, 0}, {100, 100}, {0, 100}},
		Detect:  []string{"person", "car"},
	}

	tests := []struct {
		class string
		match bool
	}{
		{"person", true},
		{"car", true},
		{"dog", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.class, func(t *testing.T) {
			d := Detection{Class: tt.class, Confidence: 0.9}
			assert.Equal(t, tt.match, matchesRegion(d, region))
		})
	}
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

// Verify mockBackend satisfies the Backend interface
var _ Backend = (*mockBackend)(nil)

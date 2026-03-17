package inference

// Detection represents a single detected object.
type Detection struct {
	Class      string     `json:"class"`
	Confidence float32    `json:"confidence"`
	BBox       [4]float32 `json:"bbox"` // x1, y1, x2, y2 normalized [0..1]
}

// Backend is the interface for object detection implementations.
type Backend interface {
	// Detect runs detection on a JPEG image and returns results.
	Detect(jpeg []byte) ([]Detection, error)
	// Close releases any resources held by the backend.
	Close() error
}

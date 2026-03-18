package viewgroup

import "time"

// GroupConfig holds the configuration for a view group.
type GroupConfig struct {
	Cameras           []string `yaml:"cameras" json:"cameras"`
	CorrelationWindow string   `yaml:"correlation_window" json:"correlation_window"`
	RequireMultiAngle bool     `yaml:"require_multi_angle" json:"require_multi_angle"`
}

// Group is the runtime state for a view group.
type Group struct {
	Name              string        `json:"name"`
	Cameras           []string      `json:"cameras"`
	CorrelationWindow time.Duration `json:"-"`
	RequireMultiAngle bool          `json:"require_multi_angle"`

	// pending holds buffered detections waiting for correlation.
	pending []*pendingDetection
}

// pendingDetection is a detection event waiting to be correlated with
// detections from other cameras in the group.
type pendingDetection struct {
	camera    string
	class     string
	timestamp time.Time
	event     any // the original DetectionEvent
	confirmed bool
}

// GroupDetectionEvent is emitted when detections are correlated across
// cameras, or when the correlation window expires.
type GroupDetectionEvent struct {
	Group      string   `json:"group"`
	Class      string   `json:"class"`
	Cameras    []string `json:"cameras"`
	Correlated bool     `json:"correlated"` // true if 2+ cameras saw it
}

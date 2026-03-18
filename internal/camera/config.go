package camera

import (
	"errors"
	"fmt"
)

// Camera holds the full configuration for a single camera.
type Camera struct {
	Name   string `yaml:"-" json:"name"`
	Stream string `yaml:"stream" json:"stream"`

	Regions    map[string]*Region    `yaml:"regions,omitempty" json:"regions,omitempty"`
	ViewGroup  string                `yaml:"view_group,omitempty" json:"view_group,omitempty"`
	Recording  *RecordingConfig      `yaml:"recording,omitempty" json:"recording,omitempty"`
	Transcode  map[string]*Transcode `yaml:"transcode_profiles,omitempty" json:"transcode_profiles,omitempty"`
}

// Region defines a polygon area within the camera's field of view with
// a list of object classes to detect within it.
type Region struct {
	Polygon [][2]int `yaml:"polygon" json:"polygon"`
	Detect  []string `yaml:"detect" json:"detect"`
}

// RecordingConfig controls segmented recording for a camera.
// When Codec is set, the stream is transcoded via ffmpeg before
// recording. When Codec is empty, the raw stream is recorded as-is.
type RecordingConfig struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	Path            string `yaml:"path,omitempty" json:"path,omitempty"`
	SegmentDuration string `yaml:"segment_duration,omitempty" json:"segment_duration,omitempty"`
	Codec           string `yaml:"codec,omitempty" json:"codec,omitempty"`
	Bitrate         string `yaml:"bitrate,omitempty" json:"bitrate,omitempty"`
	Resolution      string `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	Hardware        string `yaml:"hardware,omitempty" json:"hardware,omitempty"` // vaapi, cuda, etc.
	Retention       string `yaml:"retention,omitempty" json:"retention,omitempty"`
}

// Transcode defines an output profile for a camera stream.
type Transcode struct {
	Codec      string `yaml:"codec" json:"codec"`
	Resolution string `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	Bitrate    string `yaml:"bitrate,omitempty" json:"bitrate,omitempty"`
}

// validate checks that a camera config is minimally valid.
func (c *Camera) validate() error {
	if c.Stream == "" {
		return errors.New("camera: stream is required")
	}

	for name, region := range c.Regions {
		if len(region.Polygon) < 3 {
			return fmt.Errorf("camera: region %q needs at least 3 polygon points", name)
		}
	}

	return nil
}

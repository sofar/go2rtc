package camera

import (
	"encoding/json"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_Full(t *testing.T) {
	input := `
cameras:
  front_porch:
    stream: rtsp://192.168.1.10:554/stream1
    regions:
      driveway:
        polygon: [[0,200],[400,200],[400,480],[0,480]]
        detect: [person, car, deer]
      doorstep:
        polygon: [[400,300],[640,300],[640,480],[400,480]]
        detect: [person]
    view_group: front_yard
    recording:
      enabled: true
      codec: h265
      bitrate: 2M
      retention: 30d
    transcode_profiles:
      low:
        codec: h264
        resolution: 640x360
        bitrate: 500k
      high:
        codec: h264
        resolution: 1920x1080
        bitrate: 4M
`
	var cfg struct {
		Mod map[string]*Camera `yaml:"cameras"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(input), &cfg))
	require.Contains(t, cfg.Mod, "front_porch")

	cam := cfg.Mod["front_porch"]
	assert.Equal(t, "rtsp://192.168.1.10:554/stream1", cam.Stream)
	assert.Equal(t, "front_yard", cam.ViewGroup)

	// Regions
	require.Len(t, cam.Regions, 2)
	driveway := cam.Regions["driveway"]
	require.NotNil(t, driveway)
	assert.Len(t, driveway.Polygon, 4)
	assert.Equal(t, [2]int{0, 200}, driveway.Polygon[0])
	assert.Equal(t, []string{"person", "car", "deer"}, driveway.Detect)

	doorstep := cam.Regions["doorstep"]
	require.NotNil(t, doorstep)
	assert.Len(t, doorstep.Polygon, 4)
	assert.Equal(t, []string{"person"}, doorstep.Detect)

	// Recording
	require.NotNil(t, cam.Recording)
	assert.True(t, cam.Recording.Enabled)
	assert.Equal(t, "h265", cam.Recording.Codec)
	assert.Equal(t, "2M", cam.Recording.Bitrate)
	assert.Equal(t, "30d", cam.Recording.Retention)

	// Transcode profiles
	require.Len(t, cam.Transcode, 2)
	assert.Equal(t, "h264", cam.Transcode["low"].Codec)
	assert.Equal(t, "640x360", cam.Transcode["low"].Resolution)
	assert.Equal(t, "h264", cam.Transcode["high"].Codec)
	assert.Equal(t, "1920x1080", cam.Transcode["high"].Resolution)
}

func TestParseConfig_StringShorthand(t *testing.T) {
	input := `
cameras:
  simple: rtsp://192.168.1.10:554/stream1
`
	var cfg struct {
		Mod map[string]*Camera `yaml:"cameras"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(input), &cfg))
	require.Contains(t, cfg.Mod, "simple")
	assert.Equal(t, "rtsp://192.168.1.10:554/stream1", cfg.Mod["simple"].Stream)
}

func TestParseConfig_Minimal(t *testing.T) {
	input := `
cameras:
  cam1:
    stream: rtsp://example.com/live
`
	var cfg struct {
		Mod map[string]*Camera `yaml:"cameras"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(input), &cfg))
	cam := cfg.Mod["cam1"]
	assert.Equal(t, "rtsp://example.com/live", cam.Stream)
	assert.Nil(t, cam.Recording)
	assert.Nil(t, cam.Regions)
	assert.Nil(t, cam.Transcode)
}

func TestParseConfig_Empty(t *testing.T) {
	input := `{}`
	var cfg struct {
		Mod map[string]*Camera `yaml:"cameras"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(input), &cfg))
	assert.Nil(t, cfg.Mod)
}

func TestValidate_MissingStream(t *testing.T) {
	cam := &Camera{Name: "test"}
	err := cam.validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream is required")
}

func TestValidate_BadPolygon(t *testing.T) {
	cam := &Camera{
		Name:   "test",
		Stream: "rtsp://example.com/live",
		Regions: map[string]*Region{
			"bad": {
				Polygon: [][2]int{{0, 0}, {1, 1}}, // only 2 points
				Detect:  []string{"person"},
			},
		},
	}
	err := cam.validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 3 polygon points")
}

func TestValidate_OK(t *testing.T) {
	cam := &Camera{
		Name:   "test",
		Stream: "rtsp://example.com/live",
		Regions: map[string]*Region{
			"yard": {
				Polygon: [][2]int{{0, 0}, {100, 0}, {100, 100}},
				Detect:  []string{"person"},
			},
		},
	}
	assert.NoError(t, cam.validate())
}

func TestValidate_NoRegionsOK(t *testing.T) {
	cam := &Camera{
		Name:   "test",
		Stream: "rtsp://example.com/live",
	}
	assert.NoError(t, cam.validate())
}

func TestJSON_RoundTrip(t *testing.T) {
	cam := &Camera{
		Name:      "front",
		Stream:    "rtsp://example.com/live",
		ViewGroup: "yard",
		Regions: map[string]*Region{
			"driveway": {
				Polygon: [][2]int{{0, 0}, {100, 0}, {100, 100}, {0, 100}},
				Detect:  []string{"person", "car"},
			},
		},
	}

	b, err := json.Marshal(cam)
	require.NoError(t, err)

	var decoded Camera
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, cam.Name, decoded.Name)
	assert.Equal(t, cam.Stream, decoded.Stream)
	assert.Equal(t, cam.ViewGroup, decoded.ViewGroup)
	assert.Len(t, decoded.Regions, 1)
	assert.Equal(t, cam.Regions["driveway"].Detect, decoded.Regions["driveway"].Detect)
}

func TestAll_Sorted(t *testing.T) {
	// Reset global state
	old := cameras
	cameras = map[string]*Camera{
		"z_cam": {Name: "z_cam", Stream: "rtsp://z"},
		"a_cam": {Name: "a_cam", Stream: "rtsp://a"},
		"m_cam": {Name: "m_cam", Stream: "rtsp://m"},
	}
	defer func() { cameras = old }()

	all := All()
	require.Len(t, all, 3)
	assert.Equal(t, "a_cam", all[0].Name)
	assert.Equal(t, "m_cam", all[1].Name)
	assert.Equal(t, "z_cam", all[2].Name)
}

func TestGet(t *testing.T) {
	old := cameras
	cameras = map[string]*Camera{
		"test": {Name: "test", Stream: "rtsp://example.com"},
	}
	defer func() { cameras = old }()

	assert.NotNil(t, Get("test"))
	assert.Nil(t, Get("missing"))
}

func TestBuildTranscodeSource(t *testing.T) {
	tests := []struct {
		name    string
		camera  string
		profile *Transcode
		expect  string
	}{
		{
			"h264 with resolution",
			"front",
			&Transcode{Codec: "h264", Resolution: "640x360"},
			"ffmpeg:front#video=h264#width=640#height=360",
		},
		{
			"h265 no resolution",
			"cam1",
			&Transcode{Codec: "h265"},
			"ffmpeg:cam1#video=h265",
		},
		{
			"colon separator",
			"cam1",
			&Transcode{Codec: "h264", Resolution: "1920:1080"},
			"ffmpeg:cam1#video=h264#width=1920#height=1080",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, buildTranscodeSource(tt.camera, tt.profile))
		})
	}
}

func TestSplitResolution(t *testing.T) {
	tests := []struct {
		input  string
		expect []string
	}{
		{"640x360", []string{"640", "360"}},
		{"1920X1080", []string{"1920", "1080"}},
		{"1280:720", []string{"1280", "720"}},
		{"invalid", nil},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expect, splitResolution(tt.input))
		})
	}
}

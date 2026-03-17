package offline

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleStateChange_GoesOffline(t *testing.T) {
	old := states
	states = map[string]*state{
		"cam1": {Online: true, Since: time.Now()},
	}
	defer func() { states = old }()

	handleStateChange("cam1", streams.ProducerStateChange{
		URL:   "rtsp://example.com",
		Retry: 0,
		Err:   errors.New("connection refused"),
	})

	assert.False(t, IsOnline("cam1"))

	s := states["cam1"]
	assert.Equal(t, "connection refused", s.LastError)
}

func TestHandleStateChange_ComesBackOnline(t *testing.T) {
	old := states
	states = map[string]*state{
		"cam1": {Online: false, Since: time.Now(), LastError: "timeout"},
	}
	defer func() { states = old }()

	handleStateChange("cam1", streams.ProducerStateChange{
		URL:   "rtsp://example.com",
		Retry: -1, // success
	})

	assert.True(t, IsOnline("cam1"))

	s := states["cam1"]
	assert.Empty(t, s.LastError)
}

func TestHandleStateChange_UnknownCamera(t *testing.T) {
	old := states
	states = map[string]*state{}
	defer func() { states = old }()

	handleStateChange("new_cam", streams.ProducerStateChange{
		URL:   "rtsp://example.com",
		Retry: 0,
		Err:   errors.New("timeout"),
	})

	assert.False(t, IsOnline("new_cam"))
}

func TestIsOnline_UnknownAssumedOnline(t *testing.T) {
	old := states
	states = map[string]*state{}
	defer func() { states = old }()

	assert.True(t, IsOnline("nonexistent"))
}

func TestGetPlaceholder_OnlineReturnsNil(t *testing.T) {
	old := states
	states = map[string]*state{
		"cam1": {Online: true},
	}
	defer func() { states = old }()

	assert.Nil(t, GetPlaceholder("cam1"))
}

func TestGetPlaceholder_OfflineReturnsJPEG(t *testing.T) {
	old := states
	states = map[string]*state{
		"cam1": {Online: false, LastError: "timeout"},
	}
	defer func() { states = old }()

	data := GetPlaceholder("cam1")
	require.NotNil(t, data)
	assert.True(t, len(data) > 100)
	// JPEG magic
	assert.Equal(t, byte(0xFF), data[0])
	assert.Equal(t, byte(0xD8), data[1])
}

func TestGetPlaceholder_CachesResult(t *testing.T) {
	old := states
	states = map[string]*state{
		"cam1": {Online: false, LastError: "timeout"},
	}
	defer func() { states = old }()

	a := GetPlaceholder("cam1")
	b := GetPlaceholder("cam1")

	// Same slice (cached, not regenerated)
	assert.Equal(t, &a[0], &b[0])
}

func TestGetPlaceholder_InvalidatedOnStateChange(t *testing.T) {
	old := states
	states = map[string]*state{
		"cam1": {Online: false, LastError: "error1"},
	}
	defer func() { states = old }()

	first := GetPlaceholder("cam1")
	require.NotNil(t, first)

	// Camera goes offline again with different error
	handleStateChange("cam1", streams.ProducerStateChange{
		URL: "rtsp://example.com", Retry: 1, Err: errors.New("error2"),
	})

	second := GetPlaceholder("cam1")
	require.NotNil(t, second)

	// Should be regenerated (different error text)
	assert.NotEqual(t, first, second)
}

package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApiEvents_Empty(t *testing.T) {
	old := recentEvents
	recentEvents = nil
	defer func() { recentEvents = old }()

	r := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	w := httptest.NewRecorder()

	apiEvents(w, r)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "[]", trimJSON(w.Body.String()))
}

func TestApiEvents_FilterByCamera(t *testing.T) {
	old := recentEvents
	recentEvents = []*Event{
		{Type: TypeDetection, Camera: "cam1", Timestamp: time.Now()},
		{Type: TypeDetection, Camera: "cam2", Timestamp: time.Now()},
	}
	defer func() { recentEvents = old }()

	r := httptest.NewRequest(http.MethodGet, "/api/events?camera=cam1", nil)
	w := httptest.NewRecorder()

	apiEvents(w, r)

	var result []*Event
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Len(t, result, 1)
	assert.Equal(t, "cam1", result[0].Camera)
}

func TestApiEvents_FilterByType(t *testing.T) {
	old := recentEvents
	recentEvents = []*Event{
		{Type: TypeDetection, Camera: "cam1", Timestamp: time.Now()},
		{Type: TypeStateChange, Camera: "cam1", Timestamp: time.Now()},
	}
	defer func() { recentEvents = old }()

	r := httptest.NewRequest(http.MethodGet, "/api/events?type=detection", nil)
	w := httptest.NewRecorder()

	apiEvents(w, r)

	var result []*Event
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Len(t, result, 1)
	assert.Equal(t, TypeDetection, result[0].Type)
}

func TestApiEvents_FilterByTime(t *testing.T) {
	old := recentEvents
	t1 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2024, 1, 1, 14, 0, 0, 0, time.UTC)
	recentEvents = []*Event{
		{Type: TypeDetection, Camera: "cam1", Timestamp: t1},
		{Type: TypeDetection, Camera: "cam1", Timestamp: t2},
		{Type: TypeDetection, Camera: "cam1", Timestamp: t3},
	}
	defer func() { recentEvents = old }()

	from := "2024-01-01T11:00:00Z"
	to := "2024-01-01T13:00:00Z"
	r := httptest.NewRequest(http.MethodGet, "/api/events?from="+from+"&to="+to, nil)
	w := httptest.NewRecorder()

	apiEvents(w, r)

	var result []*Event
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Len(t, result, 1)
	assert.Equal(t, t2, result[0].Timestamp)
}

func trimJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

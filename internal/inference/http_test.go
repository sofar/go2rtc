package inference

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPBackend_Detect(t *testing.T) {
	detections := []Detection{
		{Class: "person", Confidence: 0.95, BBox: [4]float32{0.1, 0.2, 0.5, 0.8}},
		{Class: "car", Confidence: 0.87, BBox: [4]float32{0.6, 0.3, 0.9, 0.7}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "image/jpeg", r.Header.Get("Content-Type"))

		body, _ := io.ReadAll(r.Body)
		assert.Equal(t, []byte("fake jpeg"), body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(detections)
	}))
	defer server.Close()

	backend := NewHTTPBackend(server.URL, 5*time.Second)
	result, err := backend.Detect([]byte("fake jpeg"))
	require.NoError(t, err)
	require.Len(t, result, 2)

	assert.Equal(t, "person", result[0].Class)
	assert.InDelta(t, 0.95, float64(result[0].Confidence), 0.01)
	assert.Equal(t, "car", result[1].Class)
}

func TestHTTPBackend_EmptyDetections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer server.Close()

	backend := NewHTTPBackend(server.URL, 5*time.Second)
	result, err := backend.Detect([]byte("jpeg"))
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestHTTPBackend_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusInternalServerError)
	}))
	defer server.Close()

	backend := NewHTTPBackend(server.URL, 5*time.Second)
	_, err := backend.Detect([]byte("jpeg"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}

func TestHTTPBackend_BadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	backend := NewHTTPBackend(server.URL, 5*time.Second)
	_, err := backend.Detect([]byte("jpeg"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

func TestHTTPBackend_ConnectionError(t *testing.T) {
	backend := NewHTTPBackend("http://127.0.0.1:1", 100*time.Millisecond)
	_, err := backend.Detect([]byte("jpeg"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request failed")
}

func TestHTTPBackend_Close(t *testing.T) {
	backend := NewHTTPBackend("http://example.com", 5*time.Second)
	assert.NoError(t, backend.Close())
}

package inference

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPBackend sends JPEG frames to an external HTTP endpoint and parses
// JSON detection results. This is the simplest backend — works with any
// language/framework (Python+YOLO, Frigate, CodeProject.AI, etc.).
//
// Request:  POST <url> with Content-Type: image/jpeg body
// Response: JSON array of Detection objects
type HTTPBackend struct {
	URL     string
	Client  *http.Client
	Timeout time.Duration
}

// NewHTTPBackend creates a backend that POSTs frames to the given URL.
func NewHTTPBackend(url string, timeout time.Duration) *HTTPBackend {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPBackend{
		URL:     url,
		Timeout: timeout,
		Client:  &http.Client{Timeout: timeout},
	}
}

func (b *HTTPBackend) Detect(jpeg []byte) ([]Detection, error) {
	resp, err := b.Client.Post(b.URL, "image/jpeg", bytes.NewReader(jpeg))
	if err != nil {
		return nil, fmt.Errorf("inference/http: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("inference/http: status %d: %s", resp.StatusCode, body)
	}

	var detections []Detection
	if err := json.NewDecoder(resp.Body).Decode(&detections); err != nil {
		return nil, fmt.Errorf("inference/http: decode response: %w", err)
	}

	return detections, nil
}

func (b *HTTPBackend) Close() error {
	b.Client.CloseIdleConnections()
	return nil
}

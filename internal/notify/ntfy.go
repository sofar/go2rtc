package notify

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/events"
)

// NtfyConfig for the ntfy.sh notification backend.
type NtfyConfig struct {
	URL      string `yaml:"url"`      // e.g. https://ntfy.example.com/cameras
	Token    string `yaml:"token"`    // access token (optional)
	Priority string `yaml:"priority"` // min, low, default, high, urgent
	Image    string `yaml:"image"`    // none, link, attach (default: none)
	BaseURL  string `yaml:"base_url"` // go2rtc external URL, required for image: link
}

// NtfyBackend sends notifications via ntfy (https://ntfy.sh).
type NtfyBackend struct {
	cfg    *NtfyConfig
	client *http.Client
}

func NewNtfyBackend(cfg *NtfyConfig) *NtfyBackend {
	return &NtfyBackend{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (b *NtfyBackend) Send(n Notification) error {
	switch b.cfg.Image {
	case "attach":
		jpeg := events.CaptureSnapshot(n.Camera, n.Region)
		if len(jpeg) > 0 {
			return b.sendWithImage(n, jpeg)
		}
	case "link":
		if b.cfg.BaseURL != "" {
			return b.sendWithLink(n)
		}
	}

	return b.sendText(n)
}

func (b *NtfyBackend) sendText(n Notification) error {
	req, err := http.NewRequest("POST", b.cfg.URL, strings.NewReader(n.Message))
	if err != nil {
		return err
	}
	b.setHeaders(req, n)
	return b.doRequest(req)
}

func (b *NtfyBackend) sendWithImage(n Notification, jpeg []byte) error {
	req, err := http.NewRequest("PUT", b.cfg.URL, bytes.NewReader(jpeg))
	if err != nil {
		return err
	}
	b.setHeaders(req, n)
	// HTTP headers cannot contain newlines — collapse to comma-separated
	req.Header.Set("Message", strings.ReplaceAll(n.Message, "\n", ", "))
	req.Header.Set("Filename", n.Camera+".jpg")
	return b.doRequest(req)
}

func (b *NtfyBackend) sendWithLink(n Notification) error {
	req, err := http.NewRequest("POST", b.cfg.URL, strings.NewReader(n.Message))
	if err != nil {
		return err
	}
	b.setHeaders(req, n)
	url := strings.TrimRight(b.cfg.BaseURL, "/") + "/api/snapshot/" + n.Camera
	req.Header.Set("Attach", url)
	return b.doRequest(req)
}

func (b *NtfyBackend) setHeaders(req *http.Request, n Notification) {
	req.Header.Set("Title", n.Title)

	priority := n.Priority
	if b.cfg.Priority != "" {
		priority = b.cfg.Priority
	}
	if priority != "" {
		req.Header.Set("Priority", priority)
	}

	if len(n.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(n.Tags, ","))
	}

	if b.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.Token)
	}
}

func (b *NtfyBackend) doRequest(req *http.Request) error {
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		return fmt.Errorf("ntfy: status %d: %s", resp.StatusCode, string(body[:n]))
	}
	return nil
}

func (b *NtfyBackend) Close() error {
	b.client.CloseIdleConnections()
	return nil
}

package notify

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// NtfyConfig for the ntfy.sh notification backend.
type NtfyConfig struct {
	URL      string `yaml:"url"`      // e.g. https://ntfy.example.com/cameras
	Token    string `yaml:"token"`    // access token (optional)
	Priority string `yaml:"priority"` // min, low, default, high, urgent
}

// NtfyBackend sends notifications via ntfy (https://ntfy.sh).
type NtfyBackend struct {
	cfg    *NtfyConfig
	client *http.Client
}

func NewNtfyBackend(cfg *NtfyConfig) *NtfyBackend {
	return &NtfyBackend{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (b *NtfyBackend) Send(n Notification) error {
	body := strings.NewReader(n.Message)
	req, err := http.NewRequest("POST", b.cfg.URL, body)
	if err != nil {
		return err
	}

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

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("ntfy: status %d", resp.StatusCode)
	}

	return nil
}

func (b *NtfyBackend) Close() error {
	b.client.CloseIdleConnections()
	return nil
}

package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WebhookConfig for generic HTTP POST notifications.
type WebhookConfig struct {
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

// WebhookBackend sends notifications as JSON POST requests.
type WebhookBackend struct {
	cfg    *WebhookConfig
	client *http.Client
}

func NewWebhookBackend(cfg *WebhookConfig) *WebhookBackend {
	return &WebhookBackend{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (b *WebhookBackend) Send(n Notification) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", b.cfg.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range b.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook: status %d", resp.StatusCode)
	}

	return nil
}

func (b *WebhookBackend) Close() error {
	b.client.CloseIdleConnections()
	return nil
}

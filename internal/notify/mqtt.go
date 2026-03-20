package notify

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"

	"github.com/AlexxIT/go2rtc/pkg/mqtt"
)

// MQTTConfig for MQTT notification backend.
type MQTTConfig struct {
	Broker      string `yaml:"broker"`       // tcp://localhost:1883
	TopicPrefix string `yaml:"topic_prefix"` // default: go2rtc/
}

// MQTTBackend publishes notifications to an MQTT broker.
type MQTTBackend struct {
	cfg    *MQTTConfig
	client *mqtt.Client
}

func NewMQTTBackend(cfg *MQTTConfig) (*MQTTBackend, error) {
	if cfg.TopicPrefix == "" {
		cfg.TopicPrefix = "go2rtc/"
	}

	u, err := url.Parse(cfg.Broker)
	if err != nil {
		return nil, fmt.Errorf("mqtt: invalid broker URL: %w", err)
	}

	host := u.Host
	if u.Port() == "" {
		host = host + ":1883"
	}

	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, fmt.Errorf("mqtt: connect to %s: %w", host, err)
	}

	client := mqtt.NewClient(conn)

	return &MQTTBackend{
		cfg:    cfg,
		client: client,
	}, nil
}

func (b *MQTTBackend) Send(n Notification) error {
	topic := fmt.Sprintf("%s%s/%s/%s", b.cfg.TopicPrefix, n.Camera, n.Region, n.Event)

	data, err := json.Marshal(n)
	if err != nil {
		return err
	}

	return b.client.Publish(topic, data)
}

func (b *MQTTBackend) Close() error {
	return nil
}

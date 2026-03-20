// Package notify sends notifications on detection session events
// (start/end) to configured backends: ntfy, webhooks, MQTT, etc.
//
// Configuration:
//
//	notify:
//	  ntfy:
//	    url: https://ntfy.example.com/cameras
//	    priority: default
//	  webhooks:
//	    - url: http://localhost:8081/hook
//	  mqtt:
//	    broker: tcp://localhost:1883
//	    topic_prefix: go2rtc/
package notify

import (
	"fmt"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Config for the notify module.
type Config struct {
	Ntfy     *NtfyConfig     `yaml:"ntfy"`
	Webhooks []*WebhookConfig `yaml:"webhooks"`
	MQTT     *MQTTConfig     `yaml:"mqtt"`
}

// Backend sends a notification.
type Backend interface {
	Send(n Notification) error
	Close() error
}

// Notification is the payload sent to backends.
type Notification struct {
	Title    string            `json:"title"`
	Message  string            `json:"message"`
	Priority string            `json:"priority,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
	Camera   string            `json:"camera"`
	Region   string            `json:"region"`
	Class    string            `json:"class"`
	Event    string            `json:"event"` // session_start, session_end
	Session  *events.Session   `json:"session,omitempty"`
}

var backends []Backend

func Init() {
	log = app.GetLogger("notify")

	var cfg struct {
		Mod Config `yaml:"notify"`
	}
	app.LoadConfig(&cfg)

	if cfg.Mod.Ntfy != nil && cfg.Mod.Ntfy.URL != "" {
		backends = append(backends, NewNtfyBackend(cfg.Mod.Ntfy))
		log.Info().Str("url", cfg.Mod.Ntfy.URL).Msg("[notify] ntfy enabled")
	}

	for _, wh := range cfg.Mod.Webhooks {
		if wh.URL != "" {
			backends = append(backends, NewWebhookBackend(wh))
			log.Info().Str("url", wh.URL).Msg("[notify] webhook enabled")
		}
	}

	if cfg.Mod.MQTT != nil && cfg.Mod.MQTT.Broker != "" {
		b, err := NewMQTTBackend(cfg.Mod.MQTT)
		if err != nil {
			log.Error().Err(err).Msg("[notify] mqtt")
		} else {
			backends = append(backends, b)
			log.Info().Str("broker", cfg.Mod.MQTT.Broker).Msg("[notify] mqtt enabled")
		}
	}

	if len(backends) == 0 {
		return
	}

	// Subscribe to session events
	ch := events.Default.Subscribe(events.Filter{}, 128)
	go func() {
		for e := range ch {
			switch e.Type {
			case events.TypeSessionStart:
				if sess, ok := e.Data.(*events.Session); ok {
					dispatch(sessionNotification(sess, "start"))
				}
			case events.TypeSessionEnd:
				if sess, ok := e.Data.(*events.Session); ok {
					dispatch(sessionNotification(sess, "end"))
				}
			}
		}
	}()
}

func sessionNotification(sess *events.Session, event string) Notification {
	cameras := sess.Camera
	if len(sess.Cameras) > 1 {
		cameras = fmt.Sprintf("%v", sess.Cameras)
	}

	var title, message, priority string
	var tags []string

	switch event {
	case "start":
		title = fmt.Sprintf("%s detected in %s", sess.Class, sess.Region)
		message = fmt.Sprintf("Camera: %s\nConfidence: %.0f%%", cameras, sess.PeakConfidence*100)
		priority = "default"
		tags = []string{classEmoji(sess.Class), sess.Class}
	case "end":
		dur := formatDuration(sess.Duration)
		title = fmt.Sprintf("%s left %s", sess.Class, sess.Region)
		message = fmt.Sprintf("Duration: %s\nCamera: %s\nDetections: %d\nPeak: %.0f%%",
			dur, cameras, sess.DetectionCount, sess.PeakConfidence*100)
		priority = "low"
		tags = []string{sess.Class}
	}

	return Notification{
		Title:    title,
		Message:  message,
		Priority: priority,
		Tags:     tags,
		Camera:   sess.Camera,
		Region:   sess.Region,
		Class:    sess.Class,
		Event:    "session_" + event,
		Session:  sess,
	}
}

func dispatch(n Notification) {
	for _, b := range backends {
		if err := b.Send(n); err != nil {
			log.Error().Err(err).Str("type", fmt.Sprintf("%T", b)).Msg("[notify] send")
		}
	}
}

func classEmoji(class string) string {
	switch class {
	case "person":
		return "walking"
	case "car":
		return "car"
	case "dog":
		return "dog"
	case "cat":
		return "cat"
	case "bird":
		return "bird"
	case "truck":
		return "truck"
	default:
		return "bell"
	}
}

func formatDuration(sec float64) string {
	d := time.Duration(sec * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

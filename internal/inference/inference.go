// Package inference provides an object detection pipeline for camera
// streams. It periodically samples JPEG frames, sends them to a
// configurable detection backend, filters results by confidence and
// region, and publishes DetectionEvents to the event bus.
//
// Currently supports an HTTP backend (POST JPEG, receive JSON
// detections) which works with any detection service (Python+YOLO,
// Frigate, CodeProject.AI, etc.).
package inference

import (
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Config holds the inference module configuration.
type Config struct {
	Backend             string  `yaml:"backend"`
	SampleFPS           float64 `yaml:"sample_fps"`
	ConfidenceThreshold float32 `yaml:"confidence_threshold"`

	// HTTP backend config
	URL     string `yaml:"url"`
	Timeout string `yaml:"timeout"`
}

var samplers []*Sampler

func Init() {
	log = app.GetLogger("inference")

	var cfg struct {
		Mod Config `yaml:"inference"`
	}
	app.LoadConfig(&cfg)

	if cfg.Mod.Backend == "" && cfg.Mod.URL == "" {
		return // inference not configured
	}

	backend, err := createBackend(cfg.Mod)
	if err != nil {
		log.Error().Err(err).Msg("[inference] create backend")
		return
	}

	interval := time.Second / 3
	if cfg.Mod.SampleFPS > 0 {
		interval = time.Duration(float64(time.Second) / cfg.Mod.SampleFPS)
	}

	minConf := cfg.Mod.ConfidenceThreshold
	if minConf <= 0 {
		minConf = 0.5
	}

	bus := events.Default

	for _, cam := range camera.All() {
		sampler := NewSampler(cam, backend, bus, interval, minConf)
		sampler.Start()
		samplers = append(samplers, sampler)
		log.Info().Str("camera", cam.Name).Msg("[inference] started")
	}
}

func createBackend(cfg Config) (Backend, error) {
	switch cfg.Backend {
	case "http", "":
		if cfg.URL == "" {
			return nil, nil
		}
		timeout, _ := time.ParseDuration(cfg.Timeout)
		return NewHTTPBackend(cfg.URL, timeout), nil
	default:
		log.Warn().Str("backend", cfg.Backend).Msg("[inference] unknown backend, using http")
		timeout, _ := time.ParseDuration(cfg.Timeout)
		return NewHTTPBackend(cfg.URL, timeout), nil
	}
}

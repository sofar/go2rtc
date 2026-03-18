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
	"fmt"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/AlexxIT/go2rtc/internal/inference/onnxbe"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

// Config holds the inference module configuration.
type Config struct {
	Backend             string             `yaml:"backend"`
	SampleFPS           float64            `yaml:"sample_fps"`
	ConfidenceThreshold float32            `yaml:"confidence_threshold"`
	ClassThreshold      map[string]float32 `yaml:"class_threshold"`
	MinFrames           int                `yaml:"min_frames"` // detections before session starts

	// HTTP backend config
	URL     string `yaml:"url"`
	Timeout string `yaml:"timeout"`

	// ONNX backend config
	Model        string  `yaml:"model"`
	Device       string  `yaml:"device"`         // "cpu" or "openvino"
	InputSize    int     `yaml:"input_size"`      // model input size, default 640
	NMSThreshold float32 `yaml:"nms_threshold"`   // default 0.45
}

var samplers []*Sampler

func Init() {
	log = app.GetLogger("inference")

	// Register the detection parser so the session tracker can
	// extract region/class info from detection events.
	events.RegisterDetectionParser(func(data any) []events.DetectionDetail {
		de, ok := data.(DetectionEvent)
		if !ok {
			return nil
		}
		details := make([]events.DetectionDetail, len(de.Detections))
		for i, d := range de.Detections {
			details[i] = events.DetectionDetail{
				Region:     de.Region,
				Class:      d.Class,
				Confidence: d.Confidence,
			}
		}
		return details
	})

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

	// Configure session tracker min_frames
	if cfg.Mod.MinFrames > 0 {
		events.SetMinFrames(cfg.Mod.MinFrames)
	}

	bus := events.Default

	for _, cam := range camera.All() {
		sampler := NewSampler(cam, backend, bus, interval, minConf, cfg.Mod.ClassThreshold)
		sampler.Start()
		samplers = append(samplers, sampler)
		log.Info().Str("camera", cam.Name).Msg("[inference] started")
	}
}

func createBackend(cfg Config) (Backend, error) {
	switch cfg.Backend {
	case "onnx":
		if cfg.Model == "" {
			return nil, fmt.Errorf("inference: onnx backend requires model path")
		}
		return newOnnxWrapper(cfg.Model, cfg.Device, cfg.NMSThreshold, cfg.InputSize)
	case "http":
		if cfg.URL == "" {
			return nil, nil
		}
		timeout, _ := time.ParseDuration(cfg.Timeout)
		return NewHTTPBackend(cfg.URL, timeout), nil
	case "":
		// Auto-detect: if model is set, use onnx; if url is set, use http
		if cfg.Model != "" {
			return newOnnxWrapper(cfg.Model, cfg.Device, cfg.NMSThreshold, cfg.InputSize)
		}
		if cfg.URL != "" {
			timeout, _ := time.ParseDuration(cfg.Timeout)
			return NewHTTPBackend(cfg.URL, timeout), nil
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("inference: unknown backend %q", cfg.Backend)
	}
}

// onnxWrapper adapts onnxbe.Backend to the inference.Backend interface,
// mapping onnxbe.Detection to inference.Detection.
type onnxWrapper struct {
	backend *onnxbe.Backend
}

func newOnnxWrapper(model, device string, nmsThreshold float32, inputSize int) (*onnxWrapper, error) {
	b, err := onnxbe.New(model, device, nmsThreshold, inputSize)
	if err != nil {
		return nil, err
	}
	return &onnxWrapper{backend: b}, nil
}

func (w *onnxWrapper) Detect(jpeg []byte) ([]Detection, error) {
	dets, err := w.backend.Detect(jpeg)
	if err != nil {
		return nil, err
	}
	result := make([]Detection, len(dets))
	for i, d := range dets {
		result[i] = Detection{
			Class:      d.Class,
			Confidence: d.Confidence,
			BBox:       d.BBox,
		}
	}
	return result, nil
}

func (w *onnxWrapper) Close() error {
	return w.backend.Close()
}

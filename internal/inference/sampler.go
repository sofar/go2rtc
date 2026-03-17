package inference

import (
	"time"

	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/AlexxIT/go2rtc/internal/ffmpeg"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/magic"
)

// Sampler periodically captures frames from a camera stream, runs
// detection, and publishes results to the event bus.
type Sampler struct {
	camera  string
	regions map[string]*camera.Region
	backend Backend
	bus     *events.Bus
	minConf float32

	interval time.Duration
	stop     chan struct{}
}

// NewSampler creates a sampler for a camera.
func NewSampler(cam *camera.Camera, backend Backend, bus *events.Bus, interval time.Duration, minConf float32) *Sampler {
	if interval <= 0 {
		interval = time.Second / 3 // ~3 fps
	}
	if minConf <= 0 {
		minConf = 0.5
	}
	return &Sampler{
		camera:   cam.Name,
		regions:  cam.Regions,
		backend:  backend,
		bus:      bus,
		minConf:  minConf,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start begins the sampling loop in a goroutine.
func (s *Sampler) Start() {
	go s.run()
}

// Stop terminates the sampling loop.
func (s *Sampler) Stop() {
	close(s.stop)
}

func (s *Sampler) run() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.sample()
		}
	}
}

func (s *Sampler) sample() {
	jpeg := s.captureFrame()
	if jpeg == nil {
		return
	}

	detections, err := s.backend.Detect(jpeg)
	if err != nil {
		log.Debug().Err(err).Str("camera", s.camera).Msg("[inference] detect")
		return
	}

	// Filter by confidence threshold
	var filtered []Detection
	for _, d := range detections {
		if d.Confidence >= s.minConf {
			filtered = append(filtered, d)
		}
	}

	if len(filtered) == 0 {
		return
	}

	// If regions are configured, filter detections by region and class.
	// Otherwise emit all detections under a single event.
	if len(s.regions) > 0 {
		for regionName, region := range s.regions {
			var regionDets []Detection
			for _, d := range filtered {
				if matchesRegion(d, region) {
					regionDets = append(regionDets, d)
				}
			}
			if len(regionDets) > 0 {
				s.publish(regionName, regionDets, jpeg)
			}
		}
	} else {
		s.publish("", filtered, jpeg)
	}
}

func (s *Sampler) publish(region string, dets []Detection, jpeg []byte) {
	s.bus.Publish(&events.Event{
		Type:   events.TypeDetection,
		Camera: s.camera,
		Data: DetectionEvent{
			Camera:     s.camera,
			Region:     region,
			Detections: dets,
			Timestamp:  time.Now(),
			FrameJPEG:  jpeg,
		},
	})

	for _, d := range dets {
		log.Debug().
			Str("camera", s.camera).
			Str("region", region).
			Str("class", d.Class).
			Float32("confidence", d.Confidence).
			Msg("[inference] detection")
	}
}

// DetectionEvent is published to the event bus when objects are detected.
type DetectionEvent struct {
	Camera     string      `json:"camera"`
	Region     string      `json:"region,omitempty"`
	Detections []Detection `json:"detections"`
	Timestamp  time.Time   `json:"timestamp"`
	FrameJPEG  []byte      `json:"-"` // not serialized to JSON
}

// captureFrame grabs a single JPEG frame from the camera's stream.
func (s *Sampler) captureFrame() []byte {
	stream := streams.Get(s.camera)
	if stream == nil {
		return nil
	}

	cons := magic.NewKeyframe()
	if err := stream.AddConsumer(cons); err != nil {
		return nil
	}

	once := &core.OnceBuffer{}
	_, _ = cons.WriteTo(once)
	b := once.Buffer()
	stream.RemoveConsumer(cons)

	if len(b) == 0 {
		return nil
	}

	// Convert H264/H265 keyframe to JPEG via ffmpeg
	switch cons.CodecName() {
	case core.CodecH264, core.CodecH265:
		jpeg, err := ffmpeg.JPEGWithScale(b, 640, -1)
		if err != nil {
			log.Debug().Err(err).Str("camera", s.camera).Msg("[inference] transcode")
			return nil
		}
		return jpeg
	case core.CodecJPEG:
		return b
	default:
		return nil
	}
}

// matchesRegion checks if a detection's class is in the region's detect
// list. Bounding box vs polygon intersection is left for future work —
// for now, any detection with a matching class counts.
func matchesRegion(d Detection, region *camera.Region) bool {
	for _, cls := range region.Detect {
		if cls == d.Class {
			return true
		}
	}
	return false
}

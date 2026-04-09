package inference

import (
	"bytes"
	"image/jpeg"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/pion/rtp"
)

// Sampler is a persistent core.Consumer that receives video keyframes
// from a camera stream and periodically runs object detection on them.
// Unlike the previous implementation, it stays attached to the stream
// permanently — no add/remove consumer churn per frame.
type Sampler struct {
	core.Connection

	camera         string
	regions        map[string]*camera.Region
	backend        Backend
	bus            *events.Bus
	minConf        float32
	minArea        float32
	classThreshold map[string]float32
	interval       time.Duration

	// Latest keyframe data (written by packet handler, read by sampler)
	mu          sync.Mutex
	lastFrame   []byte // raw H264/H265 keyframe (AVCC format)
	lastCodec   string
	lastFrameAt time.Time

	// Continuous background suppression: tracks detections that have been
	// present at the same location for many consecutive frames. Once a
	// detection reaches bgStaleFrames it's considered static background
	// (shadow, texture, etc.) and is suppressed. It's released when it
	// disappears for bgAbsentFrames consecutive frames.
	bgTrackers []bgTracker

	stop chan struct{}
}

// bgTracker tracks a single persistent detection across frames.
type bgTracker struct {
	class   string
	bbox    [4]float32 // running average bbox
	present int        // consecutive frames present
	absent  int        // consecutive frames absent
	stale   bool       // suppressed as background
}

// NewSampler creates a sampler for a camera.
func NewSampler(cam *camera.Camera, backend Backend, bus *events.Bus, interval time.Duration, minConf, minArea float32, classThreshold map[string]float32) *Sampler {
	if interval <= 0 {
		interval = time.Second / 3
	}
	if minConf <= 0 {
		minConf = 0.5
	}
	if minArea <= 0 {
		minArea = 0.01
	}
	return &Sampler{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: "inference",
			Medias: []*core.Media{
				{
					Kind:      core.KindVideo,
					Direction: core.DirectionSendonly,
					Codecs: []*core.Codec{
						{Name: core.CodecH264},
						{Name: core.CodecH265},
					},
				},
			},
		},
		camera:         cam.Name,
		regions:        cam.Regions,
		backend:        backend,
		bus:            bus,
		minConf:        minConf,
		minArea:        minArea,
		classThreshold: classThreshold,
		interval:       interval,
		stop:           make(chan struct{}),
	}
}

func (s *Sampler) GetMedias() []*core.Media {
	return s.Medias
}

func (s *Sampler) AddTrack(media *core.Media, _ *core.Codec, track *core.Receiver) error {
	handler := core.NewSender(media, track.Codec)

	switch track.Codec.Name {
	case core.CodecH264:
		handler.Handler = func(packet *rtp.Packet) {
			if h264.IsKeyframe(packet.Payload) {
				s.storeKeyframe(packet.Payload, core.CodecH264)
			}
		}
		if track.Codec.IsRTP() {
			handler.Handler = h264.RTPDepay(track.Codec, handler.Handler)
		} else {
			handler.Handler = h264.RepairAVCC(track.Codec, handler.Handler)
		}

	case core.CodecH265:
		handler.Handler = func(packet *rtp.Packet) {
			if h265.IsKeyframe(packet.Payload) {
				s.storeKeyframe(packet.Payload, core.CodecH265)
			}
		}
		if track.Codec.IsRTP() {
			handler.Handler = h265.RTPDepay(track.Codec, handler.Handler)
		} else {
			handler.Handler = h265.RepairAVCC(track.Codec, handler.Handler)
		}

	default:
		return nil // ignore audio etc.
	}

	handler.HandleRTP(track)
	s.Senders = append(s.Senders, handler)
	return nil
}

func (s *Sampler) storeKeyframe(payload []byte, codec string) {
	data := make([]byte, len(payload))
	copy(data, payload)

	s.mu.Lock()
	s.lastFrame = data
	s.lastCodec = codec
	s.lastFrameAt = time.Now()
	s.mu.Unlock()

}

// Start begins the sampling loop and attaches to the stream.
func (s *Sampler) Start() {
	stream := streams.Get(s.camera)
	if stream == nil {
		log.Error().Str("camera", s.camera).Msg("[inference] stream not found")
		return
	}

	if err := stream.AddConsumer(s); err != nil {
		log.Error().Err(err).Str("camera", s.camera).Msg("[inference] add consumer")
		return
	}

	log.Debug().Str("camera", s.camera).Int("senders", len(s.Senders)).Msg("[inference] attached as consumer")

	go s.run(stream)
}

// Stop terminates the sampling loop and detaches from the stream.
func (s *Sampler) Stop() error {
	close(s.stop)
	for _, sender := range s.Senders {
		sender.Close()
	}
	return nil
}

func (s *Sampler) run(stream *streams.Stream) {
	// Wait for first keyframe
	time.Sleep(3 * time.Second)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			stream.RemoveConsumer(s)
			return
		case <-ticker.C:
			s.sample()
		}
	}
}

// captureJPEG is set by the snapshot module to provide JPEG frame capture.
var captureJPEG func(camera string) []byte

// RegisterFrameCapture allows the snapshot module to provide its
// capture function to the inference sampler.
func RegisterFrameCapture(fn func(camera string) []byte) {
	captureJPEG = fn
}

func (s *Sampler) sample() {
	// Check that the stream is alive (we have recent keyframes)
	s.mu.Lock()
	frameAge := time.Since(s.lastFrameAt)
	s.mu.Unlock()

	if frameAge > 10*time.Second {
		return // stream not active
	}

	// Use the snapshot module's capture pipeline — it handles
	// codec negotiation, ffmpeg transcode, and produces a proper JPEG.
	if captureJPEG == nil {
		return
	}
	jpegData := captureJPEG(s.camera)
	if len(jpegData) == 0 {
		return
	}

	if len(s.regions) > 0 {
		s.sampleRegions(jpegData)
	} else {
		s.sampleFullFrame(jpegData)
	}
}

func (s *Sampler) sampleFullFrame(jpegData []byte) {
	t0 := time.Now()
	detections, err := s.backend.Detect(jpegData)
	if err != nil {
		log.Debug().Err(err).Str("camera", s.camera).Msg("[inference] detect")
		return
	}
	log.Trace().Str("camera", s.camera).Dur("ms", time.Since(t0)).Int("detections", len(detections)).Msg("[inference] detect")

	s.bgUpdate(detections)

	filtered := s.filterByConfidence(detections)
	if len(filtered) > 0 {
		s.publish("", filtered, jpegData)
	}
}

func (s *Sampler) sampleRegions(fullJPEG []byte) {
	// Run detection on the FULL frame once, then assign detections to
	// regions by checking if the bbox center falls inside each polygon.
	// This avoids cropping small regions and upscaling them to 640x640,
	// which causes YOLO to hallucinate objects from texture artifacts.
	t0 := time.Now()
	detections, err := s.backend.Detect(fullJPEG)
	if err != nil {
		log.Debug().Err(err).Str("camera", s.camera).Msg("[inference] detect")
		return
	}
	log.Trace().Str("camera", s.camera).Dur("ms", time.Since(t0)).Int("detections", len(detections)).Msg("[inference] detect")

	s.bgUpdate(detections)

	imgW, imgH := jpegDimensions(fullJPEG)
	if imgW == 0 || imgH == 0 {
		return
	}

	for regionName, region := range s.regions {
		if len(region.Polygon) < 3 {
			continue
		}

		// Per-region confidence and min_area override globals
		regionConf := s.minConf
		if region.Confidence > 0 {
			regionConf = region.Confidence
		}
		regionMinArea := s.minArea
		if region.MinArea > 0 {
			regionMinArea = region.MinArea
		}

		var regionDets []Detection
		for _, d := range detections {
			threshold := regionConf
			if ct, ok := s.classThreshold[d.Class]; ok {
				threshold = ct
			}
			if d.Confidence < threshold {
				continue
			}

			// Reject tiny detections (noise).
			bboxArea := (d.BBox[2] - d.BBox[0]) * (d.BBox[3] - d.BBox[1])
			if bboxArea < regionMinArea {
				continue
			}

			classMatch := false
			for _, cls := range region.Detect {
				if cls == d.Class {
					classMatch = true
					break
				}
			}
			if !classMatch {
				continue
			}

			// Check that the detection center falls inside the polygon.
			centerX := float64(d.BBox[0]+d.BBox[2]) / 2 * float64(imgW)
			centerY := float64(d.BBox[1]+d.BBox[3]) / 2 * float64(imgH)
			if !pointInPolygon(centerX, centerY, region.Polygon) {
				continue
			}

			// Suppress learned background detections.
			if s.isBackground(d) {
				continue
			}

			regionDets = append(regionDets, d)
		}

		if len(regionDets) > 0 {
			s.publish(regionName, regionDets, fullJPEG)
		}
	}
}

func (s *Sampler) filterByConfidence(detections []Detection) []Detection {
	var filtered []Detection
	for _, d := range detections {
		threshold := s.minConf
		if ct, ok := s.classThreshold[d.Class]; ok {
			threshold = ct
		}
		if d.Confidence < threshold {
			continue
		}
		bboxArea := (d.BBox[2] - d.BBox[0]) * (d.BBox[3] - d.BBox[1])
		if bboxArea < s.minArea {
			continue
		}
		if s.isBackground(d) {
			continue
		}
		filtered = append(filtered, d)
	}
	return filtered
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
	FrameJPEG  []byte      `json:"-"`
}

// --- continuous background suppression ---

const (
	bgStaleFrames  = 90  // frames present before considered background (~30s at 3fps)
	bgAbsentFrames = 15  // frames absent before tracker is released (~5s at 3fps)
	bgMatchIoU     = 0.5 // IoU threshold to match a detection to a tracker
)

// bgUpdate updates background trackers with the current frame's detections.
// Must be called once per frame with ALL detections (before filtering).
func (s *Sampler) bgUpdate(detections []Detection) {
	// Mark existing trackers as not-seen this frame
	existingCount := len(s.bgTrackers)
	matched := make([]bool, existingCount)

	for _, d := range detections {
		bestIdx := -1
		bestIoU := float32(0)
		// Only match against existing trackers, not newly added ones
		for i := 0; i < existingCount; i++ {
			if s.bgTrackers[i].class != d.Class {
				continue
			}
			v := bboxIoU(d.BBox, s.bgTrackers[i].bbox)
			if v > bestIoU {
				bestIoU = v
				bestIdx = i
			}
		}

		if bestIdx >= 0 && bestIoU > bgMatchIoU {
			// Update existing tracker with exponential moving average
			tr := &s.bgTrackers[bestIdx]
			const alpha = 0.1
			for k := 0; k < 4; k++ {
				tr.bbox[k] = tr.bbox[k]*(1-alpha) + d.BBox[k]*alpha
			}
			tr.present++
			tr.absent = 0
			matched[bestIdx] = true

			if !tr.stale && tr.present >= bgStaleFrames {
				tr.stale = true
				log.Info().
					Str("camera", s.camera).
					Str("class", tr.class).
					Int("frames", tr.present).
					Msg("[inference] static detection suppressed")
			}
		} else {
			// New tracker
			s.bgTrackers = append(s.bgTrackers, bgTracker{
				class:   d.Class,
				bbox:    d.BBox,
				present: 1,
			})
		}
	}

	// Increment absent count for unmatched trackers, remove dead ones
	alive := make([]bgTracker, 0, len(s.bgTrackers))
	for i := range s.bgTrackers {
		if i < existingCount && !matched[i] {
			s.bgTrackers[i].absent++
		}
		if s.bgTrackers[i].absent < bgAbsentFrames {
			alive = append(alive, s.bgTrackers[i])
		} else if s.bgTrackers[i].stale {
			log.Info().
				Str("camera", s.camera).
				Str("class", s.bgTrackers[i].class).
				Msg("[inference] static detection released")
		}
	}
	s.bgTrackers = alive
}

// isBackground checks if a detection matches a stale background tracker.
func (s *Sampler) isBackground(d Detection) bool {
	for i := range s.bgTrackers {
		if !s.bgTrackers[i].stale {
			continue
		}
		if d.Class != s.bgTrackers[i].class {
			continue
		}
		if bboxIoU(d.BBox, s.bgTrackers[i].bbox) > bgMatchIoU {
			return true
		}
	}
	return false
}

// bboxIoU computes intersection-over-union for two normalized bboxes.
func bboxIoU(a, b [4]float32) float32 {
	ix1 := max32(a[0], b[0])
	iy1 := max32(a[1], b[1])
	ix2 := min32(a[2], b[2])
	iy2 := min32(a[3], b[3])

	iw := max32(0, ix2-ix1)
	ih := max32(0, iy2-iy1)
	inter := iw * ih

	areaA := (a[2] - a[0]) * (a[3] - a[1])
	areaB := (b[2] - b[0]) * (b[3] - b[1])
	union := areaA + areaB - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

// --- geometry helpers ---



func pointInPolygon(px, py float64, polygon [][2]int) bool {
	n := len(polygon)
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := float64(polygon[i][0]), float64(polygon[i][1])
		xj, yj := float64(polygon[j][0]), float64(polygon[j][1])

		if ((yi > py) != (yj > py)) &&
			(px < (xj-xi)*(py-yi)/(yj-yi)+xi) {
			inside = !inside
		}
		j = i
	}
	return inside
}

func jpegDimensions(data []byte) (int, int) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

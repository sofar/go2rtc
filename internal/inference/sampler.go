package inference

import (
	"bytes"
	"image"
	"image/draw"
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
	classThreshold map[string]float32
	interval       time.Duration

	// Latest keyframe data (written by packet handler, read by sampler)
	mu          sync.Mutex
	lastFrame   []byte // raw H264/H265 keyframe (AVCC format)
	lastCodec   string
	lastFrameAt time.Time

	stop chan struct{}
}

// NewSampler creates a sampler for a camera.
func NewSampler(cam *camera.Camera, backend Backend, bus *events.Bus, interval time.Duration, minConf float32, classThreshold map[string]float32) *Sampler {
	if interval <= 0 {
		interval = time.Second / 3
	}
	if minConf <= 0 {
		minConf = 0.5
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
	detections, err := s.backend.Detect(jpegData)
	if err != nil {
		log.Debug().Err(err).Str("camera", s.camera).Msg("[inference] detect")
		return
	}

	filtered := s.filterByConfidence(detections)
	if len(filtered) > 0 {
		s.publish("", filtered, jpegData)
	}
}

func (s *Sampler) sampleRegions(fullJPEG []byte) {
	img, err := jpeg.Decode(bytes.NewReader(fullJPEG))
	if err != nil {
		return
	}

	for regionName, region := range s.regions {
		if len(region.Polygon) < 3 {
			continue
		}

		bounds := polygonBounds(region.Polygon)
		cropped := cropImage(img, bounds)
		if cropped == nil {
			continue
		}

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 85}); err != nil {
			continue
		}
		cropJPEG := buf.Bytes()

		detections, err := s.backend.Detect(cropJPEG)
		if err != nil {
			log.Debug().Err(err).Str("camera", s.camera).Str("region", regionName).Msg("[inference] detect")
			continue
		}

		var regionDets []Detection
		for _, d := range detections {
			threshold := s.minConf
			if ct, ok := s.classThreshold[d.Class]; ok {
				threshold = ct
			}
			if d.Confidence < threshold {
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

			imgW := img.Bounds().Dx()
			imgH := img.Bounds().Dy()
			d.BBox = remapBBox(d.BBox, bounds, imgW, imgH)
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
		if d.Confidence >= threshold {
			filtered = append(filtered, d)
		}
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

// --- geometry helpers ---

func polygonBounds(polygon [][2]int) image.Rectangle {
	if len(polygon) == 0 {
		return image.Rectangle{}
	}
	minX, minY := polygon[0][0], polygon[0][1]
	maxX, maxY := minX, minY
	for _, p := range polygon[1:] {
		if p[0] < minX { minX = p[0] }
		if p[0] > maxX { maxX = p[0] }
		if p[1] < minY { minY = p[1] }
		if p[1] > maxY { maxY = p[1] }
	}
	return image.Rect(minX, minY, maxX, maxY)
}

func cropImage(img image.Image, bounds image.Rectangle) image.Image {
	imgBounds := img.Bounds()
	bounds = bounds.Intersect(imgBounds)
	if bounds.Empty() || bounds.Dx() < 10 || bounds.Dy() < 10 {
		return nil
	}

	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := img.(subImager); ok {
		return si.SubImage(bounds)
	}

	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Src)
	return dst
}

func remapBBox(bbox [4]float32, cropBounds image.Rectangle, imgW, imgH int) [4]float32 {
	cx := float64(cropBounds.Min.X)
	cy := float64(cropBounds.Min.Y)
	cw := float64(cropBounds.Dx())
	ch := float64(cropBounds.Dy())
	fw := float64(imgW)
	fh := float64(imgH)

	return [4]float32{
		float32((cx + float64(bbox[0])*cw) / fw),
		float32((cy + float64(bbox[1])*ch) / fh),
		float32((cx + float64(bbox[2])*cw) / fw),
		float32((cy + float64(bbox[3])*ch) / fh),
	}
}

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

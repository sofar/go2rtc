package inference

import (
	"bytes"
	"image"
	"image/jpeg"
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
	bus            *events.Bus
	minConf        float32
	classThreshold map[string]float32

	interval time.Duration
	stop     chan struct{}
}

// NewSampler creates a sampler for a camera.
func NewSampler(cam *camera.Camera, backend Backend, bus *events.Bus, interval time.Duration, minConf float32, classThreshold map[string]float32) *Sampler {
	if interval <= 0 {
		interval = time.Second / 3 // ~3 fps
	}
	if minConf <= 0 {
		minConf = 0.5
	}
	return &Sampler{
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
	fullJPEG, _, _ := s.captureFrame()
	if fullJPEG == nil {
		return
	}

	if len(s.regions) > 0 {
		// Per-region inference: crop each region from the native frame,
		// run detection on the crop. The object fills the model input
		// properly instead of being a tiny part of the full frame.
		s.sampleRegions(fullJPEG)
	} else {
		s.sampleFullFrame(fullJPEG)
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
	// Decode the full frame once
	img, err := jpeg.Decode(bytes.NewReader(fullJPEG))
	if err != nil {
		return
	}

	for regionName, region := range s.regions {
		if len(region.Polygon) < 3 {
			continue
		}

		// Crop to region bounding box
		bounds := polygonBounds(region.Polygon)
		cropped := cropImage(img, bounds)
		if cropped == nil {
			continue
		}

		// Encode crop as JPEG for the backend
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 85}); err != nil {
			continue
		}
		cropJPEG := buf.Bytes()

		// Run detection on the crop
		detections, err := s.backend.Detect(cropJPEG)
		if err != nil {
			log.Debug().Err(err).Str("camera", s.camera).Str("region", regionName).Msg("[inference] detect")
			continue
		}

		// Filter by confidence and class
		var regionDets []Detection
		for _, d := range detections {
			threshold := s.minConf
			if ct, ok := s.classThreshold[d.Class]; ok {
				threshold = ct
			}
			if d.Confidence < threshold {
				continue
			}
			// Check class matches region's detect list
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

			// Remap bbox from crop coordinates back to full frame
			// normalized coordinates
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
	FrameJPEG  []byte      `json:"-"` // not serialized to JSON
}

// captureFrame grabs a single JPEG frame from the camera's stream.
// Returns the JPEG data and the native image dimensions (for polygon
// matching — the bbox normalized [0..1] maps to these dimensions).
func (s *Sampler) captureFrame() ([]byte, int, int) {
	stream := streams.Get(s.camera)
	if stream == nil {
		return nil, 0, 0
	}

	cons := magic.NewKeyframe()
	if err := stream.AddConsumer(cons); err != nil {
		return nil, 0, 0
	}

	once := &core.OnceBuffer{}
	_, _ = cons.WriteTo(once)
	b := once.Buffer()
	stream.RemoveConsumer(cons)

	if len(b) == 0 {
		return nil, 0, 0
	}

	// Convert H264/H265 keyframe to JPEG via ffmpeg at native resolution
	switch cons.CodecName() {
	case core.CodecH264, core.CodecH265:
		jpegData, err := ffmpeg.JPEGWithScale(b, -1, -1)
		if err != nil {
			log.Debug().Err(err).Str("camera", s.camera).Msg("[inference] transcode")
			return nil, 0, 0
		}
		w, h := jpegDimensions(jpegData)
		return jpegData, w, h
	case core.CodecJPEG:
		w, h := jpegDimensions(b)
		return b, w, h
	default:
		return nil, 0, 0
	}
}

// jpegDimensions decodes a JPEG header to get width and height.
func jpegDimensions(data []byte) (int, int) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// polygonBounds returns the bounding rectangle of a polygon.
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

// cropImage extracts a rectangular region from an image.
func cropImage(img image.Image, bounds image.Rectangle) image.Image {
	// Clamp to image bounds
	imgBounds := img.Bounds()
	bounds = bounds.Intersect(imgBounds)
	if bounds.Empty() {
		return nil
	}

	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := img.(subImager); ok {
		return si.SubImage(bounds)
	}
	return nil
}

// remapBBox converts a bbox from crop-local normalized coordinates to
// full-frame normalized coordinates.
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

// matchesRegion checks if a detection's class is in the region's detect
// list AND the detection bbox center falls within the region's polygon.
// Polygon coordinates are in image pixels; bbox is normalized [0..1].
// We need the image dimensions to convert between the two.
func matchesRegion(d Detection, region *camera.Region, imgW, imgH int) bool {
	// Check class first (fast path)
	classMatch := false
	for _, cls := range region.Detect {
		if cls == d.Class {
			classMatch = true
			break
		}
	}
	if !classMatch {
		return false
	}

	// Check if bbox center is inside the polygon
	if len(region.Polygon) < 3 || imgW <= 0 || imgH <= 0 {
		return classMatch // no valid polygon, just use class match
	}

	// Bbox center in pixel coordinates
	cx := float64(d.BBox[0]+d.BBox[2]) / 2 * float64(imgW)
	cy := float64(d.BBox[1]+d.BBox[3]) / 2 * float64(imgH)

	return pointInPolygon(cx, cy, region.Polygon)
}

// pointInPolygon checks if point (px,py) is inside a polygon using
// ray casting algorithm. Polygon points are in pixel coordinates.
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

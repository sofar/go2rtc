// Package snapshot provides enhanced camera snapshot endpoints with
// region cropping and detection annotation overlays.
//
// Endpoints:
//   - GET /api/snapshot/<camera> — clean URL alias for frame.jpeg
//   - GET /api/snapshot/<camera>?region=<name> — cropped to named ROI
//   - GET /api/snapshot/<camera>?annotate=true — overlay detection boxes
package snapshot

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/camera"
	"github.com/AlexxIT/go2rtc/internal/events"
	"github.com/AlexxIT/go2rtc/internal/ffmpeg"
	"github.com/AlexxIT/go2rtc/internal/inference"
	"github.com/AlexxIT/go2rtc/internal/offline"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/magic"
	"github.com/AlexxIT/go2rtc/pkg/mjpeg"
	"github.com/AlexxIT/go2rtc/pkg/textdraw"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

func Init() {
	log = app.GetLogger("snapshot")

	if len(camera.All()) == 0 {
		return
	}

	api.HandleFunc("api/snapshot/", apiSnapshot)
	api.HandleFunc("api/session/cameras", apiSessionCameras)
}

func apiSnapshot(w http.ResponseWriter, r *http.Request) {
	// Parse camera name from path: /api/snapshot/<name>
	path := r.URL.Path
	name := strings.TrimPrefix(path, "/api/snapshot/")
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		http.Error(w, "camera name required in path", http.StatusBadRequest)
		return
	}

	cam := camera.Get(name)
	if cam == nil {
		http.Error(w, "camera not found: "+name, http.StatusNotFound)
		return
	}

	// Check if offline — serve placeholder
	if placeholder := offline.GetPlaceholder(name); placeholder != nil {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(placeholder)
		return
	}

	// Capture a frame
	jpeg_data := captureJPEG(name)
	if jpeg_data == nil {
		http.Error(w, "failed to capture frame", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()

	// Region cropping
	if regionName := q.Get("region"); regionName != "" {
		if cam.Regions == nil {
			http.Error(w, "no regions configured for camera", http.StatusBadRequest)
			return
		}
		region, ok := cam.Regions[regionName]
		if !ok {
			http.Error(w, "region not found: "+regionName, http.StatusNotFound)
			return
		}
		cropped, err := cropToRegion(jpeg_data, region)
		if err != nil {
			http.Error(w, "crop failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jpeg_data = cropped
	}

	// Detection annotation
	if q.Get("annotate") == "true" {
		jpeg_data = annotateDetections(jpeg_data, name)
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(jpeg_data)
}

// captureJPEG grabs a single JPEG frame from a camera's stream.
func captureJPEG(name string) []byte {
	stream := streams.Get(name)
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

	switch cons.CodecName() {
	case core.CodecH264, core.CodecH265:
		jpeg_data, err := ffmpeg.JPEGWithScale(b, -1, -1)
		if err != nil {
			log.Debug().Err(err).Str("camera", name).Msg("[snapshot] transcode")
			return nil
		}
		return jpeg_data
	case core.CodecJPEG:
		return mjpeg.FixJPEG(b)
	default:
		return nil
	}
}

// cropToRegion crops a JPEG to the bounding box of a polygon region.
func cropToRegion(jpegData []byte, region *camera.Region) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	bounds := polygonBounds(region.Polygon)
	cropped := img.(interface {
		SubImage(r image.Rectangle) image.Image
	}).SubImage(bounds)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return buf.Bytes(), nil
}

// polygonBounds returns the bounding rectangle of a polygon.
func polygonBounds(polygon [][2]int) image.Rectangle {
	if len(polygon) == 0 {
		return image.Rectangle{}
	}
	minX, minY := polygon[0][0], polygon[0][1]
	maxX, maxY := minX, minY
	for _, p := range polygon[1:] {
		if p[0] < minX {
			minX = p[0]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	return image.Rect(minX, minY, maxX, maxY)
}

// annotateDetections draws detection bounding boxes on a JPEG image
// using the most recent detections from the event bus.
func annotateDetections(jpegData []byte, cameraName string) []byte {
	// Get recent detections for this camera
	dets := getRecentDetections(cameraName)
	if len(dets) == 0 {
		return jpegData
	}

	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return jpegData
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	// Convert to RGBA for drawing
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)

	green := color.RGBA{R: 0, G: 255, B: 0, A: 255}
	bgColor := color.RGBA{R: 0, G: 0, B: 0, A: 180}

	// Scale font size based on image resolution
	fontSize := float64(w) / 40
	if fontSize < 14 {
		fontSize = 14
	}
	if fontSize > 48 {
		fontSize = 48
	}
	pad := int(fontSize * 0.3)
	thickness := max(2, w/640)

	for _, d := range dets {
		// Convert normalized bbox to pixel coords
		x1 := int(d.BBox[0] * float32(w))
		y1 := int(d.BBox[1] * float32(h))
		x2 := int(d.BBox[2] * float32(w))
		y2 := int(d.BBox[3] * float32(h))

		drawRect(rgba, x1, y1, x2, y2, green, thickness)

		// Draw label: "class 95%"
		label := fmt.Sprintf("%s %d%%", d.Class, int(d.Confidence*100))
		labelW := textdraw.MeasureString(label, fontSize, true)
		labelH := int(fontSize * 1.3)

		// Label background above the box
		lx := x1
		ly := y1 - labelH
		if ly < bounds.Min.Y {
			ly = y1 // put below top edge if no room above
		}
		drawFilledRect(rgba, lx, ly, lx+labelW+pad*2, ly+labelH, bgColor)
		textdraw.DrawStringBold(rgba, lx+pad, ly+labelH-pad, label, green, fontSize, true)
	}

	var buf bytes.Buffer
	jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

// drawRect draws a rectangle outline on an RGBA image.
func drawRect(img *image.RGBA, x1, y1, x2, y2 int, col color.RGBA, thickness int) {
	bounds := img.Bounds()
	for t := 0; t < thickness; t++ {
		// Top and bottom
		for x := x1; x <= x2; x++ {
			if y1+t >= bounds.Min.Y && y1+t < bounds.Max.Y && x >= bounds.Min.X && x < bounds.Max.X {
				img.Set(x, y1+t, col)
			}
			if y2-t >= bounds.Min.Y && y2-t < bounds.Max.Y && x >= bounds.Min.X && x < bounds.Max.X {
				img.Set(x, y2-t, col)
			}
		}
		// Left and right
		for y := y1; y <= y2; y++ {
			if x1+t >= bounds.Min.X && x1+t < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
				img.Set(x1+t, y, col)
			}
			if x2-t >= bounds.Min.X && x2-t < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
				img.Set(x2-t, y, col)
			}
		}
	}
}

// drawFilledRect draws a solid filled rectangle with alpha blending.
func drawFilledRect(img *image.RGBA, x1, y1, x2, y2 int, col color.RGBA) {
	bounds := img.Bounds()
	for y := y1; y < y2; y++ {
		for x := x1; x < x2; x++ {
			if x >= bounds.Min.X && x < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
				r0, g0, b0, _ := img.At(x, y).RGBA()
				a := uint32(col.A)
				r := (uint32(col.R)*a + uint32(r0>>8)*(255-a)) / 255
				g := (uint32(col.G)*a + uint32(g0>>8)*(255-a)) / 255
				b := (uint32(col.B)*a + uint32(b0>>8)*(255-a)) / 255
				img.Set(x, y, color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
			}
		}
	}
}

// apiSessionCameras returns a JSON list of cameras that have a given region.
// Used by the UI to know which cameras to fetch annotated snapshots from.
func apiSessionCameras(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	if region == "" {
		http.Error(w, "region parameter required", http.StatusBadRequest)
		return
	}

	var result []string
	for _, cam := range camera.All() {
		if cam.Regions == nil {
			continue
		}
		if _, ok := cam.Regions[region]; ok {
			result = append(result, cam.Name)
		}
	}

	api.ResponseJSON(w, result)
}

// getRecentDetections returns the most recent detections for a camera
// from the stored event buffer (last 5 seconds).
func getRecentDetections(cameraName string) []inference.Detection {
	e := events.GetRecentByCamera(events.TypeDetection, cameraName, 5*time.Second)
	if e == nil {
		return nil
	}
	if de, ok := e.Data.(inference.DetectionEvent); ok {
		return de.Detections
	}
	return nil
}

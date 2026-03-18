// Package onnxbe provides a native ONNX Runtime detection backend for
// YOLOv8 models. It loads an .onnx model file, runs inference on JPEG
// frames, and returns detections with class names and bounding boxes.
//
// Supports CPU and OpenVINO execution providers. OpenVINO is used
// automatically when its shared library is available.
package onnxbe

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"sort"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	inputSize  = 640
	numClasses = 80
)

// Detection represents a single detected object.
type Detection struct {
	Class      string     `json:"class"`
	Confidence float32    `json:"confidence"`
	BBox       [4]float32 `json:"bbox"`
}

// Backend runs YOLOv8 inference via ONNX Runtime.
type Backend struct {
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	output  *ort.Tensor[float32]
	nmsThreshold float32
}

// New creates an ONNX Runtime backend for a YOLOv8 model.
// device can be "cpu" or "openvino".
func New(modelPath string, device string, nmsThreshold float32) (*Backend, error) {
	if nmsThreshold <= 0 {
		nmsThreshold = 0.45
	}

	// Initialize ONNX Runtime (safe to call multiple times)
	ort.SetSharedLibraryPath(findOrtLib())
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("onnxbe: init environment: %w", err)
	}

	// Create input/output tensors
	inputShape := ort.NewShape(1, 3, inputSize, inputSize)
	input, err := ort.NewEmptyTensor[float32](inputShape)
	if err != nil {
		return nil, fmt.Errorf("onnxbe: create input tensor: %w", err)
	}

	outputShape := ort.NewShape(1, numClasses+4, 8400)
	output, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		input.Destroy()
		return nil, fmt.Errorf("onnxbe: create output tensor: %w", err)
	}

	// Session options
	opts, err := ort.NewSessionOptions()
	if err != nil {
		input.Destroy()
		output.Destroy()
		return nil, fmt.Errorf("onnxbe: session options: %w", err)
	}
	defer opts.Destroy()

	// Try to append OpenVINO provider if requested
	if device == "openvino" {
		err := opts.AppendExecutionProviderOpenVINO(map[string]string{
			"device_type": "CPU",
		})
		if err != nil {
			// Fall back to CPU silently
			_ = err
		}
	}

	session, err := ort.NewAdvancedSession(
		modelPath,
		[]string{"images"}, []string{"output0"},
		[]ort.ArbitraryTensor{input},
		[]ort.ArbitraryTensor{output},
		opts,
	)
	if err != nil {
		input.Destroy()
		output.Destroy()
		return nil, fmt.Errorf("onnxbe: create session: %w", err)
	}

	return &Backend{
		session:      session,
		input:        input,
		output:       output,
		nmsThreshold: nmsThreshold,
	}, nil
}

func (b *Backend) Detect(jpegData []byte) ([]Detection, error) {
	// Decode JPEG
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, fmt.Errorf("onnxbe: decode jpeg: %w", err)
	}

	// Preprocess: resize to 640x640, normalize to [0,1], CHW format
	preprocess(img, b.input.GetData())

	// Run inference
	if err := b.session.Run(); err != nil {
		return nil, fmt.Errorf("onnxbe: run: %w", err)
	}

	// Postprocess: parse YOLOv8 output, apply NMS
	imgBounds := img.Bounds()
	dets := postprocess(b.output.GetData(), imgBounds.Dx(), imgBounds.Dy(), b.nmsThreshold)

	return dets, nil
}

func (b *Backend) Close() error {
	if b.session != nil {
		b.session.Destroy()
	}
	if b.input != nil {
		b.input.Destroy()
	}
	if b.output != nil {
		b.output.Destroy()
	}
	return nil
}

// preprocess resizes the image to 640x640 and converts to CHW float32
// normalized to [0,1]. Uses letterboxing to preserve aspect ratio.
func preprocess(img image.Image, buf []float32) {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	// Letterbox scale
	scale := float64(inputSize) / math.Max(float64(srcW), float64(srcH))
	newW := int(float64(srcW) * scale)
	newH := int(float64(srcH) * scale)
	padX := (inputSize - newW) / 2
	padY := (inputSize - newH) / 2

	// Fill with grey (114/255)
	grey := float32(114.0 / 255.0)
	for i := range buf {
		buf[i] = grey
	}

	// CHW layout: buf[c*640*640 + y*640 + x]
	planeSize := inputSize * inputSize

	for y := 0; y < newH; y++ {
		srcY := int(float64(y) / scale)
		if srcY >= srcH {
			srcY = srcH - 1
		}
		for x := 0; x < newW; x++ {
			srcX := int(float64(x) / scale)
			if srcX >= srcW {
				srcX = srcW - 1
			}

			r, g, b, _ := img.At(bounds.Min.X+srcX, bounds.Min.Y+srcY).RGBA()
			dx := x + padX
			dy := y + padY

			buf[0*planeSize+dy*inputSize+dx] = float32(r>>8) / 255.0
			buf[1*planeSize+dy*inputSize+dx] = float32(g>>8) / 255.0
			buf[2*planeSize+dy*inputSize+dx] = float32(b>>8) / 255.0
		}
	}
}

// postprocess parses YOLOv8 output [1, 84, 8400] into detections.
// Output is transposed: each of 8400 anchors has [cx, cy, w, h, cls0..cls79].
func postprocess(data []float32, imgW, imgH int, nmsThreshold float32) []Detection {
	const numAnchors = 8400

	scale := math.Max(float64(imgW), float64(imgH)) / float64(inputSize)
	padX := (float64(inputSize) - float64(imgW)/scale) / 2
	padY := (float64(inputSize) - float64(imgH)/scale) / 2

	type rawDet struct {
		Detection
		area float32
	}
	var candidates []rawDet

	for i := 0; i < numAnchors; i++ {
		// Find best class
		bestClass := 0
		bestConf := float32(0)
		for c := 0; c < numClasses; c++ {
			conf := data[(4+c)*numAnchors+i]
			if conf > bestConf {
				bestConf = conf
				bestClass = c
			}
		}

		if bestConf < 0.1 { // very low pre-filter
			continue
		}

		// Decode box (center format → corner format)
		cx := float64(data[0*numAnchors+i])
		cy := float64(data[1*numAnchors+i])
		w := float64(data[2*numAnchors+i])
		h := float64(data[3*numAnchors+i])

		// Remove letterbox padding and scale to original image
		x1 := (cx - w/2 - padX) * scale
		y1 := (cy - h/2 - padY) * scale
		x2 := (cx + w/2 - padX) * scale
		y2 := (cy + h/2 - padY) * scale

		// Normalize to [0,1]
		nx1 := float32(clamp(x1/float64(imgW), 0, 1))
		ny1 := float32(clamp(y1/float64(imgH), 0, 1))
		nx2 := float32(clamp(x2/float64(imgW), 0, 1))
		ny2 := float32(clamp(y2/float64(imgH), 0, 1))

		label := "unknown"
		if bestClass < len(cocoLabels) {
			label = cocoLabels[bestClass]
		}

		candidates = append(candidates, rawDet{
			Detection: Detection{
				Class:      label,
				Confidence: bestConf,
				BBox:       [4]float32{nx1, ny1, nx2, ny2},
			},
			area: (nx2 - nx1) * (ny2 - ny1),
		})
	}

	// Sort by confidence descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Confidence > candidates[j].Confidence
	})

	// Greedy NMS
	var result []Detection
	suppressed := make([]bool, len(candidates))
	for i := range candidates {
		if suppressed[i] {
			continue
		}
		result = append(result, candidates[i].Detection)
		for j := i + 1; j < len(candidates); j++ {
			if suppressed[j] || candidates[j].Class != candidates[i].Class {
				continue
			}
			if iou(candidates[i].BBox, candidates[j].BBox) > nmsThreshold {
				suppressed[j] = true
			}
		}
	}

	return result
}

func iou(a, b [4]float32) float32 {
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

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func findOrtLib() string {
	paths := []string{
		"/usr/lib64/libonnxruntime.so",
		"/lib64/libonnxruntime.so",
		"/usr/lib64/libonnxruntime.so.1",
		"/lib64/libonnxruntime.so.1",
		"/usr/local/lib/libonnxruntime.so",
		"/usr/lib/libonnxruntime.so",
		"libonnxruntime.so",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "libonnxruntime.so"
}

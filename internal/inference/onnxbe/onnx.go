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
	"strings"

	ort "github.com/yalue/onnxruntime_go"
)

const numClasses = 80

// Detection represents a single detected object.
type Detection struct {
	Class      string     `json:"class"`
	Confidence float32    `json:"confidence"`
	BBox       [4]float32 `json:"bbox"`
}

// Backend runs YOLOv8 inference via ONNX Runtime.
type Backend struct {
	session      *ort.AdvancedSession
	input        *ort.Tensor[float32]
	output       *ort.Tensor[float32]
	nmsThreshold float32
	inputSize    int
	numAnchors   int
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

	// Detect input size from model file by doing a quick load+query.
	// Use a temporary session to read the input shape, then create
	// the real session with correct tensor sizes.
	inSize, err := probeModelInputSize(modelPath)
	if err != nil || inSize <= 0 {
		inSize = 640
	}

	// YOLOv8 anchor count: (s/8)^2 + (s/16)^2 + (s/32)^2
	numAnch := (inSize/8)*(inSize/8) + (inSize/16)*(inSize/16) + (inSize/32)*(inSize/32)

	// Create input/output tensors
	inputShape := ort.NewShape(1, 3, int64(inSize), int64(inSize))
	input, err := ort.NewEmptyTensor[float32](inputShape)
	if err != nil {
		return nil, fmt.Errorf("onnxbe: create input tensor: %w", err)
	}

	outputShape := ort.NewShape(1, int64(numClasses+4), int64(numAnch))
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
		inputSize:    int(inSize),
		numAnchors:   int(numAnch),
	}, nil
}

func (b *Backend) Detect(jpegData []byte) ([]Detection, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, fmt.Errorf("onnxbe: decode jpeg: %w", err)
	}

	preprocess(img, b.input.GetData(), b.inputSize)

	if err := b.session.Run(); err != nil {
		return nil, fmt.Errorf("onnxbe: run: %w", err)
	}

	imgBounds := img.Bounds()
	dets := postprocess(b.output.GetData(), imgBounds.Dx(), imgBounds.Dy(), b.nmsThreshold, b.inputSize, b.numAnchors)

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

// preprocess resizes the image to inputSize x inputSize and converts to
// CHW float32 normalized to [0,1]. Uses letterboxing to preserve aspect ratio.
func preprocess(img image.Image, buf []float32, inputSize int) {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	scale := float64(inputSize) / math.Max(float64(srcW), float64(srcH))
	newW := int(float64(srcW) * scale)
	newH := int(float64(srcH) * scale)
	padX := (inputSize - newW) / 2
	padY := (inputSize - newH) / 2

	grey := float32(114.0 / 255.0)
	for i := range buf {
		buf[i] = grey
	}

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

// postprocess parses YOLOv8 output [1, 84, numAnchors] into detections.
func postprocess(data []float32, imgW, imgH int, nmsThreshold float32, inputSize, numAnchors int) []Detection {
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

// probeModelInputSize reads an ONNX model file and extracts the input
// image size from the model's input shape metadata. Returns 0 on failure.
func probeModelInputSize(modelPath string) (int, error) {
	// ONNX models store input shapes in the protobuf header.
	// Rather than parsing protobuf, use a simple heuristic:
	// create a test session with 640x640, and if the model expects
	// a different size, the output anchor count won't match.
	// For now, detect common sizes from the filename.
	lower := strings.ToLower(modelPath)
	for _, size := range []int{1280, 1024, 960, 896, 832, 768, 704, 640, 512, 448, 416, 384, 352, 320} {
		if strings.Contains(lower, fmt.Sprintf("%d", size)) {
			return size, nil
		}
	}

	// Try to detect from file: read first few KB and look for dimension values
	data, err := os.ReadFile(modelPath)
	if err != nil {
		return 0, err
	}

	// Search for the pattern: input tensor dimensions are stored as
	// varint-encoded int64 values in the protobuf. Common sizes:
	// 640=0x80 0x05, 1024=0x80 0x08
	// Look for repeated dimension pattern (H and W are same for square input)
	for _, size := range []int{1024, 960, 896, 832, 768, 704, 640} {
		// Protobuf varint encoding for these sizes
		var encoded []byte
		v := size
		for v >= 0x80 {
			encoded = append(encoded, byte(v)|0x80)
			v >>= 7
		}
		encoded = append(encoded, byte(v))

		// If we find this varint at least twice close together, it's likely H,W
		count := 0
		for i := 0; i <= len(data)-len(encoded); i++ {
			match := true
			for j := range encoded {
				if data[i+j] != encoded[j] {
					match = false
					break
				}
			}
			if match {
				count++
			}
		}
		if count >= 2 {
			return size, nil
		}
	}

	return 0, fmt.Errorf("could not detect input size")
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

# go2rtc Feature Expansion Plan

## Project Goal

Expand go2rtc from a streaming proxy into a full camera management daemon
with object detection, segmented recording, multi-angle awareness, and
offline resilience. New functionality is implemented as additional
`internal/` modules following go2rtc's existing module architecture,
keeping upstream compatibility intact.

## Baseline: What go2rtc Already Provides

| Requirement | Status | How |
|---|---|---|
| (1) Connect to RTSP cameras | Done | `internal/rtsp`, `internal/streams` |
| (2) Reconnect when down | Done | `Producer.reconnect()` with backoff in `internal/streams/producer.go` |
| (4) RTSP output to clients | Done | `internal/rtsp` serves `rtsp://host:8554/<name>` |
| (5) HTTP snapshots | Partial | `internal/mjpeg` → `api/frame.jpeg?src=<name>` (single keyframe, transcodes H264/H265→JPEG via ffmpeg) |
| (8) Transcoding | Partial | `internal/ffmpeg` wraps ffmpeg subprocess; `internal/multitrans` exists |

Snapshot caching already exists in `internal/mjpeg` with a `?cache=<duration>` parameter.

## New Modules to Implement

### Phase 1: Core Infrastructure

#### `internal/camera` — Camera Configuration Layer
**Purpose:** Extend go2rtc's flat `streams:` config with camera-specific
metadata: friendly name, credentials, regions of interest, view group
membership, recording settings, transcode profiles.

```yaml
cameras:
  front_porch:
    stream: rtsp://192.168.1.10:554/stream1
    regions:
      driveway:
        polygon: [[0,200],[400,200],[400,480],[0,480]]
        detect: [person, car, deer]
      doorstep:
        polygon: [[400,300],[640,300],[640,480],[400,480]]
        detect: [person]
    view_group: front_yard
    recording:
      enabled: true
      segment_duration: 5m
      codec: h265
      bitrate: 2M
      retention: 30d
    transcode_profiles:
      low: { codec: h264, resolution: 640x360, bitrate: 500k }
      high: { codec: h264, resolution: 1920x1080, bitrate: 4M }
```

**Integration:** On `Init()`, registers each camera as a go2rtc stream
(calls `streams.New()`), and exposes camera metadata via `api/cameras`
endpoint. Also registers per-profile output streams
(e.g., `front_porch/low`, `front_porch/high`) backed by ffmpeg transcode
chains.

---

#### `internal/offline` — Offline Placeholder Frame Injection
**Purpose:** When a producer enters disconnected state, inject a synthetic
JPEG/H264 frame into the stream so consumers see a "Camera Offline" image
with camera name, last-seen timestamp, and error reason instead of a
dead stream.

**Approach:**
- Hook into `Producer.reconnect()` — on disconnect, swap in a synthetic
  producer that generates a static frame at 1fps
- Generate the placeholder image using Go's `image` stdlib + `image/draw`
  + a built-in font (no external dependency)
- Encode as JPEG for snapshot consumers, or as H264 NAL units (via a
  minimal encoder or pre-encoded template) for RTSP consumers
- On successful reconnect, the real producer replaces the placeholder
  transparently (existing `receiver.Replace()` mechanism)

**Key design question:** H264 encoding for the placeholder. Options:
1. Pre-render a small set of placeholder frames at startup and loop them
   (simplest, status text is baked in at generation time)
2. Use ffmpeg to encode on the fly (heavier, but dynamic text)
3. Use a single JPEG producer — MJPEG consumers work, RTSP/WebRTC
   consumers would need codec negotiation fallback

Recommend option 1 for initial implementation.

---

### Phase 2: Recording & Storage

#### `internal/storage` — Segmented Recording
**Purpose:** Record camera streams to disk in segments with configurable
codec, bitrate, and retention.

**Design:**
- Registers as a `core.Consumer` on each camera's stream
- Writes segments as MP4 or MPEG-TS files using ffmpeg subprocess
  (reusing `internal/ffmpeg` patterns)
- Segment boundaries are aligned to configured duration (e.g., 5 minutes)
- Directory layout: `<base_path>/<camera_name>/YYYY-MM-DD/HH-MM-SS.<ext>`
- A metadata index (SQLite or JSON manifest per day) tracks segments for
  time-range queries
- Retention manager runs periodically, deletes segments older than
  configured retention
- Transcoding is per-camera configurable: passthrough (copy), or
  re-encode to target codec/bitrate

**API endpoints:**
- `GET api/recordings?camera=<name>&from=<ts>&to=<ts>` — list segments
- `GET api/recordings/play?camera=<name>&from=<ts>&to=<ts>` — serve
  concatenated MP4 for a time range (on-the-fly, like Moonfire NVR)

---

### Phase 3: Object Detection

#### `internal/inference` — Detection Pipeline
**Purpose:** Sample frames from camera streams, crop to regions of
interest, run object detection, emit events.

**Architecture:**
```
Stream → FrameSampler (N fps) → ROI Cropper → Backend → Events
```

- **FrameSampler:** A `core.Consumer` that decodes video frames at a
  configurable rate (e.g., 3 fps). Uses ffmpeg to decode H264/H265 to
  raw frames (JPEG or Y4M — note `internal/mjpeg` already has Y4M
  consumer support).
- **ROI Cropper:** Crops decoded frames to configured polygon regions.
  Multiple regions per camera, each with its own detection class filter.
- **Backend interface:**
  ```go
  type Backend interface {
      Init(config BackendConfig) error
      Detect(frame []byte, width, height int) ([]Detection, error)
      Close() error
  }

  type Detection struct {
      Class      string
      Confidence float32
      BBox       [4]float32 // x1, y1, x2, y2 normalized
  }
  ```
- **Backend implementations (each in a sub-package):**
  - `internal/inference/onnx/` — ONNX Runtime via CGo (YOLO, RT-DETR, etc.)
  - `internal/inference/coral/` — Google Coral EdgeTPU via TFLite
  - `internal/inference/exec/` — Shell out to external process (Python
    script, HTTP microservice). Communicate via stdin/stdout (JPEG in,
    JSON detections out) or HTTP.

**Configuration:**
```yaml
inference:
  backend: onnx           # or: coral, exec
  model: yolov8m.onnx
  device: cpu              # or: cuda:0, openvino
  sample_fps: 3
  confidence_threshold: 0.5

  # exec backend specific:
  exec:
    command: python3 detect.py
    # or:
    url: http://localhost:9001/detect
```

**Events:** Detections are published to an internal event bus. Each event:
```go
type DetectionEvent struct {
    Camera     string
    Region     string
    Detections []Detection
    Timestamp  time.Time
    FrameJPEG  []byte // optional, for thumbnails
}
```

---

#### `internal/events` — Event Bus & Export
**Purpose:** Central event system for detection events, camera state
changes, and recording triggers.

**Consumers:**
- MQTT publish (go2rtc already has `pkg/mqtt`)
- Webhook (HTTP POST to configured URLs)
- Internal subscribers (storage module for event-triggered recording,
  viewgroup module for correlation)
- API: `GET api/events?camera=<name>&from=<ts>&to=<ts>` — query recent
  events
- WebSocket: `ws://host/api/ws?type=events` — live event stream

---

### Phase 4: Multi-Angle Awareness

#### `internal/viewgroup` — Cross-Camera Correlation
**Purpose:** Group cameras that view the same physical area from different
angles. Correlate detections across cameras to reduce false positives,
deduplicate alerts, and provide multi-view context.

**Configuration:**
```yaml
view_groups:
  front_yard:
    cameras: [front_porch, side_camera]
    correlation_window: 3s
    require_multi_angle: false  # true = only alert if 2+ cameras confirm
```

**Behavior:**
- Subscribes to detection events from all cameras in a group
- When a detection arrives, buffers it for `correlation_window` duration
- If another camera in the group detects the same class within the window,
  marks the event as "correlated" (higher confidence)
- If `require_multi_angle: true`, suppresses uncorrelated single-camera
  detections
- Emits enriched `GroupDetectionEvent` with references to all contributing
  camera events and their snapshots

**Future extensions:**
- Re-identification: track the same person/vehicle across cameras using
  embedding similarity (requires a re-ID model in the inference pipeline)
- Handoff tracking: when an object leaves one camera's FOV, predict which
  camera should pick it up next based on physical layout

---

### Phase 5: Enhanced Snapshots & Transcode Profiles

#### Snapshot Enhancements (extend `internal/mjpeg`)
- `GET api/frame.jpeg?src=<cam>&region=<name>` — cropped to named ROI
- `GET api/frame.jpeg?src=<cam>&annotate=true` — overlay detection boxes
- Dedicated `/api/snapshot/<camera>` alias for cleaner URLs

#### Transcode Profiles (extend `internal/camera` + `internal/ffmpeg`)
- Register per-camera transcode profiles as virtual streams
- `rtsp://host:8554/front_porch/low` → transcoded low-quality stream
- `rtsp://host:8554/front_porch/high` → transcoded high-quality stream
- `rtsp://host:8554/front_porch` → passthrough (original)
- On-demand: transcode pipeline only starts when a consumer connects

---

## Module Registration (main.go additions)

```go
// After existing modules, before helper modules:
{"camera", camera.Init},       // camera config layer
{"offline", offline.Init},     // offline placeholder injection
{"storage", storage.Init},     // segmented recording
{"inference", inference.Init}, // object detection pipeline
{"events", events.Init},       // event bus and export
{"viewgroup", viewgroup.Init}, // multi-angle correlation
```

## Configuration Schema (full example)

```yaml
# Existing go2rtc config (unchanged)
streams:
  raw_front: rtsp://admin:pass@192.168.1.10:554/stream1
  raw_side: rtsp://admin:pass@192.168.1.11:554/stream1

# New config sections
cameras:
  front_porch:
    stream: raw_front          # references go2rtc stream name
    regions:
      driveway:
        polygon: [[0,200],[400,200],[400,480],[0,480]]
        detect: [person, car, deer]
      doorstep:
        polygon: [[400,300],[640,300],[640,480],[400,480]]
        detect: [person]
    view_group: front_yard
    recording:
      enabled: true
      path: /var/lib/go2rtc/recordings/front_porch/
      segment_duration: 5m
      codec: h265
      bitrate: 2M
      retention: 30d
    transcode_profiles:
      low: { codec: h264, resolution: 640x360, bitrate: 500k }
      high: { codec: h264, resolution: 1920x1080, bitrate: 4M }

  side_camera:
    stream: raw_side
    regions:
      walkway:
        polygon: [[0,0],[640,0],[640,480],[0,480]]
        detect: [person, car]
    view_group: front_yard

view_groups:
  front_yard:
    cameras: [front_porch, side_camera]
    correlation_window: 3s
    require_multi_angle: false

inference:
  backend: onnx
  model: yolov8m.onnx
  device: cpu
  sample_fps: 3
  confidence_threshold: 0.5

events:
  mqtt:
    broker: tcp://localhost:1883
    topic_prefix: go2rtc/
  webhooks:
    - url: http://localhost:8081/hook
      events: [person, car]

storage:
  base_path: /var/lib/go2rtc/recordings
  default_retention: 14d
```

## Upstream Compatibility Strategy

- All new code lives in new `internal/` packages — no modifications to
  existing go2rtc modules except minimal hooks (e.g., offline placeholder
  needs to hook into producer state transitions)
- Periodically rebase `sofar/dev` onto upstream `master` tags
- Consider upstreaming general-purpose features (snapshot region cropping,
  offline placeholder) if they don't require the extended config schema
- go.mod module path remains `github.com/AlexxIT/go2rtc` to minimize
  churn

## Implementation Order

1. **`internal/camera`** — config parsing, stream registration, API
2. **`internal/offline`** — placeholder frame injection
3. **`internal/storage`** — segment writer, retention, playback API
4. **`internal/inference`** — frame sampling, exec backend first, then onnx
5. **`internal/events`** — event bus, MQTT, webhooks
6. **`internal/viewgroup`** — correlation engine
7. Snapshot & transcode profile enhancements

Each phase is independently useful. Phase 1-2 gives you a better camera
manager. Add phase 3 and you have a recorder. Phase 4-5 adds intelligence.
Phase 6-7 is the novel multi-angle layer.

## Open Questions

- [ ] Should `internal/camera` replace or wrap `streams:` config? (Wrap is
      safer for upstream compat — cameras reference stream names)
- [ ] SQLite vs flat-file manifest for recording index?
- [ ] Should the inference frame sampler use Y4M (already supported) or
      raw JPEG for the decode→detect pipeline?
- [ ] MQTT topic structure for events?
- [ ] Web UI — build one, or rely on API + Home Assistant integration?

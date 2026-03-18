# go2rtc Extended Configuration Reference

This document covers the configuration sections added by the camera
management modules on the `sofar/dev` branch. These are in addition to
go2rtc's existing configuration (streams, api, rtsp, ffmpeg, etc.).

All configuration is defined in `go2rtc.yaml` (or passed via `-c` flag).

---

## cameras

Defines cameras with metadata beyond what `streams:` provides. Each
camera is automatically registered as a go2rtc stream on startup.

### Shorthand form

```yaml
cameras:
  front_door: rtsp://admin:pass@192.168.1.10:554/stream1
```

This is equivalent to adding the URL under `streams:`. The camera will
have no regions, recording, or transcode profiles.

### Full form

```yaml
cameras:
  front_porch:
    # Required: stream source URL (any go2rtc-supported scheme)
    stream: rtsp://admin:pass@192.168.1.10:554/stream1

    # Optional: named regions of interest for detection filtering
    regions:
      driveway:
        # Polygon as array of [x,y] pixel coordinates (minimum 3 points)
        polygon: [[0,200],[400,200],[400,480],[0,480]]
        # Object classes to detect within this region
        detect: [person, car, deer]
      doorstep:
        polygon: [[400,300],[640,300],[640,480],[400,480]]
        detect: [person]

    # Optional: view group for multi-camera correlation
    view_group: front_yard

    # Optional: recording settings (requires storage section)
    recording:
      enabled: true              # default: false
      path: /custom/path/        # override storage.base_path for this camera
      segment_duration: 5m       # how often to rotate segment files
      retention: 30d             # override storage.default_retention

    # Optional: transcode profiles registered as virtual streams
    transcode_profiles:
      low:
        codec: h264                # video codec for transcoding
        resolution: 640x360        # WIDTHxHEIGHT (also accepts WIDTH:HEIGHT)
      high:
        codec: h264
        resolution: 1920x1080
```

### Transcode profiles

Each profile is registered as a separate go2rtc stream named
`<camera>/<profile>`. These are accessible via RTSP and all other
go2rtc output methods:

```
rtsp://host:8554/front_porch       → original stream (passthrough)
rtsp://host:8554/front_porch/low   → transcoded to 640x360 H264
rtsp://host:8554/front_porch/high  → transcoded to 1920x1080 H264
```

Transcode pipelines are on-demand — they only start when a consumer
connects, and stop when the last consumer disconnects.

### API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/cameras` | GET | List all cameras with metadata |
| `/api/cameras?name=X` | GET | Get single camera detail |

Credentials are masked in API responses.

---

## storage

Controls segmented recording to disk. Requires at least one camera
with `recording.enabled: true`.

```yaml
storage:
  # Required: base directory for all recordings
  base_path: /var/lib/go2rtc/recordings

  # Optional: delete segments older than this (default: no cleanup)
  # Supports: 5m, 1h, 7d, 30d, etc.
  default_retention: 14d
```

### How it works

- Each camera with recording enabled gets a `Recorder` consumer
  attached to its stream
- The recorder writes fragmented MP4 files using go2rtc's native
  muxer (no ffmpeg needed)
- Segment rotation happens on the next keyframe after `segment_duration`
  elapses
- Segments are stored as: `<base_path>/<camera>/YYYY-MM-DD/HH-MM-SS.mp4`
- Retention cleanup runs hourly, removing files older than the
  configured retention and pruning empty directories

### Per-camera overrides

The `recording.path` and `recording.retention` fields on individual
cameras override the global `storage.base_path` and
`storage.default_retention`.

### Defaults

| Field | Default |
|-------|---------|
| `segment_duration` | 5 minutes |
| `default_retention` | disabled (no cleanup) |

### API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/recordings?camera=X` | GET | List segments for a camera |
| `/api/recordings?camera=X&from=T&to=T` | GET | List segments in time range (RFC3339) |
| `/api/recordings/play?camera=X&ts=T` | GET | Serve a segment MP4 file |

---

## inference

Configures the object detection pipeline. Frames are periodically
sampled from each camera's stream, sent to a detection backend, and
results are published to the event bus.

```yaml
inference:
  # Detection backend URL (HTTP POST endpoint)
  # Accepts: JPEG body → returns JSON array of detections
  url: http://localhost:9001/detect

  # Optional: frames per second to sample (default: 3)
  sample_fps: 1

  # Optional: minimum confidence threshold 0.0-1.0 (default: 0.5)
  confidence_threshold: 0.3

  # Optional: HTTP request timeout (default: 10s)
  timeout: 10s
```

### HTTP backend protocol

The detection service receives a POST request with:
- `Content-Type: image/jpeg`
- Body: JPEG image data (640px wide, aspect ratio preserved)

Expected response: `200 OK` with JSON body:

```json
[
  {
    "class": "person",
    "confidence": 0.95,
    "bbox": [0.1, 0.2, 0.5, 0.8]
  }
]
```

Where `bbox` is `[x1, y1, x2, y2]` normalized to `0.0-1.0`.

### Compatible detection services

Any service implementing the above protocol works. Examples:
- Custom Python+YOLO (see `test/integration/detect_server.py`)
- CodeProject.AI
- Frigate HTTP API
- Any YOLO/ONNX wrapper with an HTTP interface

### Region filtering

If a camera has `regions` configured, detections are filtered by the
region's `detect` class list. Only detections matching a region's
class list are published for that region.

### Defaults

| Field | Default |
|-------|---------|
| `sample_fps` | 3 fps |
| `confidence_threshold` | 0.5 |
| `timeout` | 10 seconds |

---

## view_groups

Groups cameras viewing the same physical area for cross-camera
detection correlation.

```yaml
view_groups:
  front_yard:
    # Required: cameras in this group
    cameras: [front_porch, side_camera]

    # Optional: time window for correlation (default: 3s)
    correlation_window: 3s

    # Optional: suppress single-camera detections (default: false)
    require_multi_angle: false
```

### How correlation works

1. Camera A detects "person" → event buffered
2. Within `correlation_window`, Camera B also detects "person"
3. A `group_detection` event is emitted with `correlated: true` and
   both cameras listed

If `require_multi_angle: true`, single-camera detections are
suppressed entirely — only correlated (multi-camera) detections are
emitted. This reduces false positives at the cost of missing events
when only one camera has visibility.

### Defaults

| Field | Default |
|-------|---------|
| `correlation_window` | 3 seconds |
| `require_multi_angle` | false |

---

## Modules with no configuration

### offline

Automatically tracks online/offline state for all cameras registered
by the `cameras` section. No configuration needed.

**API:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/cameras/status` | GET | Online/offline state for all cameras |
| `/api/cameras/status?name=X` | GET | Status for single camera |
| `/api/cameras/snapshot?name=X` | GET | Placeholder JPEG if offline, redirect if online |

### events

In-process event bus with no configuration. Automatically stores the
last 1000 events in memory for querying.

**API:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/events` | GET | Query recent events |
| `/api/events?camera=X` | GET | Filter by camera |
| `/api/events?type=Y` | GET | Filter by type (`detection`, `state_change`, `group_detection`) |
| `/api/events?from=T&to=T` | GET | Filter by time range (RFC3339) |

**WebSocket:**

Send `{"type":"events"}` to subscribe to all events in real time.
Optionally filter: `{"type":"events","value":{"camera":"cam1","type":"detection"}}`.

### snapshot

Enhanced snapshot endpoints for cameras. No configuration needed.

**API:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/snapshot/<camera>` | GET | JPEG snapshot (clean URL) |
| `/api/snapshot/<camera>?region=X` | GET | Cropped to named region's bounding box |
| `/api/snapshot/<camera>?annotate=true` | GET | Overlay detection bounding boxes |

---

## Complete example

```yaml
# Standard go2rtc config (unchanged)
api:
  listen: ":1984"

rtsp:
  listen: ":8554"

# --- New sections below ---

cameras:
  front_porch:
    stream: rtsp://admin:pass@192.168.1.10:554/stream1
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
    transcode_profiles:
      low:
        codec: h264
        resolution: 640x360
      high:
        codec: h264
        resolution: 1920x1080

  side_camera:
    stream: rtsp://admin:pass@192.168.1.11:554/stream1
    regions:
      walkway:
        polygon: [[0,0],[640,0],[640,480],[0,480]]
        detect: [person, car]
    view_group: front_yard
    recording:
      enabled: true
      segment_duration: 5m

  # Shorthand — just a stream, no extras
  backyard: rtsp://admin:pass@192.168.1.12:554/stream1

storage:
  base_path: /var/lib/go2rtc/recordings
  default_retention: 14d

inference:
  url: http://localhost:9001/detect
  sample_fps: 1
  confidence_threshold: 0.4

view_groups:
  front_yard:
    cameras: [front_porch, side_camera]
    correlation_window: 3s
    require_multi_angle: false
```

### What this configuration produces

**Streams registered:**
- `front_porch` — original RTSP stream
- `front_porch/low` — transcoded 640x360 H264
- `front_porch/high` — transcoded 1920x1080 H264
- `side_camera` — original RTSP stream
- `backyard` — original RTSP stream

**Recording:**
- `front_porch` and `side_camera` recorded as 5-minute MP4 segments
- Stored under `/var/lib/go2rtc/recordings/<camera>/YYYY-MM-DD/`
- Segments older than 14 days automatically deleted

**Detection:**
- All 3 cameras sampled at 1 fps
- Frames sent to YOLO service at localhost:9001
- Detections filtered by region classes (person, car, deer for driveway)
- Results published to event bus

**Correlation:**
- `front_porch` and `side_camera` correlated in `front_yard` group
- Same-class detections within 3 seconds marked as correlated

**Offline tracking:**
- All 3 cameras monitored for disconnect/reconnect
- Placeholder JPEG served when a camera is offline

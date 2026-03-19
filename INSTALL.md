# Installation Guide

## Linux

### Prerequisites

**Build dependencies:**

```bash
# Fedora
sudo dnf install golang onnxruntime-devel ffmpeg

# Ubuntu/Debian
sudo apt install golang libonnxruntime-dev ffmpeg
```

Go 1.24+ is required. Check with `go version`.

**Optional — for GPU-accelerated transcoding:**

```bash
# Intel VAAPI (for Intel iGPU)
sudo dnf install intel-media-driver libva-utils    # Fedora
sudo apt install intel-media-va-driver vainfo      # Ubuntu

# Verify with: vainfo
```

**Optional — for object detection:**

A YOLOv8 ONNX model is needed for the inference backend. Export one
from ultralytics:

```bash
pip install ultralytics
python3 -c "
from ultralytics import YOLO
YOLO('yolov8s.pt').export(format='onnx', imgsz=640, simplify=True)
"
```

This produces `yolov8s.onnx` (~43MB). Copy it to `/var/lib/go2rtc/`
after installation.

### Building

```bash
git clone https://github.com/AlexxIT/go2rtc.git
cd go2rtc
git checkout sofar/dev
make build
```

The binary is built as `./go2rtc` in the project directory.

### Quick test (no install)

```bash
# Create a minimal config
cat > go2rtc.yaml << 'EOF'
api:
  listen: ":1984"
rtsp:
  listen: ":8554"
cameras:
  mycam:
    stream: rtsp://user:pass@192.168.1.10:554/stream1
EOF

./go2rtc
# Open http://localhost:1984/
```

### System installation

```bash
# Create service user
sudo useradd -r -s /sbin/nologin -d /var/lib/go2rtc go2rtc

# Install binary, service file, config, and data directories
sudo make install

# Edit the config
sudo nano /etc/go2rtc/go2rtc.yaml

# If using ONNX detection, copy the model
sudo cp yolov8s.onnx /var/lib/go2rtc/
sudo chown go2rtc:go2rtc /var/lib/go2rtc/yolov8s.onnx

# Start the service
sudo systemctl daemon-reload
sudo systemctl enable --now go2rtc

# Check status
sudo systemctl status go2rtc
sudo journalctl -u go2rtc -f
```

### Installed files

| Path | Purpose |
|------|---------|
| `/usr/local/bin/go2rtc` | Binary |
| `/usr/lib/systemd/system/go2rtc.service` | Systemd unit |
| `/etc/go2rtc/go2rtc.yaml` | Configuration |
| `/var/lib/go2rtc/` | Working directory |
| `/var/lib/go2rtc/recordings/` | Recorded segments |
| `/var/lib/go2rtc/sessions/` | Session snapshots |

### Recording storage

By default recordings go to `/var/lib/go2rtc/recordings/`. For a
dedicated disk or NAS mount, change `storage.base_path` in the config
and add the path to `ReadWritePaths=` in the service file:

```bash
sudo systemctl edit go2rtc
```

```ini
[Service]
ReadWritePaths=/media/cameras
```

Then set in `/etc/go2rtc/go2rtc.yaml`:

```yaml
storage:
  base_path: /media/cameras
  segment_duration: 10m
  default_retention: 14d
```

### Firewall

Open the required ports if you have a firewall enabled:

```bash
# Fedora/RHEL
sudo firewall-cmd --permanent --add-port=1984/tcp   # Web UI / API
sudo firewall-cmd --permanent --add-port=8554/tcp   # RTSP server
sudo firewall-cmd --permanent --add-port=8555/tcp   # WebRTC
sudo firewall-cmd --permanent --add-port=8555/udp   # WebRTC
sudo firewall-cmd --reload

# Ubuntu (ufw)
sudo ufw allow 1984/tcp
sudo ufw allow 8554/tcp
sudo ufw allow 8555/tcp
sudo ufw allow 8555/udp
```

### Updating

```bash
cd go2rtc
git pull
sudo make install
sudo systemctl restart go2rtc
```

### Uninstalling

```bash
sudo systemctl disable --now go2rtc
sudo make uninstall

# Optionally remove config and data
sudo rm -rf /etc/go2rtc /var/lib/go2rtc
sudo userdel go2rtc
```

### Running as your own user (development)

For development or single-user setups, skip the system installation
and run directly:

```bash
cd go2rtc
make build
./go2rtc
```

go2rtc looks for `go2rtc.yaml` in the current directory by default.

To run with a custom config:

```bash
./go2rtc -c /path/to/config.yaml
```

### Troubleshooting

**ONNX Runtime not found:**

```
onnxbe: init environment: Error loading ONNX shared library
```

Install the development package (`onnxruntime-devel` on Fedora,
`libonnxruntime-dev` on Ubuntu) or verify the library is in the
linker path:

```bash
ldconfig -p | grep onnxruntime
```

**No VAAPI encoding:**

```bash
vainfo    # should show encode entrypoints
```

If no encode entries appear, install the appropriate driver:
- Intel: `intel-media-driver`
- AMD: VAAPI encode requires `mesa-va-drivers-freeworld` (RPM Fusion)

**Camera connection issues:**

Check the camera is reachable:

```bash
ffprobe rtsp://user:pass@192.168.1.10:554/stream1
```

Common RTSP paths by manufacturer:
- Amcrest/Dahua: `/cam/realmonitor?channel=1&subtype=0`
- Hikvision: `/Streaming/Channels/101`
- Reolink: `/h264Preview_01_main`
- TP-Link Tapo: `/stream1`
- Generic ONVIF: use the web UI's "add" page for auto-discovery

**Service won't start — permission denied:**

Ensure the go2rtc user owns the data directory:

```bash
sudo chown -R go2rtc:go2rtc /var/lib/go2rtc
```

If recording to a mounted disk, add the path to `ReadWritePaths=`
in the service file (see Recording storage section above).

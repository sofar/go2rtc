#!/bin/bash
# Test inference pipeline end-to-end:
# 1. Start YOLO detection server
# 2. Start go2rtc with inference enabled
# 3. Wait for detections to appear in the events API
#
# Usage:
#   ./test/integration/run-inference-test.sh

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
source test/integration/env.sh

PORT=11984
DETECT_PORT=9001
DURATION=30

echo "=== Starting YOLO detection server on :${DETECT_PORT} ==="
python3 test/integration/detect_server.py &
DETECT_PID=$!
trap "kill $DETECT_PID 2>/dev/null; wait $DETECT_PID 2>/dev/null" EXIT

# Wait for detection server
for i in $(seq 1 30); do
  curl -s "http://localhost:${DETECT_PORT}/health" >/dev/null 2>&1 && break
  sleep 1
done
echo "Detection server ready."

echo ""
echo "=== Building and starting go2rtc ==="
go build -o /tmp/go2rtc .

/tmp/go2rtc -c "{
  \"api\":{\"listen\":\":${PORT}\"},
  \"rtsp\":{\"listen\":\":8554\"},
  \"cameras\":{
    \"${GO2RTC_CAM3_NAME}\":{
      \"stream\":\"${GO2RTC_CAM3_URL}\",
      \"regions\":{
        \"full_frame\":{
          \"polygon\":[[0,0],[640,0],[640,480],[0,480]],
          \"detect\":[\"person\",\"car\",\"dog\",\"cat\",\"bird\"]
        }
      }
    }
  },
  \"inference\":{
    \"url\":\"http://localhost:${DETECT_PORT}/detect\",
    \"sample_fps\":1,
    \"confidence_threshold\":0.3
  }
}" &
GO2RTC_PID=$!
trap "kill $GO2RTC_PID $DETECT_PID 2>/dev/null; wait $GO2RTC_PID $DETECT_PID 2>/dev/null" EXIT

# Wait for go2rtc API
for i in $(seq 1 10); do
  curl -s "http://localhost:${PORT}/api" >/dev/null 2>&1 && break
  sleep 0.5
done
echo "go2rtc ready."

echo ""
echo "=== Sampling for ${DURATION}s — point a camera at something! ==="
echo "    Watch live: http://localhost:${PORT}/"
echo "    RTSP:       rtsp://localhost:8554/${GO2RTC_CAM3_NAME}"
echo ""

sleep "$DURATION"

echo "=== Detection Events ==="
curl -s "http://localhost:${PORT}/api/events?type=detection" | python3 -m json.tool

echo ""
EVENTS=$(curl -s "http://localhost:${PORT}/api/events?type=detection" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))")
echo "=== Total detection events: ${EVENTS} ==="

kill $GO2RTC_PID $DETECT_PID 2>/dev/null
wait $GO2RTC_PID $DETECT_PID 2>/dev/null
trap - EXIT

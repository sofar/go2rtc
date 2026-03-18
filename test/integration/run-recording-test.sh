#!/bin/bash
# Quick recording test — records all 3 cameras for 30 seconds with
# 10-second segments, then validates the output.
#
# Usage:
#   ./test/integration/run-recording-test.sh

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
source test/integration/env.sh

RECDIR=$(mktemp -d)
DURATION=300
SEG_DUR="60s"
PORT=11984

echo "=== Building go2rtc ==="
go build -o /tmp/go2rtc .

echo "=== Recording to $RECDIR for ${DURATION}s ==="

/tmp/go2rtc -c "{
  \"api\":{\"listen\":\":${PORT}\"},
  \"rtsp\":{\"listen\":\":8554\"},
  \"cameras\":{
    \"${GO2RTC_CAM1_NAME}\":{\"stream\":\"${GO2RTC_CAM1_URL}\",\"recording\":{\"enabled\":true,\"segment_duration\":\"${SEG_DUR}\"}},
    \"${GO2RTC_CAM2_NAME}\":{\"stream\":\"${GO2RTC_CAM2_URL}\",\"recording\":{\"enabled\":true,\"segment_duration\":\"${SEG_DUR}\"}},
    \"${GO2RTC_CAM3_NAME}\":{\"stream\":\"${GO2RTC_CAM3_URL}\",\"recording\":{\"enabled\":true,\"segment_duration\":\"${SEG_DUR}\"}}
  },
  \"storage\":{\"base_path\":\"${RECDIR}\",\"default_retention\":\"1d\"}
}" &
PID=$!
trap "kill $PID 2>/dev/null; wait $PID 2>/dev/null" EXIT

# Wait for API to come up
for i in $(seq 1 10); do
  curl -s "http://localhost:${PORT}/api" >/dev/null 2>&1 && break
  sleep 0.5
done

echo "=== Cameras ==="
curl -s "http://localhost:${PORT}/api/cameras" | python3 -m json.tool

echo ""
echo "=== Waiting ${DURATION}s for segments to accumulate ==="
sleep "$DURATION"

echo ""
echo "=== Recordings API ==="
for cam in "$GO2RTC_CAM1_NAME" "$GO2RTC_CAM2_NAME" "$GO2RTC_CAM3_NAME"; do
  echo "--- $cam ---"
  curl -s "http://localhost:${PORT}/api/recordings?camera=${cam}" | python3 -m json.tool
done

echo ""
echo "=== Filesystem ==="
find "$RECDIR" -type f -name "*.mp4" | sort | while read f; do
  SIZE=$(stat --format='%s' "$f" 2>/dev/null || stat -f'%z' "$f" 2>/dev/null)
  echo "  $(echo "$SIZE" | awk '{printf "%.1fM", $1/1048576}')  $f"
done

echo ""
echo "=== Validating MP4 files with ffprobe ==="
FAIL=0
find "$RECDIR" -type f -name "*.mp4" | sort | while read f; do
  INFO=$(ffprobe -v error -show_entries stream=codec_name,width,height -of csv=p=0 "$f" 2>&1)
  if [ $? -eq 0 ] && [ -n "$INFO" ]; then
    echo "  OK   $f  ($INFO)"
  else
    echo "  FAIL $f"
    FAIL=1
  fi
done

echo ""
TOTAL=$(find "$RECDIR" -name "*.mp4" | wc -l)
TOTAL_SIZE=$(find "$RECDIR" -name "*.mp4" -exec stat --format='%s' {} \; 2>/dev/null | awk '{s+=$1} END {printf "%.1fM", s/1048576}')
echo "=== Summary: $TOTAL segments, ${TOTAL_SIZE} total ==="

# Cleanup
kill $PID 2>/dev/null; wait $PID 2>/dev/null
trap - EXIT

echo ""
read -p "Keep recordings in $RECDIR? [y/N] " keep
if [ "$keep" != "y" ] && [ "$keep" != "Y" ]; then
  rm -rf "$RECDIR"
  echo "Cleaned up."
else
  echo "Recordings kept at: $RECDIR"
fi

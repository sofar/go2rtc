#!/usr/bin/env python3
"""Minimal YOLO detection HTTP server for go2rtc inference module.

Accepts JPEG via POST, returns JSON array of detections.
Downloads yolov8n.pt (~6MB) on first run.

Usage:
    python3 test/integration/detect_server.py
    # Listens on http://0.0.0.0:9001/detect
"""

import io
import sys
from flask import Flask, request, jsonify
from ultralytics import YOLO
from PIL import Image

app = Flask(__name__)
model = None


def get_model():
    global model
    if model is None:
        print("Loading YOLOv8n model...", file=sys.stderr)
        model = YOLO("yolov8n.pt")
        print("Model loaded.", file=sys.stderr)
    return model


@app.route("/detect", methods=["POST"])
def detect():
    m = get_model()

    # Read JPEG from request body
    img = Image.open(io.BytesIO(request.data))
    results = m(img, verbose=False)[0]

    detections = []
    for box in results.boxes:
        x1, y1, x2, y2 = box.xyxyn[0].tolist()
        detections.append({
            "class": results.names[int(box.cls)],
            "confidence": round(float(box.conf), 4),
            "bbox": [round(x1, 4), round(y1, 4), round(x2, 4), round(y2, 4)],
        })

    return jsonify(detections)


@app.route("/health")
def health():
    return "ok"


if __name__ == "__main__":
    # Preload model
    get_model()
    app.run(host="0.0.0.0", port=9001)

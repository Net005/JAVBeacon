"""Render a 1200x1800 face-focused cover locally without changing Silo.
Use the original unpadded artwork, not an existing blurred poster.
Requires opencv-python-headless, NumPy and the official YuNet ONNX model.
"""
import argparse
import json
import sys
from pathlib import Path
import cv2
import numpy as np


def detect_faces(image, model):
    h, w = image.shape[:2]
    scale = min(1.0, 1200 / max(w, h))
    small = cv2.resize(image, (max(1, round(w * scale)), max(1, round(h * scale))))
    sh, sw = small.shape[:2]
    candidates = []
    for rotation in range(4):
        rotated = np.ascontiguousarray(np.rot90(small, -rotation))
        rh, rw = rotated.shape[:2]
        detector = cv2.FaceDetectorYN.create(str(model), "", (rw, rh), 0.85, 0.3, 5000)
        _, detected = detector.detect(rotated)
        if detected is None:
            continue
        for face in detected:
            x, y, fw, fh = face[:4]
            points = face[4:14].reshape(5, 2)
            if not np.all((points[:, 0] >= x) & (points[:, 0] <= x + fw) & (points[:, 1] >= y) & (points[:, 1] <= y + fh)):
                continue
            if rotation == 0:
                x0, y0, x1, y1 = x, y, x + fw, y + fh
            elif rotation == 1:
                x0, y0, x1, y1 = y, sh - x - fw, y + fh, sh - x
            elif rotation == 2:
                x0, y0, x1, y1 = sw - x - fw, sh - y - fh, sw - x, sh - y
            else:
                x0, y0, x1, y1 = sw - y - fh, x, sw - y, x + fw
            box = np.array([max(0, x0 / scale), max(0, y0 / scale), min(w, x1 / scale), min(h, y1 / scale)])
            if box[2] > box[0] and box[3] > box[1]:
                candidates.append((box, float(face[-1])))
    # Merge detections of the same face across orientations.
    faces = []
    for box, confidence in sorted(candidates, key=lambda f: f[1], reverse=True):
        area = (box[2] - box[0]) * (box[3] - box[1])
        duplicate = False
        for other, _ in faces:
            intersection = max(0, min(box[2], other[2]) - max(box[0], other[0])) * max(0, min(box[3], other[3]) - max(box[1], other[1]))
            other_area = (other[2] - other[0]) * (other[3] - other[1])
            if intersection / (area + other_area - intersection) > 0.3:
                duplicate = True
                break
        if not duplicate:
            faces.append((box, confidence))
    return faces


def crop_for_faces(width, height, faces):
    unit = min(width // 2, height // 3)
    if unit < 1:
        raise ValueError("Source too small for a 2:3 crop")
    cw, ch = unit * 2, unit * 3
    faces = sorted(faces, key=lambda f: (f[0][2]-f[0][0])*(f[0][3]-f[0][1])*f[1], reverse=True)

    def region(selected, margin):
        boxes = [f[0] for f in selected]
        return (max(0, min(b[0]-(b[2]-b[0])*margin for b in boxes)),
                max(0, min(b[1]-(b[3]-b[1])*margin for b in boxes)),
                min(width, max(b[2]+(b[2]-b[0])*margin for b in boxes)),
                min(height, max(b[3]+(b[3]-b[1])*margin for b in boxes)))

    if not faces:
        return [(width-cw)//2, (height-ch)//2, (width-cw)//2+cw, (height-ch)//2+ch], 0.0, "center-fallback"
    # A face anchors the composition; at least one third of the frame remains
    # outside its vertical extent. Prefer a smaller face with context over a
    # dominant close-up that cannot fit. Never cut a face just to force 2:3.
    contextual = [f for f in faces
                  if f[0][3]-f[0][1] <= ch*2/3
                  and (f[0][2]-f[0][0])*1.16 <= cw]
    if not contextual:
        return None, max(f[1] for f in faces), "unchanged-closeup"
    protected = region(faces, .08)
    mode = "all-faces"
    if protected[2]-protected[0] > cw or protected[3]-protected[1] > ch*2/3:
        faces = contextual[:1]
        protected = region(faces, .08)
        mode = "dominant-face"
        # Prefer full face over extra hair margin if the source is tight.
        if protected[2]-protected[0] > cw or protected[3]-protected[1] > ch:
            protected = region(faces, 0)
    left, top, right, bottom = protected
    cx = (left+right-cw)/2
    cy = (top+bottom)/2-ch*.42
    if right-left <= cw:
        cx = np.clip(cx, max(0, right-cw), min(width-cw, left))
    else:
        mode = "dominant-face-tight"
    if bottom-top <= ch:
        cy = np.clip(cy, max(0, bottom-ch), min(height-ch, top))
    else:
        mode = "dominant-face-tight"
    x = min(width-cw, max(0, round(float(cx))))
    y = min(height-ch, max(0, round(float(cy))))
    return [x, y, x+cw, y+ch], min(f[1] for f in faces), mode


def select_crop(image, model):
    h, w = image.shape[:2]
    return crop_for_faces(w, h, detect_faces(image, model))


def render_bytes(raw, model):
    image = cv2.imdecode(np.frombuffer(raw, dtype=np.uint8), cv2.IMREAD_COLOR)
    if image is None or image.shape[0]*image.shape[1] > 64000000:
        raise ValueError("Invalid or oversized image")
    rect, confidence, mode = select_crop(image, model)
    if rect is None:
        return b""
    x0,y0,x1,y1=rect
    result=cv2.resize(image[y0:y1,x0:x1],(1200,1800),interpolation=cv2.INTER_LANCZOS4)
    ok, encoded=cv2.imencode('.jpg',result,[cv2.IMWRITE_JPEG_QUALITY,94])
    if not ok:
        raise ValueError("Unable to encode crop")
    return encoded.tobytes()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path, nargs="?")
    parser.add_argument("output", type=Path, nargs="?")
    parser.add_argument("--model", required=True, type=Path)
    parser.add_argument('--stdio', action='store_true')
    args = parser.parse_args()
    cv2.setNumThreads(1)
    if args.stdio:
        raw=sys.stdin.buffer.read((16<<20)+1)
        if len(raw)>16<<20:
            parser.exit(2,"Image exceeds 16 MiB\n")
        sys.stdout.buffer.write(render_bytes(raw,args.model))
        return
    if args.source is None or args.output is None:
        parser.error("Source and output are required without --stdio")
    if args.source.resolve() == args.output.resolve():
        parser.error("Output must differ from source")
    image = cv2.imread(str(args.source))
    if image is None:
        parser.error("Unable to decode source")
    rect, confidence, mode = select_crop(image, args.model)
    if rect is None:
        print(json.dumps({"mode": mode, "changed": False}))
        return
    x0, y0, x1, y1 = rect
    output = cv2.resize(image[y0:y1, x0:x1], (1200, 1800), interpolation=cv2.INTER_LANCZOS4)
    if not cv2.imwrite(str(args.output), output):
        parser.exit(2, "Unable to write output\n")
    print(json.dumps({"crop": rect, "face_confidence": confidence, "mode": mode, "size": [1200, 1800]}))


if __name__ == "__main__":
    main()

# Local contextual cover renderer

YuNet detects faces in four orientations. Crop geometry prefers faces that fit with at least one third of frame height for surrounding scene detail; oversized close-ups are preserved rather than tightly cropped. Output is 1200×1800 JPEG. `--stdio` reads original image bytes and writes JPEG bytes; empty output signals insufficient context.

The Docker image uses Alpine 3.22 py3-opencv (OpenCV 4.11). Geometry tests also run in CI with opencv-python-headless 4.13.0.92. The bundled face_detection_yunet_2023mar.onnx model comes from the official OpenCV Zoo repository, under the included MIT license:
https://github.com/opencv/opencv_zoo/tree/main/models/face_detection_yunet

Model SHA256: 8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4

HTTP callers authenticate using the existing JAVBeacon API key. Rendering accepts at most 16 MiB / 30 MP and two concurrent processes with a 45-second deadline. No network calls or generative image service are used by the renderer.

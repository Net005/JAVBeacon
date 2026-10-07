package covers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	_ "golang.org/x/image/webp"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var ErrInsufficientContext = errors.New("original artwork is already a close-up; keep existing cover")

var sceneCropSlots = make(chan struct{}, 2)

// RenderSceneCrop uses the bundled local YuNet processor. The input must be
// original artwork; existing padded posters are never detector inputs.
func RenderSceneCrop(ctx context.Context, raw []byte) ([]byte, error) {
	if len(raw) > 16<<20 {
		return nil, fmt.Errorf("poster source exceeds 16 MiB")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 2 || cfg.Height < 3 || int64(cfg.Width)*int64(cfg.Height) > 64000000 {
		return nil, fmt.Errorf("invalid or oversized poster source")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	select {
	case sceneCropSlots <- struct{}{}:
		defer func() { <-sceneCropSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir := os.Getenv("JAVBEACON_CROPPER_DIR")
	if dir == "" {
		dir = "/app/cropper"
	}
	cmd := exec.CommandContext(ctx, "python3", filepath.Join(dir, "yunet_crop.py"), "--stdio", "--model", filepath.Join(dir, "face_detection_yunet_2023mar.onnx"))
	cmd.Stdin = bytes.NewReader(raw)
	var output bytes.Buffer
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("local poster renderer unavailable or failed: %w", err)
	}
	result := output.Bytes()
	if len(result) == 0 {
		return nil, ErrInsufficientContext
	}
	rendered, _, err := image.DecodeConfig(bytes.NewReader(result))
	if err != nil || rendered.Width != 1200 || rendered.Height != 1800 {
		return nil, fmt.Errorf("poster renderer returned invalid dimensions")
	}
	return result, nil
}

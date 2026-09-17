package c2

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
)

func TestScreenshotModuleName(t *testing.T) {
	m := &ScreenshotModule{}
	if m.Name() != "screenshot" {
		t.Fatalf("expected name 'screenshot', got %q", m.Name())
	}
}

func makeTestImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	return img
}

func TestEncodeJPEGSmallImageFits(t *testing.T) {
	img := makeTestImage(64, 64)

	data, err := encodeJPEG(img, 500000)
	if err != nil {
		t.Fatalf("encodeJPEG failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("encodeJPEG returned empty output")
	}

	// Output must be a valid JPEG that round-trips.
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not a valid JPEG: %v", err)
	}
	if decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 64 {
		t.Fatalf("decoded dimensions = %v, want 64x64", decoded.Bounds())
	}
}

func TestEncodeJPEGFallsBackQuality(t *testing.T) {
	img := makeTestImage(256, 256)

	// Full budget: should fit at the first quality tried (75).
	full, err := encodeJPEG(img, 500000)
	if err != nil {
		t.Fatalf("encodeJPEG failed: %v", err)
	}

	// Constrain maxSize below the quality-75 size but above quality-30 size.
	var q75Len, q30Len int
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	q75Len = buf.Len()
	buf.Reset()
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 30}); err != nil {
		t.Fatal(err)
	}
	q30Len = buf.Len()

	if q30Len >= q75Len {
		t.Skip("test image does not compress differently across qualities")
	}

	constrained, err := encodeJPEG(img, q75Len-1)
	if err != nil {
		t.Fatalf("encodeJPEG with constrained maxSize failed: %v", err)
	}
	if len(constrained) > q75Len-1 {
		t.Fatalf("output %d bytes exceeds maxSize %d", len(constrained), q75Len-1)
	}
	if len(constrained) >= len(full) {
		t.Logf("quality fallback produced smaller output: %d -> %d bytes", len(full), len(constrained))
	}
	if _, err := jpeg.Decode(bytes.NewReader(constrained)); err != nil {
		t.Fatalf("fallback output is not a valid JPEG: %v", err)
	}
}

func TestEncodeJPEGTooLarge(t *testing.T) {
	img := makeTestImage(64, 64)

	_, err := encodeJPEG(img, 10)
	if err == nil {
		t.Fatal("expected error for impossibly small maxSize, got nil")
	}
	if !strings.Contains(err.Error(), "Screenshot too large") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

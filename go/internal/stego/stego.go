// Package stego implements a robust cover-image steganography channel for
// Spotify playlist covers. Spotify may re-encode uploaded covers, and JPEG
// re-encoding destroys pixel-LSB data, so the payload is appended AFTER the
// JPEG EOI marker (FFD9). Decoders ignore trailing bytes, so the image still
// renders everywhere. If Spotify serves the image byte-identical the data
// survives; if they re-encode, extraction fails cleanly with ErrNoPayload
// and the caller falls back to the description channel.
package stego

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
)

// MaxCoverSize is Spotify's playlist cover upload limit (256KB).
const MaxCoverSize = 256 * 1024

// ErrNoPayload is returned by Extract when the payload marker is absent.
// This is the re-encode detection path: a re-encoded image loses the
// appended trailer entirely.
var ErrNoPayload = errors.New("stego: no payload marker found (image may have been re-encoded)")

var (
	// ErrNotJPEG is returned when the cover lacks a JPEG SOI (FFD8) or EOI (FFD9).
	ErrNotJPEG = errors.New("stego: cover is not a valid JPEG (missing SOI or EOI marker)")
	// ErrTooLarge is returned when the embedded result would exceed MaxCoverSize.
	ErrTooLarge = errors.New("stego: embedded cover exceeds Spotify cover size limit")
)

// magic marks the start of the payload trailer after the JPEG EOI.
var magic = []byte("SXFIL1")

var (
	jpegSOI = []byte{0xFF, 0xD8}
	jpegEOI = []byte{0xFF, 0xD9}
)

// Embed appends payload to coverJPEG after its EOI marker. The result is
// coverJPEG[:eoi+2] + MAGIC + uint32be(len(payload)) + payload. Anything
// after the first EOI in the input is dropped. It returns ErrNotJPEG if the
// cover is not a JPEG and ErrTooLarge if the result would exceed MaxCoverSize.
func Embed(coverJPEG, payload []byte) ([]byte, error) {
	if !bytes.HasPrefix(coverJPEG, jpegSOI) {
		return nil, ErrNotJPEG
	}
	eoi := bytes.LastIndex(coverJPEG, jpegEOI)
	if eoi < 0 {
		return nil, ErrNotJPEG
	}
	end := eoi + 2
	total := end + len(magic) + 4 + len(payload)
	if total > MaxCoverSize {
		return nil, fmt.Errorf("%w: %d bytes > %d", ErrTooLarge, total, MaxCoverSize)
	}
	out := make([]byte, 0, total)
	out = append(out, coverJPEG[:end]...)
	out = append(out, magic...)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	out = append(out, lenBuf[:]...)
	out = append(out, payload...)
	return out, nil
}

// Extract locates the LAST FFD9 in data, then requires MAGIC+length
// immediately after it. It returns ErrNoPayload when the marker is absent
// (the re-encode case) and a descriptive error when the trailer is present
// but truncated or corrupt.
func Extract(data []byte) ([]byte, error) {
	eoi := bytes.LastIndex(data, jpegEOI)
	if eoi < 0 {
		return nil, ErrNoPayload
	}
	rest := data[eoi+2:]
	if len(rest) < len(magic)+4 || !bytes.Equal(rest[:len(magic)], magic) {
		return nil, ErrNoPayload
	}
	n := binary.BigEndian.Uint32(rest[len(magic):])
	payload := rest[len(magic)+4:]
	if uint64(n) > uint64(len(payload)) {
		return nil, fmt.Errorf("stego: truncated payload: header declares %d bytes, only %d present", n, len(payload))
	}
	return payload[:n], nil
}

// GenerateCover produces a benign 640x640 JPEG cover with no external
// assets: a smooth vertical gradient between two seeded colors plus subtle
// seeded horizontal banding, encoded at JPEG quality 85. The output is
// deterministic for a given seed and well under MaxCoverSize.
func GenerateCover(seed int64) ([]byte, error) {
	const size = 640
	rng := rand.New(rand.NewSource(seed))

	c1 := color.NRGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 0xFF}
	c2 := color.NRGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 0xFF}
	// Subtle banding: a few low-amplitude sine bands across the height.
	bands := 2 + rng.Intn(5)
	amp := 3.0 + rng.Float64()*4.0
	phase := rng.Float64() * 2 * math.Pi

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		t := float64(y) / float64(size-1)
		band := amp * math.Sin(2*math.Pi*float64(bands)*t+phase)
		r := lerp(float64(c1.R), float64(c2.R), t) + band
		g := lerp(float64(c1.G), float64(c2.G), t) + band
		b := lerp(float64(c1.B), float64(c2.B), t) + band
		row := color.NRGBA{R: clamp(r), G: clamp(g), B: clamp(b), A: 0xFF}
		for x := 0; x < size; x++ {
			img.SetNRGBA(x, y, row)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("stego: encode cover: %w", err)
	}
	if buf.Len() >= MaxCoverSize {
		return nil, fmt.Errorf("%w: generated cover is %d bytes", ErrTooLarge, buf.Len())
	}
	return buf.Bytes(), nil
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func clamp(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

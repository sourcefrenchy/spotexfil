package stego

import (
	"bytes"
	"errors"
	"image/jpeg"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	cover, err := GenerateCover(42)
	if err != nil {
		t.Fatalf("GenerateCover: %v", err)
	}
	payload := []byte("covert channel payload \x00\x01\x02 binary-safe")
	embedded, err := Embed(cover, payload)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(embedded) > MaxCoverSize {
		t.Fatalf("embedded size %d exceeds MaxCoverSize", len(embedded))
	}
	got, err := Extract(embedded)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %q want %q", got, payload)
	}
}

func TestExtractPlainCoverReturnsErrNoPayload(t *testing.T) {
	cover, err := GenerateCover(7)
	if err != nil {
		t.Fatalf("GenerateCover: %v", err)
	}
	_, err = Extract(cover)
	if !errors.Is(err, ErrNoPayload) {
		t.Fatalf("expected ErrNoPayload, got %v", err)
	}
}

func TestEmbedOversizedPayload(t *testing.T) {
	cover, err := GenerateCover(1)
	if err != nil {
		t.Fatalf("GenerateCover: %v", err)
	}
	payload := make([]byte, MaxCoverSize) // alone already at the limit
	_, err = Embed(cover, payload)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestEmbedRejectsNonJPEG(t *testing.T) {
	if _, err := Embed([]byte("not a jpeg at all"), []byte("x")); !errors.Is(err, ErrNotJPEG) {
		t.Fatalf("expected ErrNotJPEG, got %v", err)
	}
	// SOI present but no EOI.
	if _, err := Embed([]byte{0xFF, 0xD8, 0x00, 0x01}, []byte("x")); !errors.Is(err, ErrNotJPEG) {
		t.Fatalf("expected ErrNotJPEG for missing EOI, got %v", err)
	}
}

func TestExtractTruncatedTrailer(t *testing.T) {
	cover, err := GenerateCover(3)
	if err != nil {
		t.Fatalf("GenerateCover: %v", err)
	}
	embedded, err := Embed(cover, []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// Chop the last 4 payload bytes: header declares more than is present.
	truncated := embedded[:len(embedded)-4]
	if _, err := Extract(truncated); err == nil {
		t.Fatal("expected error for truncated payload, got nil")
	}

	// Corrupt the length field to something huge.
	corrupt := bytes.Clone(embedded)
	eoi := bytes.LastIndex(corrupt, []byte{0xFF, 0xD9})
	lenOff := eoi + 2 + len(magic)
	corrupt[lenOff] = 0xFF
	corrupt[lenOff+1] = 0xFF
	corrupt[lenOff+2] = 0xFF
	corrupt[lenOff+3] = 0xFF
	if _, err := Extract(corrupt); err == nil {
		t.Fatal("expected error for corrupt length, got nil")
	}

	// Garbage after EOI with no magic.
	garbage := append(bytes.Clone(cover), []byte("GARBAGE!")...)
	if _, err := Extract(garbage); !errors.Is(err, ErrNoPayload) {
		t.Fatalf("expected ErrNoPayload for garbage trailer, got %v", err)
	}
}

func TestGenerateCoverProperties(t *testing.T) {
	cover, err := GenerateCover(99)
	if err != nil {
		t.Fatalf("GenerateCover: %v", err)
	}
	if cover[0] != 0xFF || cover[1] != 0xD8 {
		t.Fatal("cover does not start with JPEG SOI FFD8")
	}
	if _, err := jpeg.Decode(bytes.NewReader(cover)); err != nil {
		t.Fatalf("cover is not a decodable JPEG: %v", err)
	}
	if len(cover) >= MaxCoverSize {
		t.Fatalf("cover size %d not under MaxCoverSize", len(cover))
	}

	again, err := GenerateCover(99)
	if err != nil {
		t.Fatalf("GenerateCover (repeat): %v", err)
	}
	if !bytes.Equal(cover, again) {
		t.Fatal("GenerateCover not deterministic for the same seed")
	}

	other, err := GenerateCover(100)
	if err != nil {
		t.Fatalf("GenerateCover (other seed): %v", err)
	}
	if bytes.Equal(cover, other) {
		t.Fatal("different seeds produced identical covers")
	}
}

// TestExtractAfterReencode documents the re-encode failure mode: once the
// image is decoded and re-encoded (what Spotify may do server-side), the
// appended trailer is gone and Extract must report ErrNoPayload so the
// caller falls back to the description channel.
func TestExtractAfterReencode(t *testing.T) {
	cover, err := GenerateCover(5)
	if err != nil {
		t.Fatalf("GenerateCover: %v", err)
	}
	embedded, err := Embed(cover, []byte("secret"))
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(embedded))
	if err != nil {
		t.Fatalf("decode embedded: %v", err)
	}
	var reencoded bytes.Buffer
	if err := jpeg.Encode(&reencoded, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if _, err := Extract(reencoded.Bytes()); !errors.Is(err, ErrNoPayload) {
		t.Fatalf("expected ErrNoPayload after re-encode, got %v", err)
	}
}

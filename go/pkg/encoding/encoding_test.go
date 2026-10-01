package encoding

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompressDecompressRoundtrip(t *testing.T) {
	original := []byte("Hello, this is test data that should compress well! " +
		"Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
		"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.")

	compressed := Compress(original)
	if compressed[0] != 0x01 {
		t.Error("expected compressed flag")
	}

	decompressed, err := Decompress(compressed)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(decompressed) != string(original) {
		t.Errorf("roundtrip failed")
	}
}

func TestCompressSmallData(t *testing.T) {
	// Very small data may not benefit from compression
	original := []byte("Hi")
	compressed := Compress(original)
	decompressed, err := Decompress(compressed)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(decompressed) != string(original) {
		t.Errorf("roundtrip failed for small data")
	}
}

func TestEncodeDecodePayloadRoundtrip(t *testing.T) {
	// Create a temp file
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	content := []byte("This is a test file for spotexfil payload encoding.")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Encode with encryption
	key := "test-key-12345"
	encoded, err := EncodePayload(testFile, key, true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Decode
	decoded, err := DecodePayload(encoded, key)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if string(decoded) != string(content) {
		t.Errorf("roundtrip failed: got %q, want %q", string(decoded), string(content))
	}
}

func TestEncodeDecodePayloadNoCompression(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	content := []byte("Test file without compression.")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	key := "test-key-no-compress"
	encoded, err := EncodePayload(testFile, key, false)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodePayload(encoded, key)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if string(decoded) != string(content) {
		t.Errorf("roundtrip failed: got %q, want %q", string(decoded), string(content))
	}
}

func TestEncodeDecodePayloadLegacy(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	content := []byte("Legacy mode test - no encryption.")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Empty key = legacy mode
	encoded, err := EncodePayload(testFile, "", true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodePayload(encoded, "")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if string(decoded) != string(content) {
		t.Errorf("roundtrip failed: got %q, want %q", string(decoded), string(content))
	}
}

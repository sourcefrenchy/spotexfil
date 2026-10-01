package encoding

import (
	"crypto/rand"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func stressRandomPassword(t *testing.T) string {
	t.Helper()
	length, err := rand.Int(rand.Reader, big.NewInt(56))
	if err != nil {
		t.Fatalf("rand.Int: %v", err)
	}
	n := int(length.Int64()) + 8
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	for i := range buf {
		buf[i] = 33 + buf[i]%94
	}
	return string(buf)
}

func stressRandomBytes(t *testing.T, size int) []byte {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return buf
}

func writeTempFile(t *testing.T, dir string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, "stress_input.bin")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// TestStressEncodingRoundtrip runs 50 random file payloads through
// the full encode/decode pipeline.
func TestStressEncodingRoundtrip(t *testing.T) {
	sizes := []int{0, 1, 50, 500, 5000, 50_000}

	for i := 0; i < 50; i++ {
		size := sizes[i%len(sizes)]
		key := stressRandomPassword(t)
		data := stressRandomBytes(t, size)
		compress := i%2 == 0

		dir := t.TempDir()
		path := writeTempFile(t, dir, data)

		encoded, err := EncodePayload(path, key, compress)
		if err != nil {
			t.Fatalf("iter %d (size=%d): encode: %v", i, size, err)
		}

		decoded, err := DecodePayload(encoded, key)
		if err != nil {
			t.Fatalf("iter %d (size=%d): decode: %v", i, size, err)
		}

		if len(data) == 0 && len(decoded) == 0 {
			continue
		}
		if string(decoded) != string(data) {
			t.Errorf("iter %d (size=%d): roundtrip mismatch: got len %d, want len %d",
				i, size, len(decoded), len(data))
		}
	}
}

// TestStressCompressDecompress tests compression roundtrip with many sizes.
func TestStressCompressDecompress(t *testing.T) {
	for i := 0; i < 100; i++ {
		size := i * 100
		data := stressRandomBytes(t, size)

		compressed := Compress(data)
		decompressed, err := Decompress(compressed)
		if err != nil {
			t.Fatalf("iter %d (size=%d): decompress: %v", i, size, err)
		}

		if len(data) == 0 && len(decompressed) == 0 {
			continue
		}
		if string(decompressed) != string(data) {
			t.Errorf("iter %d (size=%d): compress roundtrip mismatch", i, size)
		}
	}
}

// TestStressConcurrentEncoding runs 20 goroutines encoding/decoding simultaneously.
func TestStressConcurrentEncoding(t *testing.T) {
	const numWorkers = 20
	var wg sync.WaitGroup
	errCh := make(chan string, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := stressRandomPassword(t)
			data := stressRandomBytes(t, 200+idx*50)

			dir := t.TempDir()
			path := writeTempFile(t, dir, data)

			encoded, err := EncodePayload(path, key, true)
			if err != nil {
				errCh <- "encode: " + err.Error()
				return
			}

			decoded, err := DecodePayload(encoded, key)
			if err != nil {
				errCh <- "decode: " + err.Error()
				return
			}

			if string(decoded) != string(data) {
				errCh <- "data mismatch"
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for errMsg := range errCh {
		t.Errorf("concurrent worker failed: %s", errMsg)
	}
}

// TestStressLegacyModeRoundtrip tests encode/decode without encryption.
func TestStressLegacyModeRoundtrip(t *testing.T) {
	for i := 0; i < 20; i++ {
		size := i * 500
		data := stressRandomBytes(t, size)

		dir := t.TempDir()
		path := writeTempFile(t, dir, data)

		encoded, err := EncodePayload(path, "", true)
		if err != nil {
			t.Fatalf("iter %d: encode: %v", i, err)
		}

		decoded, err := DecodePayload(encoded, "")
		if err != nil {
			t.Fatalf("iter %d: decode: %v", i, err)
		}

		if len(data) == 0 && len(decoded) == 0 {
			continue
		}
		if string(decoded) != string(data) {
			t.Errorf("iter %d: legacy roundtrip mismatch", i)
		}
	}
}

// Package encoding provides the full file exfil pipeline matching Python's
// Subcipher: read -> BLAKE2b -> gzip -> prepend hash -> AES-GCM -> base64 -> JSON.
package encoding

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"

	"github.com/sourcefrenchy/spotexfil/internal/crypto"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

var (
	flagCompressed = byte(shared.Proto.Compression.FlagCompressed)
	flagRaw        = byte(shared.Proto.Compression.FlagRaw)
)

// Compress applies gzip compression with a flag byte prefix.
// Returns FLAG_COMPRESSED + gzip'd data, or FLAG_RAW + original if no benefit.
func Compress(data []byte) []byte {
	var buf bytes.Buffer
	// Use BestCompression (level 9) to match Python's compresslevel=9
	w, err := gzip.NewWriterLevel(&buf, flate.BestCompression)
	if err != nil {
		// Fallback to raw
		return append([]byte{flagRaw}, data...)
	}
	w.Write(data)
	w.Close()

	compressed := buf.Bytes()
	if len(compressed) < len(data) {
		return append([]byte{flagCompressed}, compressed...)
	}
	return append([]byte{flagRaw}, data...)
}

// Decompress reverses Compress based on the flag byte prefix.
func Decompress(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	flag := data[0]
	payload := data[1:]
	if flag == flagCompressed {
		r, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	return payload, nil
}

// EncodePayload encodes a file into a transmittable payload string.
// Pipeline: read -> BLAKE2b -> compress -> prepend hash -> AES-GCM -> base64 -> JSON
func EncodePayload(inputFile, encryptionKey string, compress bool) (string, error) {
	plaintext, err := os.ReadFile(inputFile)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	checksum := crypto.ComputeBlake2b(plaintext)
	fmt.Printf("[*] checksum plaintext %s\n", checksum)
	fmt.Printf("[*] original size: %d bytes\n", len(plaintext))

	var data []byte
	if compress {
		data = Compress(plaintext)
		if data[0] == flagCompressed {
			saved := len(plaintext) - len(data) + 1
			pct := float64(0)
			if len(plaintext) > 0 {
				pct = float64(saved) / float64(len(plaintext)) * 100
			}
			fmt.Printf("[*] compressed: %d bytes (saved %.0f%%)\n", len(data), pct)
		} else {
			fmt.Println("[*] compression skipped (no benefit)")
		}
	} else {
		data = append([]byte{flagRaw}, plaintext...)
		fmt.Println("[*] compression disabled")
	}

	if encryptionKey != "" {
		// Prepend hash for integrity verification after decryption
		hashBytes := []byte(checksum)
		hashLen := make([]byte, 2)
		binary.BigEndian.PutUint16(hashLen, uint16(len(hashBytes)))
		dataToEncrypt := append(hashLen, hashBytes...)
		dataToEncrypt = append(dataToEncrypt, data...)
		data, err = crypto.Encrypt(dataToEncrypt, encryptionKey)
		if err != nil {
			return "", fmt.Errorf("encrypt: %w", err)
		}
		fmt.Println("[*] payload encrypted with AES-256-GCM")
	} else {
		fmt.Println("[!] WARNING: no encryption key set, payload is plaintext")
	}

	b64data := base64.StdEncoding.EncodeToString(data)
	jsonBytes, err := json.Marshal(b64data)
	if err != nil {
		return "", fmt.Errorf("json marshal: %w", err)
	}
	return string(jsonBytes), nil
}

// DecodePayload decodes a received payload back to original file bytes.
// Pipeline: unescape -> JSON -> base64 -> decrypt -> verify -> decompress
func DecodePayload(payload, encryptionKey string) ([]byte, error) {
	// HTML unescape (Spotify descriptions may have HTML entities)
	data := html.UnescapeString(payload)

	// JSON decode to get the base64 string
	var b64str string
	if err := json.Unmarshal([]byte(data), &b64str); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}

	rawBytes, err := base64.StdEncoding.DecodeString(b64str)
	if err != nil {
		// Try with padding correction
		rawBytes, err = decodeBase64Lenient(b64str)
		if err != nil {
			return nil, fmt.Errorf("base64 decode: %w", err)
		}
	}

	if encryptionKey != "" {
		decrypted, err := crypto.Decrypt(rawBytes, encryptionKey)
		if err != nil {
			return nil, fmt.Errorf("decryption failed (wrong key?): %w", err)
		}

		// Extract and verify integrity hash
		if len(decrypted) < 2 {
			return nil, fmt.Errorf("decrypted data too short")
		}
		hashLen := binary.BigEndian.Uint16(decrypted[:2])
		if int(hashLen)+2 > len(decrypted) {
			return nil, fmt.Errorf("invalid hash length")
		}
		storedHash := string(decrypted[2 : 2+hashLen])
		compressedData := decrypted[2+hashLen:]

		plaintext, err := Decompress(compressedData)
		if err != nil {
			return nil, fmt.Errorf("decompress: %w", err)
		}

		actualHash := crypto.ComputeBlake2b(plaintext)
		if storedHash != actualHash {
			return nil, fmt.Errorf("integrity check FAILED: expected %s, got %s", storedHash, actualHash)
		}
		fmt.Printf("[*] integrity verified: %s\n", actualHash)
		return plaintext, nil
	}

	// Legacy mode: decompress then hash
	plaintext, err := Decompress(rawBytes)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}
	checksum := crypto.ComputeBlake2b(plaintext)
	fmt.Printf("[*] checksum payload %s (not verified, no key)\n", checksum)
	return plaintext, nil
}

// decodeBase64Lenient decodes base64 with automatic padding correction,
// matching Python's behavior.
func decodeBase64Lenient(s string) ([]byte, error) {
	// Add padding if needed
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.StdEncoding.DecodeString(s)
}

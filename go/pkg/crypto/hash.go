package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/crypto/blake2b"
)

// ComputeBlake2b computes an unkeyed BLAKE2b hash with 20-byte digest
// and returns the hex-encoded string.
func ComputeBlake2b(data []byte) string {
	h, err := blake2b.New(20, nil)
	if err != nil {
		panic("blake2b.New failed: " + err.Error())
	}
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeHMACSHA256 computes HMAC-SHA256(key, message) and returns raw bytes.
func ComputeHMACSHA256(key []byte, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

// ComputeHMACSHA256Hex computes HMAC-SHA256 and returns the hex-encoded string.
func ComputeHMACSHA256Hex(key []byte, message []byte) string {
	return hex.EncodeToString(ComputeHMACSHA256(key, message))
}

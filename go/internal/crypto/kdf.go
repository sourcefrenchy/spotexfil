// Package crypto provides AES-256-GCM encryption, PBKDF2 key derivation,
// BLAKE2b hashing, and HMAC-SHA256 operations matching the Python implementation.
package crypto

import (
	"crypto/sha256"

	"github.com/sourcefrenchy/spotexfil/internal/shared"
	"golang.org/x/crypto/pbkdf2"
)

// DeriveKey derives a 256-bit AES key from a password using PBKDF2-SHA256.
func DeriveKey(password string, salt []byte) []byte {
	return pbkdf2.Key(
		[]byte(password),
		salt,
		shared.Proto.Crypto.KDFIterations,
		shared.Proto.Crypto.KeySize,
		sha256.New,
	)
}

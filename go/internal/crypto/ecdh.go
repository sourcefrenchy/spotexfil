package crypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"io"

	"github.com/sourcefrenchy/spotexfil/internal/shared"
	"golang.org/x/crypto/hkdf"
)

// GenerateX25519 generates an X25519 ECDH key pair.
func GenerateX25519() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// DeriveSessionKey derives a 32-byte session key from an ECDH shared secret
// and the master key using HKDF-SHA256. The info label is build-time
// injectable (see shared.HKDFLabel) so obfuscated builds don't share it.
func DeriveSessionKey(sharedSecret []byte, masterKey string) ([]byte, error) {
	r := hkdf.New(sha256.New, sharedSecret, []byte(masterKey), []byte(shared.HKDFLabel()))
	key := make([]byte, 32)
	_, err := io.ReadFull(r, key)
	return key, err
}

package crypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/hkdf"
)

// GenerateX25519 generates an X25519 ECDH key pair.
func GenerateX25519() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// DeriveSessionKey derives a 32-byte session key from an ECDH shared secret
// and the master key using HKDF-SHA256.
func DeriveSessionKey(sharedSecret []byte, masterKey string) ([]byte, error) {
	r := hkdf.New(sha256.New, sharedSecret, []byte(masterKey), []byte("spotexfil-session-v1"))
	key := make([]byte, 32)
	_, err := io.ReadFull(r, key)
	return key, err
}

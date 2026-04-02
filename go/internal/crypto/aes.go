package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"

	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

// Encrypt encrypts plaintext using AES-256-GCM with PBKDF2 key derivation.
// Output format: salt(16) || nonce(12) || ciphertext+tag(16)
func Encrypt(plaintext []byte, password string) ([]byte, error) {
	saltSize := shared.Proto.Crypto.SaltSize
	nonceSize := shared.Proto.Crypto.NonceSize

	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	key := DeriveKey(password, salt)
	return encryptWithKeyAndNonce(plaintext, key, salt, nonce)
}

// EncryptWithSaltNonce encrypts with explicit salt and nonce (for testing).
func EncryptWithSaltNonce(plaintext []byte, password string, salt, nonce []byte) ([]byte, error) {
	key := DeriveKey(password, salt)
	return encryptWithKeyAndNonce(plaintext, key, salt, nonce)
}

func encryptWithKeyAndNonce(plaintext, key, salt, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	// salt || nonce || ciphertext+tag
	result := make([]byte, 0, len(salt)+len(nonce)+len(ciphertext))
	result = append(result, salt...)
	result = append(result, nonce...)
	result = append(result, ciphertext...)
	return result, nil
}

// Decrypt decrypts AES-256-GCM encrypted data produced by Encrypt.
// Input format: salt(16) || nonce(12) || ciphertext+tag(16)
func Decrypt(data []byte, password string) ([]byte, error) {
	saltSize := shared.Proto.Crypto.SaltSize
	nonceSize := shared.Proto.Crypto.NonceSize
	minLen := saltSize + nonceSize + 16 // 16 = GCM tag

	if len(data) < minLen {
		return nil, fmt.Errorf("encrypted data too short: %d bytes (minimum %d)", len(data), minLen)
	}

	salt := data[:saltSize]
	nonce := data[saltSize : saltSize+nonceSize]
	ciphertext := data[saltSize+nonceSize:]

	key := DeriveKey(password, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}

// EncryptFast encrypts using a pre-derived AES key (no PBKDF2).
// Used for C2 metadata encryption with HMAC-derived keys.
// Output format: nonce(12) || ciphertext+tag(16)
func EncryptFast(plaintext, key []byte) ([]byte, error) {
	nonceSize := shared.Proto.Crypto.NonceSize

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	result := make([]byte, 0, len(nonce)+len(ciphertext))
	result = append(result, nonce...)
	result = append(result, ciphertext...)
	return result, nil
}

// DecryptFast decrypts data encrypted by EncryptFast.
// Input format: nonce(12) || ciphertext+tag(16)
func DecryptFast(data, key []byte) ([]byte, error) {
	nonceSize := shared.Proto.Crypto.NonceSize
	minLen := nonceSize + 16

	if len(data) < minLen {
		return nil, fmt.Errorf("encrypted data too short: %d bytes (minimum %d)", len(data), minLen)
	}

	nonce := data[:nonceSize]
	ciphertext := data[nonceSize:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}

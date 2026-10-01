// Package mythicmap implements Mythic C2's agent-message wire format so that
// both the server-side profile container and the agent can interoperate with
// Mythic. The underlying transport is a dumb pipe for these envelopes.
//
// Wire formats (Mythic 4.0):
//
//	Encrypted (AESPSK = "aes256_hmac"):
//	  EncBlob = IV(16) || CT || TAG(32)
//	    CT  = AES-256-CBC(key, IV, PKCS7-pad(plaintext, 16))
//	    TAG = HMAC-SHA256(key, IV || CT)
//	  envelope = base64( UUID(36 ASCII) || EncBlob )
//
//	Plaintext (AESPSK = "none"):
//	  envelope = base64( UUID(36 ASCII) || plaintext )
//
//	EKE staging ("staging_rsa"):
//	  4096-bit RSA keypair; staging message sent through the encrypted path;
//	  Mythic replies with session_key = base64(RSA-OAEP-SHA1(pub, newKey)).
package mythicmap

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
)

const (
	// KeySize is the required AES-256 key length in bytes.
	KeySize = 32
	// uuidLen is the length of a Mythic UUID in ASCII form.
	uuidLen = 36
	// ivLen is the AES-CBC IV length.
	ivLen = aes.BlockSize
	// tagLen is the HMAC-SHA256 tag length.
	tagLen = sha256.Size
	// stagingKeyBits is the RSA key size Mythic uses for staging_rsa.
	stagingKeyBits = 4096
	// sessionIDLen is the length of the random staging session id.
	sessionIDLen = 20
)

// DecodeKey decodes a base64-encoded AESPSK key and validates its length.
// Keys must be exactly 32 bytes (AES-256).
func DecodeKey(b64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("mythicmap: decode key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("mythicmap: key must be exactly %d bytes, got %d", KeySize, len(key))
	}
	return key, nil
}

// Encrypt builds a Mythic wire envelope for plaintext under the given UUID.
// A nil or empty key selects plaintext mode (AESPSK = "none"); otherwise the
// key must be exactly 32 bytes and the encrypted format is used.
func Encrypt(uuid string, key, plaintext []byte) (string, error) {
	if len(uuid) != uuidLen {
		return "", fmt.Errorf("mythicmap: uuid must be %d ASCII bytes, got %d", uuidLen, len(uuid))
	}
	if len(key) == 0 {
		// Plaintext mode.
		return base64.StdEncoding.EncodeToString(append([]byte(uuid), plaintext...)), nil
	}
	if len(key) != KeySize {
		return "", fmt.Errorf("mythicmap: key must be exactly %d bytes, got %d", KeySize, len(key))
	}
	iv := make([]byte, ivLen)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("mythicmap: generate IV: %w", err)
	}
	return encryptWithIV(uuid, key, iv, plaintext)
}

// encryptWithIV is Encrypt with a caller-supplied IV. It is used by tests to
// reproduce fixed-IV interop vectors.
func encryptWithIV(uuid string, key, iv, plaintext []byte) (string, error) {
	if len(iv) != ivLen {
		return "", fmt.Errorf("mythicmap: IV must be %d bytes, got %d", ivLen, len(iv))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("mythicmap: init AES: %w", err)
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

	blob := make([]byte, 0, ivLen+len(ct)+tagLen)
	blob = append(blob, iv...)
	blob = append(blob, ct...)
	mac := hmac.New(sha256.New, key)
	mac.Write(blob)
	blob = mac.Sum(blob)

	env := make([]byte, 0, uuidLen+len(blob))
	env = append(env, uuid...)
	env = append(env, blob...)
	return base64.StdEncoding.EncodeToString(env), nil
}

// Decrypt parses a Mythic wire envelope, returning the cleartext UUID and
// plaintext. With a non-empty key it verifies the HMAC-SHA256 tag in constant
// time before decrypting; a nil or empty key selects plaintext mode.
func Decrypt(key []byte, envelopeB64 string) (uuid string, plaintext []byte, err error) {
	env, err := base64.StdEncoding.DecodeString(envelopeB64)
	if err != nil {
		return "", nil, fmt.Errorf("mythicmap: decode envelope: %w", err)
	}
	if len(env) < uuidLen {
		return "", nil, fmt.Errorf("mythicmap: envelope too short: %d bytes", len(env))
	}
	uuid = string(env[:uuidLen])
	body := env[uuidLen:]

	if len(key) == 0 {
		return uuid, body, nil
	}
	if len(key) != KeySize {
		return "", nil, fmt.Errorf("mythicmap: key must be exactly %d bytes, got %d", KeySize, len(key))
	}
	if len(body) < ivLen+tagLen+aes.BlockSize || (len(body)-ivLen-tagLen)%aes.BlockSize != 0 {
		return "", nil, fmt.Errorf("mythicmap: malformed encrypted blob: %d bytes", len(body))
	}

	tag := body[len(body)-tagLen:]
	ivct := body[:len(body)-tagLen]
	mac := hmac.New(sha256.New, key)
	mac.Write(ivct)
	if !hmac.Equal(mac.Sum(nil), tag) {
		return "", nil, errors.New("mythicmap: HMAC verification failed")
	}

	iv, ct := ivct[:ivLen], ivct[ivLen:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", nil, fmt.Errorf("mythicmap: init AES: %w", err)
	}
	padded := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(padded, ct)

	plaintext, err = pkcs7Unpad(padded, aes.BlockSize)
	if err != nil {
		return "", nil, fmt.Errorf("mythicmap: %w", err)
	}
	return uuid, plaintext, nil
}

// ExtractUUID returns the cleartext UUID prefix of an envelope without
// decrypting it, for server-side correlation.
func ExtractUUID(envelopeB64 string) (string, error) {
	env, err := base64.StdEncoding.DecodeString(envelopeB64)
	if err != nil {
		return "", fmt.Errorf("mythicmap: decode envelope: %w", err)
	}
	if len(env) < uuidLen {
		return "", fmt.Errorf("mythicmap: envelope too short for UUID: %d bytes", len(env))
	}
	return string(env[:uuidLen]), nil
}

// GenerateStagingKeypair creates the 4096-bit RSA keypair used for the
// staging_rsa EKE exchange and returns the private key, the staging message
// JSON (to be sent through the normal encrypted path), and the session ID.
func GenerateStagingKeypair() (priv *rsa.PrivateKey, stagingMsgJSON []byte, sessionID string, err error) {
	priv, err = rsa.GenerateKey(rand.Reader, stagingKeyBits)
	if err != nil {
		return nil, nil, "", fmt.Errorf("mythicmap: generate RSA key: %w", err)
	}
	der := x509.MarshalPKCS1PublicKey(&priv.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: der})

	sessionID, err = randomAlphanumeric(sessionIDLen)
	if err != nil {
		return nil, nil, "", fmt.Errorf("mythicmap: generate session id: %w", err)
	}
	msg, err := json.Marshal(map[string]string{
		"action":     "staging_rsa",
		"pub_key":    base64.StdEncoding.EncodeToString(pubPEM),
		"session_id": sessionID,
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("mythicmap: marshal staging message: %w", err)
	}
	return priv, msg, sessionID, nil
}

// DecryptSessionKey decrypts the base64( RSA-OAEP-SHA1(pub, newKey) ) session
// key from Mythic's staging_rsa response and returns the first 32 bytes, the
// new AES-256 session key.
func DecryptSessionKey(priv *rsa.PrivateKey, b64SessionKey string) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("mythicmap: nil private key")
	}
	enc, err := base64.StdEncoding.DecodeString(b64SessionKey)
	if err != nil {
		return nil, fmt.Errorf("mythicmap: decode session key: %w", err)
	}
	dec, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, priv, enc, nil)
	if err != nil {
		return nil, fmt.Errorf("mythicmap: RSA-OAEP decrypt session key: %w", err)
	}
	if len(dec) < KeySize {
		return nil, fmt.Errorf("mythicmap: decrypted session key too short: %d bytes", len(dec))
	}
	return dec[:KeySize], nil
}

func randomAlphanumeric(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	r := make([]byte, n)
	if _, err := rand.Read(r); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(r[i])%len(alphabet)]
	}
	return string(b), nil
}

// pkcs7Pad pads data to a multiple of blockSize per PKCS#7.
func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - (len(data) % blockSize)
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

// pkcs7Unpad removes and strictly validates PKCS#7 padding: data must be
// non-empty, a multiple of blockSize, the padding length in [1, blockSize],
// and every padding byte must equal the padding length.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("pkcs7: empty data")
	}
	if blockSize <= 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("pkcs7: data length %d not a multiple of block size %d", len(data), blockSize)
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, fmt.Errorf("pkcs7: invalid padding length %d", pad)
	}
	if !bytes.Equal(data[len(data)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("pkcs7: corrupted padding bytes")
	}
	return data[:len(data)-pad], nil
}

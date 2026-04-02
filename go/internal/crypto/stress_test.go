package crypto

import (
	"crypto/rand"
	"math/big"
	"sync"
	"testing"
)

func randomPassword(t *testing.T) string {
	t.Helper()
	length, err := rand.Int(rand.Reader, big.NewInt(56))
	if err != nil {
		t.Fatalf("rand.Int: %v", err)
	}
	n := int(length.Int64()) + 8 // 8..63
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	// Convert to printable ASCII
	for i := range buf {
		buf[i] = 33 + buf[i]%94 // '!' .. '~'
	}
	return string(buf)
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return buf
}

// TestStressCryptoRoundtrip encrypts and decrypts 100 random payloads
// with random keys and varying data sizes.
func TestStressCryptoRoundtrip(t *testing.T) {
	sizes := []int{0, 1, 100, 10_000, 100_000}
	iteration := 0
	for _, size := range sizes {
		for i := 0; i < 20; i++ {
			iteration++
			password := randomPassword(t)
			plaintext := randomBytes(t, size)

			encrypted, err := Encrypt(plaintext, password)
			if err != nil {
				t.Fatalf("iter %d (size=%d): encrypt: %v", iteration, size, err)
			}

			decrypted, err := Decrypt(encrypted, password)
			if err != nil {
				t.Fatalf("iter %d (size=%d): decrypt: %v", iteration, size, err)
			}

			if len(plaintext) == 0 && len(decrypted) == 0 {
				continue // both empty is fine
			}
			if !bytesEqual(decrypted, plaintext) {
				t.Errorf("iter %d (size=%d): roundtrip mismatch", iteration, size)
			}
		}
	}
}

// TestStressKDFDeterminism verifies that DeriveKey is deterministic:
// same password + salt always yields the same key (100 iterations).
func TestStressKDFDeterminism(t *testing.T) {
	for i := 0; i < 100; i++ {
		password := randomPassword(t)
		salt := randomBytes(t, 16)

		key1 := DeriveKey(password, salt)
		key2 := DeriveKey(password, salt)

		if !bytesEqual(key1, key2) {
			t.Errorf("iter %d: KDF not deterministic for password=%q", i, password)
		}
	}
}

// TestStressFastCryptoRoundtrip tests EncryptFast/DecryptFast with 100 random payloads.
func TestStressFastCryptoRoundtrip(t *testing.T) {
	for i := 0; i < 100; i++ {
		key := ComputeHMACSHA256([]byte(randomPassword(t)), []byte("stress-test-label"))
		size := []int{0, 1, 50, 500, 5000}[i%5]
		plaintext := randomBytes(t, size)

		encrypted, err := EncryptFast(plaintext, key)
		if err != nil {
			t.Fatalf("iter %d: encrypt fast: %v", i, err)
		}

		decrypted, err := DecryptFast(encrypted, key)
		if err != nil {
			t.Fatalf("iter %d: decrypt fast: %v", i, err)
		}

		if len(plaintext) == 0 && len(decrypted) == 0 {
			continue
		}
		if !bytesEqual(decrypted, plaintext) {
			t.Errorf("iter %d: fast roundtrip mismatch", i)
		}
	}
}

// TestStressConcurrentCrypto runs 20 goroutines encoding/decoding simultaneously.
func TestStressConcurrentCrypto(t *testing.T) {
	const numWorkers = 20
	var wg sync.WaitGroup
	errCh := make(chan string, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			password := randomPassword(t)
			plaintext := randomBytes(t, 500+idx*100)

			encrypted, err := Encrypt(plaintext, password)
			if err != nil {
				errCh <- "encrypt: " + err.Error()
				return
			}

			decrypted, err := Decrypt(encrypted, password)
			if err != nil {
				errCh <- "decrypt: " + err.Error()
				return
			}

			if !bytesEqual(decrypted, plaintext) {
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

// TestStressBlake2bConsistency verifies BLAKE2b hash consistency over many inputs.
func TestStressBlake2bConsistency(t *testing.T) {
	for i := 0; i < 100; i++ {
		data := randomBytes(t, i*100)
		h1 := ComputeBlake2b(data)
		h2 := ComputeBlake2b(data)
		if h1 != h2 {
			t.Errorf("iter %d: blake2b not deterministic", i)
		}
		if len(h1) != 40 { // 20 bytes = 40 hex chars
			t.Errorf("iter %d: blake2b hash length %d, want 40", i, len(h1))
		}
	}
}

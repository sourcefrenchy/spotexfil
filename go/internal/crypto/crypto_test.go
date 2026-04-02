package crypto

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testVectorPath(name string) string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "..", "shared", "test_vectors", name)
}

// --- KDF vectors ---

type kdfVector struct {
	Password string `json:"password"`
	Salt     string `json:"salt"`
	Key      string `json:"key"`
}

func TestDeriveKey(t *testing.T) {
	data, err := os.ReadFile(testVectorPath("kdf_vectors.json"))
	if err != nil {
		t.Fatalf("read kdf_vectors.json: %v", err)
	}
	var vectors []kdfVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("parse kdf_vectors.json: %v", err)
	}

	for i, v := range vectors {
		salt, _ := hex.DecodeString(v.Salt)
		key := DeriveKey(v.Password, salt)
		got := hex.EncodeToString(key)
		if got != v.Key {
			t.Errorf("vector %d: got %s, want %s", i, got, v.Key)
		}
	}
}

// --- BLAKE2b vectors ---

type blake2bVector struct {
	Input      string `json:"input"`
	DigestSize int    `json:"digest_size"`
	Hash       string `json:"hash"`
}

func TestComputeBlake2b(t *testing.T) {
	data, err := os.ReadFile(testVectorPath("blake2b_vectors.json"))
	if err != nil {
		t.Fatalf("read blake2b_vectors.json: %v", err)
	}
	var vectors []blake2bVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("parse blake2b_vectors.json: %v", err)
	}

	for i, v := range vectors {
		input, _ := hex.DecodeString(v.Input)
		got := ComputeBlake2b(input)
		if got != v.Hash {
			t.Errorf("vector %d: got %s, want %s", i, got, v.Hash)
		}
	}
}

// --- Encrypt vectors ---

type encryptVector struct {
	Password   string `json:"password"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Plaintext  string `json:"plaintext"`
	Ciphertext string `json:"ciphertext"`
	Key        string `json:"key"`
}

func TestEncryptDecrypt(t *testing.T) {
	data, err := os.ReadFile(testVectorPath("encrypt_vectors.json"))
	if err != nil {
		t.Fatalf("read encrypt_vectors.json: %v", err)
	}
	var vectors []encryptVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("parse encrypt_vectors.json: %v", err)
	}

	for i, v := range vectors {
		salt, _ := hex.DecodeString(v.Salt)
		nonce, _ := hex.DecodeString(v.Nonce)

		// Test encryption with known salt/nonce
		encrypted, err := EncryptWithSaltNonce([]byte(v.Plaintext), v.Password, salt, nonce)
		if err != nil {
			t.Errorf("vector %d: encrypt error: %v", i, err)
			continue
		}

		// Extract the ciphertext portion (after salt+nonce)
		gotCT := encrypted[len(salt)+len(nonce):]

		// The vector ciphertext is base64-encoded
		wantCT, _ := base64.StdEncoding.DecodeString(v.Ciphertext)
		if !bytesEqual(gotCT, wantCT) {
			t.Errorf("vector %d: ciphertext mismatch\n  got:  %x\n  want: %x", i, gotCT, wantCT)
		}

		// Test decryption roundtrip
		decrypted, err := Decrypt(encrypted, v.Password)
		if err != nil {
			t.Errorf("vector %d: decrypt error: %v", i, err)
			continue
		}
		if string(decrypted) != v.Plaintext {
			t.Errorf("vector %d: decrypted %q, want %q", i, string(decrypted), v.Plaintext)
		}
	}
}

// --- HMAC vectors ---

type hmacVector struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	FullHex     string `json:"full_hex"`
	Truncated   string `json:"truncated"`
	TruncateLen int    `json:"truncate_len"`
}

func TestHMACSHA256(t *testing.T) {
	data, err := os.ReadFile(testVectorPath("hmac_vectors.json"))
	if err != nil {
		t.Fatalf("read hmac_vectors.json: %v", err)
	}
	var vectors []hmacVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("parse hmac_vectors.json: %v", err)
	}

	for i, v := range vectors {
		fullHex := ComputeHMACSHA256Hex([]byte(v.Key), []byte(v.Label))
		if fullHex != v.FullHex {
			t.Errorf("vector %d: full_hex got %s, want %s", i, fullHex, v.FullHex)
		}
		truncated := fullHex[:v.TruncateLen]
		if truncated != v.Truncated {
			t.Errorf("vector %d: truncated got %s, want %s", i, truncated, v.Truncated)
		}
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	password := "test-roundtrip-key"
	plaintext := []byte("Hello, this is a roundtrip test!")

	encrypted, err := Encrypt(plaintext, password)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := Decrypt(encrypted, password)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("roundtrip failed: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestFastEncryptDecryptRoundtrip(t *testing.T) {
	key := ComputeHMACSHA256([]byte("test-key"), []byte("spotexfil-c2-meta-key"))
	plaintext := []byte(`{"m":{"c":"cmd","i":1,"seq":1},"d":"test"}`)

	encrypted, err := EncryptFast(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := DecryptFast(encrypted, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("roundtrip failed: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

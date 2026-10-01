package mythicmap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

const testUUID = "b50a5fe8-099d-4611-a2ac-96d93e6ec77b"

func testKey(t *testing.T) []byte {
	t.Helper()
	key, err := DecodeKey("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=") // bytes 00..1f
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	return key
}

func TestRoundtripEncrypted(t *testing.T) {
	key := testKey(t)
	pt := []byte(`{"action":"get_tasking","tasking_size":1}`)

	env, err := Encrypt(testUUID, key, pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	uuid, got, err := Decrypt(key, env)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if uuid != testUUID {
		t.Errorf("uuid = %q, want %q", uuid, testUUID)
	}
	if string(got) != string(pt) {
		t.Errorf("plaintext = %q, want %q", got, pt)
	}
}

func TestRoundtripPlaintext(t *testing.T) {
	pt := []byte(`{"action":"checkin","ip":"10.0.0.5"}`)
	env, err := Encrypt(testUUID, nil, pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	uuid, got, err := Decrypt(nil, env)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if uuid != testUUID || string(got) != string(pt) {
		t.Errorf("got (%q, %q), want (%q, %q)", uuid, got, testUUID, pt)
	}
}

func TestTamperCiphertextFails(t *testing.T) {
	key := testKey(t)
	env, err := Encrypt(testUUID, key, []byte("tamper me if you can"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(env)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Flip a byte inside the CT region (after UUID + IV, before the tag).
	raw[uuidLen+ivLen+2] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, _, err := Decrypt(key, tampered); err == nil {
		t.Fatal("Decrypt of tampered envelope succeeded, want HMAC failure")
	}
}

func TestWrongKeyFails(t *testing.T) {
	key := testKey(t)
	env, err := Encrypt(testUUID, key, []byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	wrong := make([]byte, KeySize)
	if _, err := rand.Read(wrong); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Decrypt(wrong, env); err == nil {
		t.Fatal("Decrypt with wrong key succeeded, want failure")
	}
}

func TestDecodeKeyRejectsShortKey(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := DecodeKey(short); err == nil {
		t.Fatal("DecodeKey accepted 16-byte key, want error")
	} else if !strings.Contains(err.Error(), "32") {
		t.Errorf("error %q does not state the required length", err)
	}
}

func TestExtractUUIDWithoutKey(t *testing.T) {
	// Envelope built under one key; extraction must need no key at all.
	key, err := DecodeKey("Hx4dHBsaGRgXFhUUExIREA8ODQwLCgkIBwYFBAMCAQA=") // bytes 1f..00
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	env, err := Encrypt(testUUID, key, []byte("payload"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	uuid, err := ExtractUUID(env)
	if err != nil {
		t.Fatalf("ExtractUUID: %v", err)
	}
	if uuid != testUUID {
		t.Errorf("ExtractUUID = %q, want %q", uuid, testUUID)
	}
}

func TestPKCS7Roundtrip(t *testing.T) {
	for n := 1; n <= 64; n++ { // includes exact block multiples 16/32/48/64
		data := make([]byte, n)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		padded := pkcs7Pad(data, 16)
		if len(padded)%16 != 0 || len(padded) < n+1 || len(padded) > n+16 {
			t.Fatalf("n=%d: padded length %d invalid", n, len(padded))
		}
		got, err := pkcs7Unpad(padded, 16)
		if err != nil {
			t.Fatalf("n=%d: unpad: %v", n, err)
		}
		if string(got) != string(data) {
			t.Fatalf("n=%d: roundtrip mismatch", n)
		}
	}
}

func TestPKCS7UnpadRejects(t *testing.T) {
	valid := pkcs7Pad([]byte("0123456789abcde"), 16) // 1-byte pad

	cases := map[string][]byte{
		"zero length":        {},
		"not multiple of 16": make([]byte, 17),
		"padding length 0":   append(append([]byte{}, valid[:15]...), 0),
		"padding length >16": append(append([]byte{}, valid[:15]...), 17),
		"corrupted padding": func() []byte {
			b := pkcs7Pad([]byte("0123456789ab"), 16) // 4-byte pad
			b[14] ^= 0x01                             // corrupt one padding byte
			return b
		}(),
	}
	for name, in := range cases {
		if _, err := pkcs7Unpad(in, 16); err == nil {
			t.Errorf("%s: unpad succeeded, want error", name)
		}
	}
}

// TestInteropVector verifies byte-exact compatibility with a reference vector
// generated outside Go with python3 + cryptography (Mythic's own AESPSK
// scheme): key = bytes 00..1f, IV = bytes 10..1f, CT = AES-256-CBC, TAG =
// HMAC-SHA256(key, IV||CT), envelope = base64(UUID||IV||CT||TAG).
const (
	interopKeyB64   = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	interopIVHex    = "101112131415161718191a1b1c1d1e1f"
	interopPT       = `{"action":"get_tasking","tasking_size":1}`
	interopEnvelope = "YjUwYTVmZTgtMDk5ZC00NjExLWEyYWMtOTZkOTNlNmVjNzdiEBESExQVFhcYGRobHB0eH8yvZScnBO0BIxhsF9K3jHDVEtxc8YYIs1OFg2/KsDI68xJ2+EtMRZNqV8IhGAGItuKoJq/DY0Tk71JK8U8TYRPINjMZ3B+cVD2OxXZwUSrd"
)

func TestInteropVectorDecrypt(t *testing.T) {
	key := testKey(t)
	uuid, pt, err := Decrypt(key, interopEnvelope)
	if err != nil {
		t.Fatalf("Decrypt interop vector: %v", err)
	}
	if uuid != testUUID {
		t.Errorf("uuid = %q, want %q", uuid, testUUID)
	}
	if string(pt) != interopPT {
		t.Errorf("plaintext = %q, want %q", pt, interopPT)
	}
}

func TestInteropVectorEncrypt(t *testing.T) {
	key := testKey(t)
	iv := make([]byte, ivLen)
	for i := range iv {
		iv[i] = byte(0x10 + i) // matches interopIVHex
	}
	env, err := encryptWithIV(testUUID, key, iv, []byte(interopPT))
	if err != nil {
		t.Fatalf("encryptWithIV: %v", err)
	}
	if env != interopEnvelope {
		t.Errorf("envelope mismatch:\n got: %s\nwant: %s", env, interopEnvelope)
	}
	_ = interopKeyB64 // documented above; key material identical to testKey
}

func TestStagingEKE(t *testing.T) {
	priv, msgJSON, sessionID, err := GenerateStagingKeypair()
	if err != nil {
		t.Fatalf("GenerateStagingKeypair: %v", err)
	}

	// Validate the staging message shape.
	var msg struct {
		Action    string `json:"action"`
		PubKey    string `json:"pub_key"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(msgJSON, &msg); err != nil {
		t.Fatalf("staging message not JSON: %v", err)
	}
	if msg.Action != "staging_rsa" {
		t.Errorf("action = %q, want staging_rsa", msg.Action)
	}
	if msg.SessionID != sessionID || len(sessionID) != 20 {
		t.Errorf("session_id = %q (len %d), want %q (len 20)", msg.SessionID, len(msg.SessionID), sessionID)
	}
	pemBytes, err := base64.StdEncoding.DecodeString(msg.PubKey)
	if err != nil {
		t.Fatalf("pub_key not base64: %v", err)
	}
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		t.Fatal("pub_key not PEM")
	}
	pub, err := x509.ParsePKCS1PublicKey(blk.Bytes)
	if err != nil {
		t.Fatalf("pub_key not PKCS1: %v", err)
	}
	if pub.N.BitLen() != 4096 {
		t.Errorf("RSA key size = %d, want 4096", pub.N.BitLen())
	}

	// Simulate Mythic: encrypt a new 32-byte session key to the agent's public
	// key using RSA-OAEP with SHA-1 (Mythic's staging_rsa choice).
	sessionKey := make([]byte, 40) // Mythic sends more than 32; we take first 32
	if _, err := rand.Read(sessionKey); err != nil {
		t.Fatal(err)
	}
	enc, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, sessionKey, nil)
	if err != nil {
		t.Fatalf("EncryptOAEP: %v", err)
	}
	got, err := DecryptSessionKey(priv, base64.StdEncoding.EncodeToString(enc))
	if err != nil {
		t.Fatalf("DecryptSessionKey: %v", err)
	}
	if len(got) != 32 || string(got) != string(sessionKey[:32]) {
		t.Errorf("session key mismatch: got %x, want %x", got, sessionKey[:32])
	}
}

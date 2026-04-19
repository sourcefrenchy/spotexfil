package crypto

import (
	"testing"
)

func TestGenerateX25519(t *testing.T) {
	priv, err := GenerateX25519()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if priv == nil {
		t.Fatal("nil private key")
	}
	pub := priv.PublicKey()
	if pub == nil {
		t.Fatal("nil public key")
	}
	if len(pub.Bytes()) != 32 {
		t.Errorf("pubkey length: got %d, want 32", len(pub.Bytes()))
	}
}

func TestX25519SharedSecret(t *testing.T) {
	// Both sides generate keypairs
	privA, _ := GenerateX25519()
	privB, _ := GenerateX25519()

	// Compute shared secrets from both sides
	secretA, err := privA.ECDH(privB.PublicKey())
	if err != nil {
		t.Fatalf("ECDH A->B: %v", err)
	}
	secretB, err := privB.ECDH(privA.PublicKey())
	if err != nil {
		t.Fatalf("ECDH B->A: %v", err)
	}

	// Must be identical
	if string(secretA) != string(secretB) {
		t.Fatal("shared secrets don't match")
	}
}

func TestDeriveSessionKey(t *testing.T) {
	privA, _ := GenerateX25519()
	privB, _ := GenerateX25519()

	secret, _ := privA.ECDH(privB.PublicKey())

	// Derive session key
	key, err := DeriveSessionKey(secret, "master-key")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length: got %d, want 32", len(key))
	}

	// Same inputs = same output (deterministic)
	key2, _ := DeriveSessionKey(secret, "master-key")
	if string(key) != string(key2) {
		t.Error("determinism failed")
	}

	// Different master key = different session key
	key3, _ := DeriveSessionKey(secret, "other-key")
	if string(key) == string(key3) {
		t.Error("different master keys produced same session key")
	}
}

func TestFullKeyExchangeFlow(t *testing.T) {
	// Simulate: implant generates keypair, sends pubkey in checkin
	implantPriv, _ := GenerateX25519()
	implantPub := implantPriv.PublicKey()

	// Operator generates keypair, receives implant pubkey, computes session key
	operatorPriv, _ := GenerateX25519()
	operatorPub := operatorPriv.PublicKey()

	sharedOp, _ := operatorPriv.ECDH(implantPub)
	sessionKeyOp, _ := DeriveSessionKey(sharedOp, "shared-passphrase")

	// Implant receives operator pubkey, computes same session key
	sharedImp, _ := implantPriv.ECDH(operatorPub)
	sessionKeyImp, _ := DeriveSessionKey(sharedImp, "shared-passphrase")

	// Both must have the same session key
	if string(sessionKeyOp) != string(sessionKeyImp) {
		t.Fatal("session keys don't match after full key exchange")
	}
}

func TestForwardSecrecyProperty(t *testing.T) {
	masterKey := "compromised-master-key"

	// Session 1
	priv1A, _ := GenerateX25519()
	priv1B, _ := GenerateX25519()
	secret1, _ := priv1A.ECDH(priv1B.PublicKey())
	session1Key, _ := DeriveSessionKey(secret1, masterKey)

	// Session 2 (new ephemeral keys)
	priv2A, _ := GenerateX25519()
	priv2B, _ := GenerateX25519()
	secret2, _ := priv2A.ECDH(priv2B.PublicKey())
	session2Key, _ := DeriveSessionKey(secret2, masterKey)

	// Different sessions must have different keys even with same master key
	if string(session1Key) == string(session2Key) {
		t.Fatal("different sessions produced same key — forward secrecy broken")
	}
}

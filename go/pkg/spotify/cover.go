package spotify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	spotifyapi "github.com/zmb3/spotify/v2"
)

// coverFetchLimit caps cover downloads at 1MB, well above Spotify's 256KB
// upload limit, to bound memory on a hostile or unexpected response.
const coverFetchLimit = 1 << 20

// SetPlaylistCover uploads a JPEG cover image for a playlist owned by the
// authenticated user. Requires ScopeImageUpload plus playlist-modify scope.
func (c *Client) SetPlaylistCover(ctx context.Context, playlistID string, jpeg []byte) error {
	if len(jpeg) == 0 {
		return fmt.Errorf("spotify: empty cover image")
	}
	return c.api.SetPlaylistImage(ctx, spotifyapi.ID(playlistID), bytes.NewReader(jpeg))
}

// GetPlaylistCover fetches the playlist, picks the largest cover image URL,
// HTTP GETs it, and returns the raw bytes. Cover URLs are public CDN links,
// so no auth header is needed for the download itself.
func (c *Client) GetPlaylistCover(ctx context.Context, playlistID string) ([]byte, error) {
	pl, err := c.api.GetPlaylist(ctx, spotifyapi.ID(playlistID))
	if err != nil {
		return nil, fmt.Errorf("spotify: get playlist %s: %w", playlistID, err)
	}
	if len(pl.Images) == 0 {
		return nil, fmt.Errorf("spotify: playlist %s has no cover images", playlistID)
	}
	best := pl.Images[0]
	for _, img := range pl.Images[1:] {
		if img.Width*img.Height > best.Width*best.Height {
			best = img
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, best.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("spotify: build cover request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify: fetch cover: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify: fetch cover: unexpected status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, coverFetchLimit))
}

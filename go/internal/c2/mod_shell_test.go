package c2

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestShellWorkerBasic(t *testing.T) {
	t.Cleanup(ResetShellWorker)
	m := &ShellModule{}

	status, data := m.Execute(map[string]interface{}{"cmd": "echo hello"})
	if status != "ok" {
		t.Fatalf("status: got %s, want ok (data %q)", status, data)
	}
	if data != "hello\n" {
		t.Errorf("data: got %q, want %q", data, "hello\n")
	}
}

func TestShellWorkerStatePersistence(t *testing.T) {
	t.Cleanup(ResetShellWorker)
	m := &ShellModule{}

	if runtime.GOOS == "windows" {
		// cmd.exe: `cd /d` changes drive+dir; bare `cd` prints the cwd.
		status, data := m.Execute(map[string]interface{}{"cmd": "cd /d %TEMP%"})
		if status != "ok" {
			t.Fatalf("cd status: got %s (data %q)", status, data)
		}
		status, data = m.Execute(map[string]interface{}{"cmd": "cd"})
		if status != "ok" {
			t.Fatalf("cd (print) status: got %s (data %q)", status, data)
		}
		if !strings.Contains(data, ":\\") {
			t.Errorf("expected a Windows path in %q", data)
		}
		return
	}

	dir := t.TempDir()
	// macOS temp dirs live under /var -> /private/var symlinks; pwd prints
	// the resolved physical path.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	status, data := m.Execute(map[string]interface{}{"cmd": "cd " + dir})
	if status != "ok" {
		t.Fatalf("cd status: got %s (data %q)", status, data)
	}

	// Second Execute must run in the SAME worker: cwd persisted.
	status, data = m.Execute(map[string]interface{}{"cmd": "pwd"})
	if status != "ok" {
		t.Fatalf("pwd status: got %s (data %q)", status, data)
	}
	// sh may report the logical path ($PWD) or the physical one depending on
	// the shell build; accept either.
	if !strings.Contains(data, dir) && !strings.Contains(data, realDir) {
		t.Errorf("pwd output %q contains neither %q nor %q (worker state not persisted)", data, dir, realDir)
	}
}

func TestShellWorkerExitCode(t *testing.T) {
	t.Cleanup(ResetShellWorker)
	m := &ShellModule{}

	// NB: use `false`, not `exit 3` — `exit` would kill the worker itself.
	status, _ := m.Execute(map[string]interface{}{"cmd": "false"})
	if status != "error" {
		t.Errorf("false: got status %s, want error", status)
	}

	status, data := m.Execute(map[string]interface{}{"cmd": "true"})
	if status != "ok" {
		t.Errorf("true: got status %s, want ok (data %q)", status, data)
	}
}

func TestShellWorkerConcurrency(t *testing.T) {
	t.Cleanup(ResetShellWorker)
	m := &ShellModule{}

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, data := m.Execute(map[string]interface{}{"cmd": fmt.Sprintf("echo %d", i)})
			if status != "ok" {
				errs <- fmt.Sprintf("goroutine %d: status %s (data %q)", i, status, data)
				return
			}
			want := fmt.Sprintf("%d\n", i)
			if data != want {
				errs <- fmt.Sprintf("goroutine %d: data %q, want %q", i, data, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestShellWorkerRespawnAfterExit(t *testing.T) {
	t.Cleanup(ResetShellWorker)
	m := &ShellModule{}

	// `exit` kills the worker shell mid-command; the sentinel never arrives.
	_, _ = m.Execute(map[string]interface{}{"cmd": "exit"})

	// The next Execute must transparently respawn a fresh worker.
	status, data := m.Execute(map[string]interface{}{"cmd": "echo back"})
	if status != "ok" {
		t.Fatalf("status: got %s, want ok (data %q)", status, data)
	}
	if !strings.Contains(data, "back") {
		t.Errorf("data: got %q, want it to contain %q", data, "back")
	}
}

// Guard against the temp-dir symlink assertion silently passing for the wrong
// reason on non-Darwin unixes: sanity-check os.TempDir is usable.
func TestShellWorkerTempDirSanity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sanity check")
	}
	if _, err := os.Stat(os.TempDir()); err != nil {
		t.Fatalf("os.TempDir: %v", err)
	}
}

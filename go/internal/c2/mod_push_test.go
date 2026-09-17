package c2

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

func TestPushEmptyPath(t *testing.T) {
	m := &PushModule{}
	status, data := m.Execute(map[string]interface{}{"data": "aGVsbG8="})
	if status != "error" || data != "Empty path" {
		t.Fatalf("expected (error, Empty path), got (%s, %s)", status, data)
	}
}

func TestPushEmptyData(t *testing.T) {
	m := &PushModule{}
	status, data := m.Execute(map[string]interface{}{"path": "x"})
	if status != "error" || data != "Empty data" {
		t.Fatalf("expected (error, Empty data), got (%s, %s)", status, data)
	}
}

func TestPushInvalidBase64(t *testing.T) {
	m := &PushModule{}
	status, data := m.Execute(map[string]interface{}{
		"path": filepath.Join(t.TempDir(), "x"),
		"data": "not-valid-base64!!!",
	})
	if status != "error" || data != "Invalid base64 data" {
		t.Fatalf("expected (error, Invalid base64 data), got (%s, %s)", status, data)
	}
}

func TestPushSuccessfulWrite(t *testing.T) {
	m := &PushModule{}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sub", "pushed.txt")
	content := []byte("hello push module")
	status, data := m.Execute(map[string]interface{}{
		"path": path,
		"data": base64.StdEncoding.EncodeToString(content),
	})
	if status != "ok" {
		t.Fatalf("expected ok, got (%s, %s)", status, data)
	}
	expected := fmt.Sprintf("Wrote %d bytes to %s", len(content), path)
	if data != expected {
		t.Fatalf("expected %q, got %q", expected, data)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: got %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected mode 0600, got %o", info.Mode().Perm())
	}
}

func TestPushOversizedContent(t *testing.T) {
	m := &PushModule{}
	oversized := make([]byte, shared.Proto.C2.MaxResultSize+1)
	path := filepath.Join(t.TempDir(), "big.bin")
	status, data := m.Execute(map[string]interface{}{
		"path": path,
		"data": base64.StdEncoding.EncodeToString(oversized),
	})
	if status != "error" {
		t.Fatalf("expected error, got (%s, %s)", status, data)
	}
	if !strings.HasPrefix(data, "File too large:") {
		t.Fatalf("unexpected message: %s", data)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should not have been written")
	}
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliverSessionKeyQuietRequiresKeyFile(t *testing.T) {
	var buf bytes.Buffer
	err := deliverSessionKey("alpha-bravo", "", true, &buf)
	if err == nil {
		t.Fatal("expected error for --quiet without --key-file")
	}
	if !strings.Contains(err.Error(), "--quiet requires --key-file") {
		t.Errorf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing should be printed in quiet mode, got %q", buf.String())
	}
}

func TestDeliverSessionKeyWritesKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.key")
	var buf bytes.Buffer

	if err := deliverSessionKey("alpha-bravo", path, false, &buf); err != nil {
		t.Fatalf("deliverSessionKey: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}
	if string(data) != "alpha-bravo\n" {
		t.Errorf("key file contents: got %q, want %q", data, "alpha-bravo\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("key file perms: got %o, want 600", info.Mode().Perm())
	}

	out := buf.String()
	if !strings.Contains(out, "[*] Session key: alpha-bravo") {
		t.Errorf("stdout missing key line: %q", out)
	}
	if !strings.Contains(out, "Session key written to "+path+" (0600)") {
		t.Errorf("stdout missing key-file confirmation: %q", out)
	}
}

func TestDeliverSessionKeyQuietWritesOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.key")
	var buf bytes.Buffer

	if err := deliverSessionKey("alpha-bravo", path, true, &buf); err != nil {
		t.Fatalf("deliverSessionKey: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("quiet mode printed %q, want no output", buf.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}
	if string(data) != "alpha-bravo\n" {
		t.Errorf("key file contents: got %q, want %q", data, "alpha-bravo\n")
	}
}

func TestDeliverSessionKeyNonQuietNoKeyFile(t *testing.T) {
	var buf bytes.Buffer
	if err := deliverSessionKey("alpha-bravo", "", false, &buf); err != nil {
		t.Fatalf("deliverSessionKey: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[*] Session key: alpha-bravo") {
		t.Errorf("stdout missing key line: %q", out)
	}
	if !strings.Contains(out, "c2-operator -k \"alpha-bravo\"") {
		t.Errorf("stdout missing operator hint: %q", out)
	}
	if strings.Contains(out, "written to") {
		t.Errorf("no key file given, but got write confirmation: %q", out)
	}
}

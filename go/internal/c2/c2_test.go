package c2

import (
	"encoding/json"
	"testing"
)

func TestShellModule(t *testing.T) {
	m := &ShellModule{}
	if m.Name() != "shell" {
		t.Errorf("name: got %s, want shell", m.Name())
	}

	// Test a simple command
	status, data := m.Execute(map[string]interface{}{"cmd": "echo hello"})
	if status != "ok" {
		t.Errorf("status: got %s, want ok", status)
	}
	if data != "hello\n" {
		t.Errorf("data: got %q, want %q", data, "hello\n")
	}

	// Test empty command
	status, _ = m.Execute(map[string]interface{}{})
	if status != "error" {
		t.Errorf("empty cmd status: got %s, want error", status)
	}
}

func TestExfilModule(t *testing.T) {
	m := &ExfilModule{}
	if m.Name() != "exfil" {
		t.Errorf("name: got %s, want exfil", m.Name())
	}

	// Test empty path
	status, _ := m.Execute(map[string]interface{}{})
	if status != "error" {
		t.Errorf("empty path status: got %s, want error", status)
	}

	// Test nonexistent file
	status, _ = m.Execute(map[string]interface{}{"path": "/nonexistent/file"})
	if status != "error" {
		t.Errorf("nonexistent file status: got %s, want error", status)
	}
}

func TestSysinfoModule(t *testing.T) {
	m := &SysinfoModule{}
	if m.Name() != "sysinfo" {
		t.Errorf("name: got %s, want sysinfo", m.Name())
	}

	status, data := m.Execute(nil)
	if status != "ok" {
		t.Errorf("status: got %s, want ok", status)
	}

	var info map[string]interface{}
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		t.Fatalf("parse sysinfo: %v", err)
	}

	if _, ok := info["os"]; !ok {
		t.Error("missing 'os' field")
	}
	if _, ok := info["hostname"]; !ok {
		t.Error("missing 'hostname' field")
	}
}

func TestModuleRegistry(t *testing.T) {
	modules := ListModules()
	if len(modules) < 3 {
		t.Errorf("expected at least 3 modules, got %d", len(modules))
	}

	for _, name := range []string{"shell", "exfil", "sysinfo"} {
		m := GetModule(name)
		if m == nil {
			t.Errorf("module %s not found in registry", name)
		}
	}

	// Unknown module
	if GetModule("nonexistent") != nil {
		t.Error("expected nil for unknown module")
	}
}

func TestModuleRegistryDynamic(t *testing.T) {
	// Register a test module
	testMod := &testModule{name: "test-dynamic"}
	RegisterModule(testMod)
	defer UnregisterModule("test-dynamic")

	m := GetModule("test-dynamic")
	if m == nil {
		t.Fatal("test-dynamic not found after registration")
	}
	if m.Name() != "test-dynamic" {
		t.Errorf("name: got %s", m.Name())
	}

	// Unregister
	UnregisterModule("test-dynamic")
	if GetModule("test-dynamic") != nil {
		t.Error("test-dynamic found after unregistration")
	}
}

func TestModuleRegistryConcurrency(t *testing.T) {
	// Concurrent register/unregister/get should not race
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			name := "concurrent-" + string(rune('A'+id))
			mod := &testModule{name: name}
			RegisterModule(mod)
			_ = GetModule(name)
			_ = ListModules()
			UnregisterModule(name)
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

type testModule struct {
	name string
}

func (m *testModule) Name() string { return m.name }
func (m *testModule) Execute(args map[string]interface{}) (string, string) {
	return "ok", "test"
}

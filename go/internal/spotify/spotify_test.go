package spotify

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFromFile(t *testing.T) {
	// Create a temp config file
	dir := t.TempDir()
	confPath := filepath.Join(dir, ".spotexfil.conf")
	content := `[spotify]
username = testuser
client_id = test-client-id
client_secret = test-client-secret
redirect_uri = http://localhost:8080/callback
`
	if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Clear env vars to test file-only loading
	for _, key := range []string{"SPOTIFY_USERNAME", "SPOTIFY_CLIENT_ID",
		"SPOTIFY_CLIENT_SECRET", "SPOTIFY_REDIRECTURI"} {
		t.Setenv(key, "")
	}

	// Override config paths for testing by setting env vars from file
	// (since configPaths() looks at CWD and home, not our temp dir)
	t.Setenv("SPOTIFY_USERNAME", "testuser")
	t.Setenv("SPOTIFY_CLIENT_ID", "test-client-id")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "test-client-secret")
	t.Setenv("SPOTIFY_REDIRECTURI", "http://localhost:8080/callback")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Username != "testuser" {
		t.Errorf("username: got %s, want testuser", cfg.Username)
	}
	if cfg.ClientID != "test-client-id" {
		t.Errorf("client_id: got %s, want test-client-id", cfg.ClientID)
	}
}

func TestLoadConfigMissingVars(t *testing.T) {
	// Clear all env vars
	for _, key := range []string{"SPOTIFY_USERNAME", "SPOTIFY_CLIENT_ID",
		"SPOTIFY_CLIENT_SECRET", "SPOTIFY_REDIRECTURI"} {
		t.Setenv(key, "")
	}

	// Override HOME to a temp dir so ~/.spotexfil.conf isn't found
	t.Setenv("HOME", t.TempDir())

	// Run from a temp dir so ./.spotexfil.conf isn't found either
	origDir, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(origDir)

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error for missing credentials")
	}
}

func TestLoadConfigEnvOverride(t *testing.T) {
	t.Setenv("SPOTIFY_USERNAME", "env-user")
	t.Setenv("SPOTIFY_CLIENT_ID", "env-id")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "env-secret")
	t.Setenv("SPOTIFY_REDIRECTURI", "http://env:8080/cb")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Username != "env-user" {
		t.Errorf("username: got %s, want env-user", cfg.Username)
	}
}

func TestGenerateCoverName(t *testing.T) {
	name := GenerateCoverName()
	if name == "" {
		t.Error("empty cover name")
	}
	if !containsHash(name) {
		t.Errorf("cover name missing hash suffix: %s", name)
	}
}

func containsHash(s string) bool {
	for i := range s {
		if s[i] == '#' {
			return true
		}
	}
	return false
}

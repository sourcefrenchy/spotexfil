package spotify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validTokenJSON returns a spotipy-format cache JSON blob with a refresh
// token (accepted regardless of expiry).
func validTokenJSON(accessToken string) string {
	return fmt.Sprintf(`{
		"access_token": %q,
		"token_type": "Bearer",
		"expires_in": 3600,
		"scope": "user-library-read",
		"expires_at": %d,
		"refresh_token": "refresh-xyz"
	}`, accessToken, time.Now().Add(time.Hour).Unix())
}

// testConfig returns a Config with a unique username so no real
// .cache-<username> file in CWD/parent/home can match.
func testConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Username:     fmt.Sprintf("spotexfil-test-%d", time.Now().UnixNano()),
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		RedirectURI:  "http://localhost:8888/callback",
	}
}

func TestResolveTokenFromTokenFile(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("SPOTIFY_TOKEN_JSON", "")

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	if err := os.WriteFile(tokenPath, []byte(validTokenJSON("file-access-token")), 0600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	opts := ClientOptions{TokenFile: tokenPath, AllowOAuth: false}
	token, err := resolveToken(cfg, opts, nil)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if token == nil {
		t.Fatal("expected token, got nil (OAuth sentinel)")
	}
	if token.AccessToken != "file-access-token" {
		t.Errorf("access token = %q, want %q", token.AccessToken, "file-access-token")
	}
	if token.RefreshToken != "refresh-xyz" {
		t.Errorf("refresh token = %q, want %q", token.RefreshToken, "refresh-xyz")
	}
}

func TestResolveTokenFromEnv(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("SPOTIFY_TOKEN_JSON", validTokenJSON("env-access-token"))

	opts := ClientOptions{AllowOAuth: false}
	token, err := resolveToken(cfg, opts, nil)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if token == nil {
		t.Fatal("expected token, got nil (OAuth sentinel)")
	}
	if token.AccessToken != "env-access-token" {
		t.Errorf("access token = %q, want %q", token.AccessToken, "env-access-token")
	}
}

func TestResolveTokenFileWinsOverEnv(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("SPOTIFY_TOKEN_JSON", validTokenJSON("env-access-token"))

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	if err := os.WriteFile(tokenPath, []byte(validTokenJSON("file-access-token")), 0600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	opts := ClientOptions{TokenFile: tokenPath, AllowOAuth: false}
	token, err := resolveToken(cfg, opts, nil)
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if token == nil {
		t.Fatal("expected token, got nil (OAuth sentinel)")
	}
	if token.AccessToken != "file-access-token" {
		t.Errorf("access token = %q, want TokenFile to win over env", token.AccessToken)
	}
}

func TestResolveTokenNoTokenOAuthDisabled(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("SPOTIFY_TOKEN_JSON", "")

	opts := ClientOptions{AllowOAuth: false}
	token, err := resolveToken(cfg, opts, nil)
	if err == nil {
		t.Fatal("expected error when no token available and OAuth disabled")
	}
	if token != nil {
		t.Errorf("expected nil token, got %+v", token)
	}
	if !strings.Contains(err.Error(), "interactive OAuth disabled") {
		t.Errorf("error should mention interactive OAuth disabled, got: %v", err)
	}
}

func TestResolveTokenNoTokenOAuthAllowed(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("SPOTIFY_TOKEN_JSON", "")

	opts := ClientOptions{AllowOAuth: true}
	token, err := resolveToken(cfg, opts, nil)
	if err != nil {
		t.Fatalf("expected no error when OAuth allowed, got: %v", err)
	}
	if token != nil {
		t.Errorf("expected nil token (OAuth sentinel), got %+v", token)
	}
}

func TestResolveTokenMalformedEnvFallsThrough(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("SPOTIFY_TOKEN_JSON", "{not valid json")

	// Malformed env + OAuth disabled → clear error (env skipped gracefully)
	opts := ClientOptions{AllowOAuth: false}
	token, err := resolveToken(cfg, opts, nil)
	if err == nil {
		t.Fatal("expected fall-through error when env JSON is malformed")
	}
	if token != nil {
		t.Errorf("expected nil token, got %+v", token)
	}
	if !strings.Contains(err.Error(), "interactive OAuth disabled") {
		t.Errorf("error should mention interactive OAuth disabled, got: %v", err)
	}

	// Malformed env + OAuth allowed → sentinel (nil, nil)
	opts.AllowOAuth = true
	token, err = resolveToken(cfg, opts, nil)
	if err != nil {
		t.Fatalf("expected graceful fall-through to OAuth sentinel, got: %v", err)
	}
	if token != nil {
		t.Errorf("expected nil token (OAuth sentinel), got %+v", token)
	}
}

func TestTokenFromCacheJSON(t *testing.T) {
	// Valid with refresh token (even if expired)
	tok, err := tokenFromCacheJSON([]byte(`{
		"access_token": "a", "token_type": "Bearer", "expires_in": 3600,
		"scope": "s", "expires_at": 1, "refresh_token": "r"}`))
	if err != nil {
		t.Fatalf("expired token with refresh token should be accepted: %v", err)
	}
	if tok.RefreshToken != "r" {
		t.Errorf("refresh token = %q, want %q", tok.RefreshToken, "r")
	}

	// Valid unexpired without refresh token
	tok, err = tokenFromCacheJSON([]byte(fmt.Sprintf(`{
		"access_token": "a", "token_type": "Bearer", "expires_in": 3600,
		"scope": "s", "expires_at": %d, "refresh_token": ""}`,
		time.Now().Add(time.Hour).Unix())))
	if err != nil {
		t.Fatalf("unexpired token should be accepted: %v", err)
	}

	// Expired without refresh token → error
	if _, err = tokenFromCacheJSON([]byte(`{
		"access_token": "a", "token_type": "Bearer", "expires_in": 3600,
		"scope": "s", "expires_at": 1, "refresh_token": ""}`)); err == nil {
		t.Error("expected error for expired token without refresh token")
	}

	// Malformed JSON → error
	if _, err = tokenFromCacheJSON([]byte("{nope")); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

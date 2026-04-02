// Package spotify provides the Spotify API wrapper for covert channel operations.
package spotify

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/ini.v1"
)

// Config holds Spotify API credentials.
type Config struct {
	Username     string
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// configPaths returns the config file search paths (CWD first, then home).
func configPaths() []string {
	paths := []string{
		filepath.Join(".", ".spotexfil.conf"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".spotexfil.conf"))
	}
	return paths
}

// LoadConfig loads Spotify credentials from env vars and/or config file.
// Environment variables always take precedence over config file values.
func LoadConfig() (*Config, error) {
	// Try loading from config file first
	fileVals := loadConfigFile()

	// Helper: get value from env, fall back to config file
	get := func(envKey, confVal string) string {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		return confVal
	}

	cfg := &Config{
		Username:     get("SPOTIFY_USERNAME", fileVals["SPOTIFY_USERNAME"]),
		ClientID:     get("SPOTIFY_CLIENT_ID", fileVals["SPOTIFY_CLIENT_ID"]),
		ClientSecret: get("SPOTIFY_CLIENT_SECRET", fileVals["SPOTIFY_CLIENT_SECRET"]),
		RedirectURI:  get("SPOTIFY_REDIRECTURI", fileVals["SPOTIFY_REDIRECTURI"]),
	}

	// Validate required fields
	missing := []string{}
	if cfg.Username == "" {
		missing = append(missing, "SPOTIFY_USERNAME")
	}
	if cfg.ClientID == "" {
		missing = append(missing, "SPOTIFY_CLIENT_ID")
	}
	if cfg.ClientSecret == "" {
		missing = append(missing, "SPOTIFY_CLIENT_SECRET")
	}
	if cfg.RedirectURI == "" {
		missing = append(missing, "SPOTIFY_REDIRECTURI")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing credentials: %v\nSet env vars or create ~/.spotexfil.conf", missing)
	}

	return cfg, nil
}

// loadConfigFile searches for .spotexfil.conf and parses it.
func loadConfigFile() map[string]string {
	result := make(map[string]string)

	keyMap := map[string]string{
		"username":      "SPOTIFY_USERNAME",
		"client_id":     "SPOTIFY_CLIENT_ID",
		"client_secret": "SPOTIFY_CLIENT_SECRET",
		"redirect_uri":  "SPOTIFY_REDIRECTURI",
	}

	for _, path := range configPaths() {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		cfg, err := ini.Load(path)
		if err != nil {
			continue
		}

		section, err := cfg.GetSection("spotify")
		if err != nil {
			continue
		}

		for confKey, envKey := range keyMap {
			if section.HasKey(confKey) {
				result[envKey] = section.Key(confKey).String()
			}
		}

		fmt.Printf("[*] Loaded config from %s\n", path)
		return result
	}

	return result
}

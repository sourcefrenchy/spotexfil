//go:build !implantonly

package c2

import (
	"testing"
	"time"

	"github.com/sourcefrenchy/spotexfil/pkg/protocol"
)

// TestRouteTunnelLocked verifies the PollResults tunnel-routing hook:
// non-tunnel results pass through, tunnel frames are consumed, the
// poller never blocks, and frames reach the tunnel server when running.
func TestRouteTunnelLocked(t *testing.T) {
	op := newTestOperator(t)

	// Non-tunnel result: not consumed
	if op.routeTunnelLocked(map[string]interface{}{"module": "shell", "data": "x"}) {
		t.Error("non-tunnel result should not be consumed")
	}

	// Tunnel frame with NO server running: consumed without panic
	frame := Frame{Conn: 1, Op: TunnelOpData, Seq: 0, Data: "aGk="}
	if !op.routeTunnelLocked(map[string]interface{}{
		"module": "tunnel", "data": mustMarshalFrame(t, frame),
	}) {
		t.Error("tunnel frame should be consumed even with no server running")
	}

	// Inject a running tunnel server and verify delivery
	ch := make(chan Frame, 1)
	operatorTunnelsMu.Lock()
	operatorTunnels[op] = &tunnelServer{frameCh: ch}
	operatorTunnelsMu.Unlock()
	defer func() {
		operatorTunnelsMu.Lock()
		delete(operatorTunnels, op)
		operatorTunnelsMu.Unlock()
	}()

	if !op.routeTunnelLocked(map[string]interface{}{
		"module": "tunnel", "data": mustMarshalFrame(t, frame),
	}) {
		t.Fatal("tunnel frame should be consumed")
	}
	select {
	case got := <-ch:
		if got.Conn != 1 || got.Op != TunnelOpData {
			t.Errorf("delivered frame mismatch: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("frame was not delivered to the tunnel server")
	}

	// Full channel: routing must not block the poller
	for i := 0; i < 5; i++ {
		done := make(chan struct{})
		go func() {
			op.routeTunnelLocked(map[string]interface{}{
				"module": "tunnel", "data": mustMarshalFrame(t, frame),
			})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("routeTunnelLocked blocked on a full channel")
		}
	}
}

// TestHandleCommandPayloadTunnelDispatch verifies tunnel commands are
// routed to the tunnel manager (marked processed, NOT dispatched to the
// module executor, which would emit an "Unknown module" error result).
func TestHandleCommandPayloadTunnelDispatch(t *testing.T) {
	key := "testkey"
	imp := NewImplantWithOptions(nil, key,
		ImplantOptions{Interval: 20, Quiet: true})

	// A "data" frame for a nonexistent connection is dropped silently by
	// the tunnel manager — crucially it must NOT go through execute().
	msg := protocol.NewC2Message("tunnel", 42)
	msg.Args = map[string]interface{}{
		"conn": float64(99),
		"op":   "data",
		"seq":  float64(0),
		"data": "aGk=",
	}
	encoded, err := protocol.EncodeMessage(msg.ToCommandMap(), key)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	imp.handleCommandPayload(42, encoded)

	select {
	case r := <-imp.resultCh:
		t.Fatalf("tunnel frame wrongly dispatched to module executor: %+v", r)
	case <-time.After(500 * time.Millisecond):
		// expected: no result — the tunnel manager consumed the frame
	}

	imp.seqMu.Lock()
	processed := imp.processedSeqs[42]
	imp.seqMu.Unlock()
	if !processed {
		t.Error("tunnel command should be marked processed")
	}
}

func mustMarshalFrame(t *testing.T, f Frame) string {
	t.Helper()
	return f.Marshal()
}

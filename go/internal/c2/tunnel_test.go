//go:build !implantonly

package c2

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/protocol"
)

func TestTunnelFrameRoundtrip(t *testing.T) {
	frames := []Frame{
		{Conn: 3, Op: TunnelOpOpen, Addr: "10.0.0.1:22"},
		NewDataFrame(3, 7, []byte("hello world")),
		{Conn: 3, Op: TunnelOpClose},
		{Conn: 4, Op: TunnelOpOpenFail, Data: "dial tcp: connection refused"},
		{Conn: 4, Op: TunnelOpOpenOK},
	}
	for _, want := range frames {
		// JSON Data-string roundtrip (implant -> operator)
		got, err := UnmarshalFrame(want.Marshal())
		if err != nil {
			t.Fatalf("UnmarshalFrame: %v", err)
		}
		if got != want {
			t.Errorf("Data roundtrip: got %+v, want %+v", got, want)
		}

		// Args roundtrip (operator -> implant), through JSON so ints
		// become float64 like on the real channel.
		raw, err := json.Marshal(want.toArgs())
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		var args map[string]interface{}
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatalf("unmarshal args: %v", err)
		}
		if got := frameFromArgs(args); got != want {
			t.Errorf("Args roundtrip: got %+v, want %+v", got, want)
		}
	}

	// Payload helper
	df := NewDataFrame(1, 0, []byte("ping"))
	p, err := df.Payload()
	if err != nil || string(p) != "ping" {
		t.Errorf("Payload: got %q, err %v", p, err)
	}

	// FrameFromResult
	res := map[string]interface{}{
		"module": "tunnel",
		"data":   df.Marshal(),
	}
	if f, ok := FrameFromResult(res); !ok || f != df {
		t.Errorf("FrameFromResult: got %+v ok=%v", f, ok)
	}
	if _, ok := FrameFromResult(map[string]interface{}{"module": "shell"}); ok {
		t.Error("FrameFromResult accepted non-tunnel result")
	}
}

func TestTunnelSocksHandshake(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	type hsResult struct {
		req *socksRequest
		err error
	}
	resCh := make(chan hsResult, 1)
	go func() {
		req, err := socksHandshake(server)
		resCh <- hsResult{req, err}
	}()

	// Greeting: VER=5, NMETHODS=1, no-auth
	if _, err := client.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(client, rep); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		t.Fatalf("method reply = %v, want [05 00]", rep)
	}

	// CONNECT request for domain example.com:80
	dom := "example.com"
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(dom))}
	req = append(req, dom...)
	req = append(req, 0x00, 0x50) // port 80
	if _, err := client.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("handshake: %v", r.err)
		}
		if r.req.cmd != socksCmdConnect {
			t.Errorf("cmd = %d, want CONNECT", r.req.cmd)
		}
		if r.req.atyp != socksAtypDomain {
			t.Errorf("atyp = %d, want domain", r.req.atyp)
		}
		if r.req.addr != "example.com:80" {
			t.Errorf("addr = %q, want example.com:80", r.req.addr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handshake timed out")
	}
}

func TestTunnelReorderBuffer(t *testing.T) {
	rb := newReorderBuffer()

	// Feed seqs 2, 0, 1 -> output order 0, 1, 2
	if out := rb.push(2, []byte("two")); out != nil {
		t.Errorf("push(2) delivered %d payloads, want 0", len(out))
	}
	out := rb.push(0, []byte("zero"))
	if len(out) != 1 || string(out[0]) != "zero" {
		t.Fatalf("push(0) = %v, want [zero]", out)
	}
	out = rb.push(1, []byte("one"))
	if len(out) != 2 || string(out[0]) != "one" || string(out[1]) != "two" {
		t.Fatalf("push(1) = %v, want [one two]", out)
	}

	// Duplicate seq dropped
	if out := rb.push(1, []byte("one-dup")); out != nil {
		t.Errorf("duplicate push(1) delivered %d payloads, want 0", len(out))
	}
	// Duplicate buffered seq dropped
	rb2 := newReorderBuffer()
	rb2.push(1, []byte("one"))
	rb2.push(1, []byte("one-dup"))
	out = rb2.push(0, []byte("zero"))
	if len(out) != 2 || string(out[0]) != "zero" || string(out[1]) != "one" {
		t.Fatalf("push(0) = %v, want [zero one]", out)
	}
}

func TestTunnelIPv6Rejected(t *testing.T) {
	sendCalled := false
	srv := newTunnelServer(func(Frame) error {
		sendCalled = true
		return nil
	})
	defer srv.close()

	server, client := net.Pipe()
	defer client.Close()
	go srv.handleClient(server)

	// Greeting
	if _, err := client.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(client, rep); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if rep[1] != 0x00 {
		t.Fatalf("method reply = %v, want [05 00]", rep)
	}

	// CONNECT with ATYP=4 (IPv6) ::1 port 443
	req := []byte{0x05, 0x01, 0x00, 0x04}
	req = append(req, make([]byte, 16)...) // :: (all zeros)
	req[4+15] = 1                          // ::1
	req = append(req, 0x01, 0xBB)          // port 443
	if _, err := client.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != socksRepAtypUnsup {
		t.Fatalf("reply = %v, want [05 08 ...] (atyp not supported)", reply)
	}
	if sendCalled {
		t.Error("send called for unsupported address type")
	}
}

func TestTunnelImplantOpenFail(t *testing.T) {
	imp := NewImplantWithOptions(nil, "test-key",
		ImplantOptions{Interval: 20, Quiet: true})

	msg := protocol.NewC2Message("tunnel", 1)
	msg.Args = Frame{Conn: 7, Op: TunnelOpOpen, Addr: "127.0.0.1:1"}.toArgs()
	imp.handleTunnelFrame(msg)

	select {
	case res := <-imp.resultCh:
		if res.Module != "tunnel" {
			t.Errorf("result module = %q, want tunnel", res.Module)
		}
		if res.Status != "error" {
			t.Errorf("result status = %q, want error", res.Status)
		}
		f, err := UnmarshalFrame(res.Data)
		if err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		if f.Op != TunnelOpOpenFail || f.Conn != 7 {
			t.Errorf("frame = %+v, want open-fail conn=7", f)
		}
		if f.Data == "" {
			t.Error("open-fail frame missing error string")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no open-fail result")
	}
}

// TestTunnelLoopback wires an operator-side tunnel server and an
// implant back-to-back in memory (no Spotify) and pushes data through
// a SOCKS5 CONNECT to a local echo server.
func TestTunnelLoopback(t *testing.T) {
	// Echo server = the "target only the implant can reach".
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()

	imp := NewImplantWithOptions(nil, "test-key",
		ImplantOptions{Interval: 20, Quiet: true})

	// Operator side: frames to the implant are delivered synchronously
	// (preserving C2 message order); implant results are drained into
	// the operator's frame channel.
	srv := newTunnelServer(func(f Frame) error {
		msg := protocol.NewC2Message("tunnel", 0)
		msg.Args = f.toArgs()
		imp.handleTunnelFrame(msg)
		return nil
	})
	defer srv.close()
	go func() {
		for msg := range imp.resultCh {
			f, err := UnmarshalFrame(msg.Data)
			if err != nil {
				continue
			}
			srv.frameCh <- f
		}
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks listen: %v", err)
	}
	srv.serve(ln)

	// SOCKS5 client
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial socks: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))

	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	rep := make([]byte, 2)
	if _, err := io.ReadFull(c, rep); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		t.Fatalf("method reply = %v, want [05 00]", rep)
	}

	// CONNECT to the echo server via IPv4 ATYP
	echoAddr := echoLn.Addr().(*net.TCPAddr)
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, echoAddr.IP.To4()...)
	var portB [2]byte
	binary.BigEndian.PutUint16(portB[:], uint16(echoAddr.Port))
	req = append(req, portB[:]...)
	if _, err := c.Write(req); err != nil {
		t.Fatalf("write connect: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	if reply[1] != socksRepSuccess {
		t.Fatalf("connect reply = %v, want success", reply)
	}

	// Small write: "ping" -> "ping"
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read ping: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("got %q, want ping", got)
	}

	// Multi-frame write: 1000 bytes > 400-byte chunk size, exercising
	// sequencing + the reorder buffer.
	big := make([]byte, 1000)
	for i := range big {
		big[i] = byte(i)
	}
	if _, err := c.Write(big); err != nil {
		t.Fatalf("write big: %v", err)
	}
	gotBig := make([]byte, 1000)
	if _, err := io.ReadFull(c, gotBig); err != nil {
		t.Fatalf("read big: %v", err)
	}
	if !bytes.Equal(gotBig, big) {
		t.Fatal("big payload corrupted through tunnel")
	}

	// Clean close: client closes, implant's close frame should tear
	// down the operator-side connection state.
	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if srv.connCount() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("operator still has %d tunnel conns after close", srv.connCount())
}

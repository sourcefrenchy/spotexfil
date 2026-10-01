package pump

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

// --- fakes -----------------------------------------------------------------

type sentRecord struct {
	uuid     string
	envelope string
}

type fakeMythic struct {
	mu        sync.Mutex
	sent      []sentRecord
	sendErr   error
	taskingCh chan tasking
	recvErr   error
}

type tasking struct {
	uuid     string
	envelope string
}

func (f *fakeMythic) SendToMythic(uuid string, envelopeB64 string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, sentRecord{uuid: uuid, envelope: envelopeB64})
	return nil
}

func (f *fakeMythic) RecvTasking(ctx context.Context) (string, string, error) {
	if f.recvErr != nil {
		return "", "", f.recvErr
	}
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case t, ok := <-f.taskingCh:
		if !ok {
			<-ctx.Done()
			return "", "", ctx.Err()
		}
		return t.uuid, t.envelope, nil
	}
}

func (f *fakeMythic) sentRecords() []sentRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentRecord(nil), f.sent...)
}

type writeRecord struct {
	channel  string
	seq      int
	envelope string
}

type cleanRecord struct {
	channel string
	seq     int
}

type fakeTransport struct {
	mu       sync.Mutex
	channels map[string]map[int]string
	writes   []writeRecord
	cleans   []cleanRecord
	readErr  error
	writeErr error
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{channels: map[string]map[int]string{}}
}

func (f *fakeTransport) ReadChannel(ctx context.Context, channel string) (map[int]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	out := map[int]string{}
	for seq, env := range f.channels[channel] {
		out[seq] = env
	}
	return out, nil
}

func (f *fakeTransport) WriteChannel(ctx context.Context, channel string, seq int, envelopeB64 string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, writeRecord{channel: channel, seq: seq, envelope: envelopeB64})
	return nil
}

func (f *fakeTransport) CleanChannel(ctx context.Context, channel string, seq int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleans = append(f.cleans, cleanRecord{channel: channel, seq: seq})
	delete(f.channels[channel], seq)
	return nil
}

func (f *fakeTransport) writeRecords() []writeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]writeRecord(nil), f.writes...)
}

func (f *fakeTransport) cleanRecords() []cleanRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cleanRecord(nil), f.cleans...)
}

// --- helpers ---------------------------------------------------------------

const testUUID = "12345678-1234-1234-1234-123456789012"

func makeEnvelope(uuid string) string {
	return base64.StdEncoding.EncodeToString([]byte(uuid + "opaque-encrypted-blob"))
}

func newTestPump(m MythicSide, tr TransportSide, cfg Config) *Pump {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Millisecond
	}
	p, err := New(m, tr, cfg)
	if err != nil {
		panic(err)
	}
	return p
}

// --- tests -----------------------------------------------------------------

func TestAgentMessageFlowsToMythicWithUUID(t *testing.T) {
	m := &fakeMythic{taskingCh: make(chan tasking)}
	tr := newFakeTransport()
	envelope := makeEnvelope(testUUID)
	tr.channels[DefaultResChannel] = map[int]string{7: envelope}

	p := newTestPump(m, tr, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.RunAgentToMythic(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if recs := m.sentRecords(); len(recs) == 1 {
			if recs[0].uuid != testUUID {
				t.Fatalf("uuid = %q, want %q", recs[0].uuid, testUUID)
			}
			if recs[0].envelope != envelope {
				t.Fatalf("envelope mangled: got %q", recs[0].envelope)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("message was not forwarded to Mythic in time")
}

func TestTaskingFlowsToCmdChannelWithIncrementingSeq(t *testing.T) {
	m := &fakeMythic{taskingCh: make(chan tasking, 2)}
	tr := newFakeTransport()
	p := newTestPump(m, tr, Config{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.RunMythicToAgent(ctx) }()

	envA := makeEnvelope("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	envB := makeEnvelope("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	m.taskingCh <- tasking{uuid: "a", envelope: envA}
	m.taskingCh <- tasking{uuid: "b", envelope: envB}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(tr.writeRecords()) == 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	writes := tr.writeRecords()
	if len(writes) != 2 {
		t.Fatalf("writes = %d, want 2", len(writes))
	}
	// Order is deterministic here (single goroutine), but sort by seq anyway.
	sort.Slice(writes, func(i, j int) bool { return writes[i].seq < writes[j].seq })
	if writes[0].seq != 0 || writes[1].seq != 1 {
		t.Fatalf("seqs = %d,%d, want 0,1", writes[0].seq, writes[1].seq)
	}
	if writes[0].channel != DefaultCmdChannel || writes[1].channel != DefaultCmdChannel {
		t.Fatalf("tasking written to wrong channel: %+v", writes)
	}
	if writes[0].envelope != envA || writes[1].envelope != envB {
		t.Fatalf("envelopes mangled: %+v", writes)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunMythicToAgent returned %v, want nil on cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunMythicToAgent did not exit on cancel")
	}
}

func TestBadEnvelopeIsSkippedAndCleaned(t *testing.T) {
	m := &fakeMythic{taskingCh: make(chan tasking)}
	tr := newFakeTransport()
	tr.channels[DefaultResChannel] = map[int]string{
		3: "!!!not-base64!!!",
		4: makeEnvelope(testUUID),
	}

	p := newTestPump(m, tr, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.RunAgentToMythic(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cleans := tr.cleanRecords()
		if len(cleans) == 2 && len(m.sentRecords()) == 1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("bad envelope not skipped+cleaned: sent=%v cleans=%v", m.sentRecords(), tr.cleanRecords())
}

func TestCleanCalledAfterSuccessfulSend(t *testing.T) {
	m := &fakeMythic{taskingCh: make(chan tasking)}
	tr := newFakeTransport()
	tr.channels[DefaultResChannel] = map[int]string{11: makeEnvelope(testUUID)}

	p := newTestPump(m, tr, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.RunAgentToMythic(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cleans := tr.cleanRecords()
		if len(cleans) == 1 {
			if cleans[0].channel != DefaultResChannel || cleans[0].seq != 11 {
				t.Fatalf("clean record = %+v", cleans[0])
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("clean not called after successful send")
}

func TestSendFailureLeavesPlaylistForRetry(t *testing.T) {
	m := &fakeMythic{taskingCh: make(chan tasking), sendErr: errors.New("mythic down")}
	tr := newFakeTransport()
	tr.channels[DefaultResChannel] = map[int]string{5: makeEnvelope(testUUID)}

	p := newTestPump(m, tr, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.RunAgentToMythic(ctx) }()

	time.Sleep(50 * time.Millisecond)
	if len(tr.cleanRecords()) != 0 {
		t.Fatal("clean must not run when send fails (message would be lost)")
	}
	if len(m.sentRecords()) != 0 {
		t.Fatal("send should have errored, no record expected")
	}
}

func TestContextCancellationExitsBothLoops(t *testing.T) {
	m := &fakeMythic{taskingCh: make(chan tasking)}
	tr := newFakeTransport()
	p := newTestPump(m, tr, Config{PollInterval: time.Hour}) // no tick fires

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- p.RunAgentToMythic(ctx) }()
	go func() { done <- p.RunMythicToAgent(ctx) }()

	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("loop returned %v, want nil on cancel", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("loop did not exit on cancel")
		}
	}
}

func TestNewValidatesAndDefaults(t *testing.T) {
	if _, err := New(nil, newFakeTransport(), Config{}); err == nil {
		t.Fatal("nil MythicSide should error")
	}
	if _, err := New(&fakeMythic{}, nil, Config{}); err == nil {
		t.Fatal("nil TransportSide should error")
	}
	p := newTestPump(&fakeMythic{}, newFakeTransport(), Config{})
	if p.cmdChannel != DefaultCmdChannel || p.resChannel != DefaultResChannel {
		t.Fatalf("default channels not applied: %q %q", p.cmdChannel, p.resChannel)
	}
}

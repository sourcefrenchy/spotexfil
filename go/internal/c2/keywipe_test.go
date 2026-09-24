//go:build !implantonly

package c2

import (
	"strings"
	"testing"
)

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestSetSessionKeyZeroesOldKey(t *testing.T) {
	imp := newTestImplant(ImplantOptions{Quiet: true})

	old := []byte("0123456789abcdef0123456789abcdef")
	imp.setSessionKey(old)
	imp.setSessionKey([]byte("fedcba9876543210fedcba9876543210"))

	if !allZero(old) {
		t.Errorf("old session key not zeroed on rotation: %q", old)
	}

	// Reset via nil (shutdown path) also zeroes
	cur := imp.getSessionKey()
	imp.setSessionKey(nil)
	if !allZero(cur) {
		t.Errorf("session key not zeroed on reset: %q", cur)
	}
	if imp.getSessionKey() != nil {
		t.Error("session key should be nil after reset")
	}
}

func TestImplantWipeKeys(t *testing.T) {
	imp := newTestImplant(ImplantOptions{Quiet: true})
	k := []byte("0123456789abcdef0123456789abcdef")
	imp.setSessionKey(k)

	imp.wipeKeys()
	if !allZero(k) {
		t.Errorf("wipeKeys did not zero session key: %q", k)
	}
	if imp.getSessionKey() != nil {
		t.Error("session key should be nil after wipeKeys")
	}
}

func TestOperatorReconnectZeroesKeys(t *testing.T) {
	op := newTestOperator(t)

	cid := "cafe0123456789ab"
	oldSession := []byte("0123456789abcdef0123456789abcdef")
	oldPending := []byte("fedcba9876543210fedcba9876543210")

	op.mu.Lock()
	op.sessionKeys[cid] = oldSession
	op.pendingKeys[cid] = oldPending
	op.connectedClients[cid] = ClientInfo{SessionID: "old-session-id"}

	// Reconnect: same client_id, different session_id
	checkin := map[string]interface{}{
		"data": `{"client_id":"` + cid + `","session_id":"new-session-id",` +
			`"hostname":"target","os":"linux/amd64","user":"root","pid":1234}`,
	}
	job := op.handleCheckinLocked(checkin)
	_, hasSession := op.sessionKeys[cid]
	_, hasPending := op.pendingKeys[cid]
	op.mu.Unlock()

	if !allZero(oldSession) {
		t.Errorf("old active session key not zeroed on reconnect: %q", oldSession)
	}
	if !allZero(oldPending) {
		t.Errorf("old pending key not zeroed on reconnect: %q", oldPending)
	}
	if hasSession || hasPending {
		t.Error("old keys should be removed from maps after reconnect")
	}
	if job != nil {
		t.Error("no pubkey in checkin — expected nil kxJob")
	}
}

func TestOperatorWipeKeys(t *testing.T) {
	op := newTestOperator(t)

	k1 := []byte("0123456789abcdef0123456789abcdef")
	k2 := []byte("fedcba9876543210fedcba9876543210")
	op.mu.Lock()
	op.sessionKeys["client-a"] = k1
	op.pendingKeys["client-b"] = k2
	op.mu.Unlock()

	op.WipeKeys()

	if !allZero(k1) {
		t.Errorf("session key not zeroed by WipeKeys: %q", k1)
	}
	if !allZero(k2) {
		t.Errorf("pending key not zeroed by WipeKeys: %q", k2)
	}

	op.mu.RLock()
	nSession, nPending := len(op.sessionKeys), len(op.pendingKeys)
	op.mu.RUnlock()
	if nSession != 0 || nPending != 0 {
		t.Errorf("maps not cleared: sessionKeys=%d pendingKeys=%d", nSession, nPending)
	}
}

func TestQuietSuppressesStartupBanner(t *testing.T) {
	out := captureStdout(t, func() {
		NewImplantWithOptions(nil, "testkey",
			ImplantOptions{Interval: 20, Quiet: true})
	})
	if out != "" {
		t.Errorf("quiet constructor printed %q, want no output", out)
	}

	out = captureStdout(t, func() {
		NewImplantWithOptions(nil, "testkey",
			ImplantOptions{Interval: 20, Quiet: false})
	})
	if !strings.Contains(out, "Client ID") || !strings.Contains(out, "Interval") {
		t.Errorf("verbose constructor missing banner, got %q", out)
	}
}

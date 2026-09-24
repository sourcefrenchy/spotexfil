package c2

// zeroKey overwrites b's bytes in place so replaced/deleted session
// keys do not linger in the heap until GC. Best-effort: the runtime
// may hold copies (e.g. from slice growth), and Go strings (like the
// master passphrase) cannot be reliably wiped at all.
func zeroKey(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

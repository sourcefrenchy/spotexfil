// Package c2 provides the C2 implant and operator functionality.
package c2

// Module is the interface for C2 modules.
type Module interface {
	Name() string
	Execute(args map[string]interface{}) (status string, data string)
}

// Registry maps module names to module instances.
var registry = map[string]Module{}

// RegisterModule registers a C2 module.
func RegisterModule(m Module) {
	registry[m.Name()] = m
}

// GetModule returns a registered module by name.
func GetModule(name string) Module {
	return registry[name]
}

// ListModules returns all registered module names.
func ListModules() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

func init() {
	RegisterModule(&ShellModule{})
	RegisterModule(&ExfilModule{})
	RegisterModule(&SysinfoModule{})
}

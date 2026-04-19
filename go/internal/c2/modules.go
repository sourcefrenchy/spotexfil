// Package c2 provides the C2 implant and operator functionality.
package c2

import (
	"sync"

	"github.com/sourcefrenchy/spotexfil/internal/modapi"
)

// Module is the interface for C2 modules.
type Module = modapi.Module

// registry maps module names to module instances.
var (
	registry   = map[string]Module{}
	registryMu sync.RWMutex
)

// RegisterModule registers a C2 module.
func RegisterModule(m Module) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[m.Name()] = m
}

// UnregisterModule removes a module from the registry by name.
func UnregisterModule(name string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, name)
}

// GetModule returns a registered module by name.
func GetModule(name string) Module {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name]
}

// ListModules returns all registered module names.
func ListModules() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

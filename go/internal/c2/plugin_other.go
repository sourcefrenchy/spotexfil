//go:build !linux && !darwin

package c2

// LoadPlugins is a no-op on platforms that don't support Go plugins.
func LoadPlugins(dir string) error {
	return nil // plugins not supported on this platform
}

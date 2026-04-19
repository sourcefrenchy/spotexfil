package modapi

// Module defines the interface for C2 command modules.
type Module interface {
	Name() string
	Execute(args map[string]interface{}) (status string, data string)
}

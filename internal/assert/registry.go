package assert

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// A Factory builds one Assertion from its still-undecoded YAML params.
type Factory func(node *yaml.Node) (Assertion, error)

// registry maps assertion name -> the factory that builds it.
var registry = map[string]Factory{}

// Register is called from each assertion file's init(), e.g.:
//   func init() { Register("newest_snapshot_age_max", newSnapshotAge) }

func Register(name string, f Factory) {
	_, exists := registry[name]
	if exists { panic("The name you entered is already in registry.") }
	registry[name] = f
}

// Build looks up name and runs its factory against node.
func Build(name string, node *yaml.Node) (Assertion, error) {
	f, found := registry[name]
	if !found {
		return nil, fmt.Errorf("assertion %q not registered", name)
	}
	return f(node)
}

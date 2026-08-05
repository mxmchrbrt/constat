package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Targets []Target `yaml:"targets"`
}

type Target struct {
	Name    string      `yaml:"name"`
	Source  Source      `yaml:"source"`
	Restore Restore     `yaml:"restore"`
	Assert  []yaml.Node `yaml:"assert"`
}

type Source struct {
	Kind         string `yaml:"kind"`
	Repo         string `yaml:"repo"`
	PasswordFile string `yaml:"password_file"`
}

// Restore.Paths filters what comes out of the repository (restic --include
// semantics: prefix match against paths inside the snapshot). Schema only for
// now — session 3 wires it into driver.Driver.Restore; until then it is
// accepted but unused, not silently ignored.
type Restore struct {
	Paths []string `yaml:"paths"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := validate(&cfg, &root); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// targetNodes returns the sequence of target mapping nodes from the raw
// document tree, in the same order as Config.Targets, so validation errors
// can cite a line number.
func targetNodes(root *yaml.Node) []*yaml.Node {
	if root == nil || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "targets" {
			return doc.Content[i+1].Content
		}
	}
	return nil
}

func validate(cfg *Config, root *yaml.Node) error {
	if len(cfg.Targets) == 0 {
		return fmt.Errorf("config has no targets")
	}

	nodes := targetNodes(root)
	seen := make(map[string]int, len(cfg.Targets))

	for i, t := range cfg.Targets {
		line := 0
		if i < len(nodes) {
			line = nodes[i].Line
		}
		loc := targetLoc(i, t.Name, line)

		if t.Name == "" {
			return fmt.Errorf("%s: name is required", loc)
		}
		if prev, ok := seen[t.Name]; ok {
			return fmt.Errorf("%s: duplicate target name, first used at target %d", loc, prev)
		}
		seen[t.Name] = i

		if t.Source.Kind == "" {
			return fmt.Errorf("%s: source.kind is required", loc)
		}
		if t.Source.Repo == "" {
			return fmt.Errorf("%s: source.repo is required", loc)
		}
		if t.Source.PasswordFile == "" {
			return fmt.Errorf("%s: source.password_file is required", loc)
		}
		if len(t.Assert) == 0 {
			return fmt.Errorf("%s: assert list is empty, nothing to verify", loc)
		}
	}

	return nil
}

func targetLoc(index int, name string, line int) string {
	label := fmt.Sprintf("target %d", index)
	if name != "" {
		label = fmt.Sprintf("target %q", name)
	}
	if line > 0 {
		return fmt.Sprintf("%s (line %d)", label, line)
	}
	return label
}

package config

import (
	"fmt"
	"os"
	"time"

	"github.com/mxmchrbrt/constat/internal/safepath"
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

	// VerifyWith describes the disposable database the restored dump is loaded
	// into. Required only when an assertion needs a database; a target that
	// only checks files never starts a container.
	VerifyWith *VerifyWith `yaml:"verify_with"`

	// Timeout bounds the whole target: restore plus every assertion. Zero
	// means DefaultTimeout. A hung restic must not hang the run — failure
	// mode #10 is about time.
	Timeout time.Duration `yaml:"timeout"`
}

// DefaultTimeout applies when a target sets no timeout. Generous enough for a
// moderate restore, small enough that a 500 GB target forces the operator to
// set it explicitly and therefore to think about their RTO.
const DefaultTimeout = time.Hour

// EffectiveTimeout is t.Timeout, or DefaultTimeout when unset.
func (t Target) EffectiveTimeout() time.Duration {
	if t.Timeout <= 0 {
		return DefaultTimeout
	}
	return t.Timeout
}

type Source struct {
	Kind         string `yaml:"kind"`
	Repo         string `yaml:"repo"`
	PasswordFile string `yaml:"password_file"`
}

type Restore struct {
	// Paths filters what comes out of the repository (restic --include
	// semantics: prefix match against paths inside the snapshot).
	Paths []string `yaml:"paths"`

	// StripPrefix is the directory, as recorded in the backup, that assertion
	// paths should be written relative to. Backups store absolute paths and a
	// restore reproduces them, so a snapshot of /home/app/data lands at
	// <restoredir>/home/app/data and every assertion would otherwise have to
	// repeat that prefix. Set it once here and write `path_exists: config.php`.
	//
	// Deliberately not inferred from the snapshot: a snapshot can cover two
	// unrelated paths, and guessing wrong would move the root of every
	// assertion silently. A prefix that is not in the restored tree is an
	// error, not a failed verification.
	StripPrefix string `yaml:"strip_prefix"`
}

// VerifyWith is the disposable environment a dump is restored into. Proving a
// dump loads is the difference between this tool and a checksum: `restic check`
// can tell you the bytes are intact, and nothing but a real load tells you the
// database comes back.
type VerifyWith struct {
	// Image is the container image, e.g. postgres:16-alpine. Its version is
	// half of the version-mismatch check (taxonomy #9): a dump taken from 16
	// and loaded into 15 fails here, which is the only place it ever surfaces.
	Image string `yaml:"image"`

	// Load is the dump file, relative to the assertion root — so relative to
	// restore.strip_prefix when that is set. Streamed into the container on
	// stdin, so the container needs no mounts.
	Load string `yaml:"load"`
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
		if t.Timeout < 0 {
			return fmt.Errorf("%s: timeout must be positive, got %s", loc, t.Timeout)
		}
		if t.Restore.StripPrefix != "" {
			if _, err := safepath.RelFromBackupPath(t.Restore.StripPrefix); err != nil {
				return fmt.Errorf("%s: restore.strip_prefix: %w", loc, err)
			}
		}
		if v := t.VerifyWith; v != nil {
			if v.Image == "" {
				return fmt.Errorf("%s: verify_with.image is required", loc)
			}
			if v.Load == "" {
				return fmt.Errorf("%s: verify_with.load is required", loc)
			}
			// The dump path comes out of the restored tree, so it gets the
			// same treatment as an assertion path: no absolute paths, no
			// escaping the restore root.
			if _, err := safepath.ValidateRelPath(v.Load); err != nil {
				return fmt.Errorf("%s: verify_with.load: %w", loc, err)
			}
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

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

	// Posted the run's report on completion, pass and fail both. Top-level
	// rather than per-target: alerting is about the run, not one target.
	Webhook *Webhook `yaml:"webhook"`

	// Signs the report when set. Optional: an unsigned report is a
	// legitimate output for an operator who just wants a pass/fail answer.
	Signing *Signing `yaml:"signing"`
}

// Signing names the ed25519 private key used to sign reports — a path, never
// the key itself.
type Signing struct {
	KeyFile string `yaml:"key_file"`
}

type Target struct {
	Name    string      `yaml:"name"`
	Source  Source      `yaml:"source"`
	Restore Restore     `yaml:"restore"`
	Assert  []yaml.Node `yaml:"assert"`

	// The disposable database the restored dump is loaded into. Required
	// only when an assertion needs one.
	VerifyWith *VerifyWith `yaml:"verify_with"`

	// Bounds the whole target: restore plus every assertion. Zero means
	// DefaultTimeout.
	Timeout time.Duration `yaml:"timeout"`
}

// DefaultTimeout applies when a target sets no timeout.
const DefaultTimeout = time.Hour

// EffectiveTimeout is t.Timeout, or DefaultTimeout when unset.
func (t Target) EffectiveTimeout() time.Duration {
	if t.Timeout <= 0 {
		return DefaultTimeout
	}
	return t.Timeout
}

// Webhook mirrors internal/webhook.Config in YAML. Duplicated rather than
// imported so config stays dependency-free.
type Webhook struct {
	URL string `yaml:"url"`

	// generic (default), ntfy, healthchecks, or discord. Unvalidated here,
	// like source.kind — config has no dependency on internal/webhook, so an
	// unrecognised format is caught where the webhook is actually sent.
	Format string `yaml:"format"`

	Timeout     time.Duration `yaml:"timeout"`
	MaxAttempts int           `yaml:"max_attempts"`
}

type Source struct {
	Kind         string `yaml:"kind"`
	Repo         string `yaml:"repo"`
	PasswordFile string `yaml:"password_file"`
}

type Restore struct {
	// restic --include semantics: prefix match against paths inside the
	// snapshot.
	Paths []string `yaml:"paths"`

	// The directory, as recorded in the backup, that assertion paths are
	// written relative to. Backups store absolute paths and a restore
	// reproduces them verbatim, so this saves every assertion from repeating
	// the full original path.
	//
	// Not inferred from the snapshot: a snapshot can cover two unrelated
	// paths, and a wrong guess would move every assertion's root silently.
	// A prefix not in the restored tree is an error, not a failed
	// verification.
	StripPrefix string `yaml:"strip_prefix"`
}

// VerifyWith is the disposable database a dump is restored into.
type VerifyWith struct {
	// e.g. postgres:16-alpine. Its version is half of the version-mismatch
	// check (taxonomy #9).
	Image string `yaml:"image"`

	// The dump file, relative to the assertion root. Streamed to the
	// container on stdin, so it needs no mounts.
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

	if w := cfg.Webhook; w != nil {
		if w.URL == "" {
			return fmt.Errorf("webhook.url is required")
		}
		if w.Timeout < 0 {
			return fmt.Errorf("webhook.timeout must be positive, got %s", w.Timeout)
		}
		if w.MaxAttempts < 0 {
			return fmt.Errorf("webhook.max_attempts must not be negative, got %d", w.MaxAttempts)
		}
	}

	if s := cfg.Signing; s != nil {
		if s.KeyFile == "" {
			return fmt.Errorf("signing.key_file is required when a signing block is present")
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

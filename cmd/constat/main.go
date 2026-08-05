package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Targets []Target `yaml:"targets"`
}

type Target struct {
	Name   string `yaml:"name"`
	Source Source `yaml:"source"`
	Assert Assert `yaml:"assert"`
}

type Source struct {
	Kind         string `yaml:"kind"`
	Repo         string `yaml:"repo"`
	PasswordFile string `yaml:"password_file"`
}

type Assert struct {
	NewestSnapshotAgeMax time.Duration `yaml:"newest_snapshot_age_max"`
}

type Snapshot struct {
	ID      string    `json:"id"`
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &cfg, nil
}

func newestSnapshot(src Source) (*Snapshot, error) {
	cmd := exec.Command("restic",
		"-r", src.Repo,
		"--password-file", src.PasswordFile,
		"--json",
		"snapshots",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("restic snapshots: %w (output: %s)", err, out)
	}

	var snapshots []Snapshot
	if err := json.Unmarshal(out, &snapshots); err != nil {
		return nil, fmt.Errorf("parsing restic output: %w", err)
	}

	if len(snapshots) == 0 {
		return nil, fmt.Errorf("no snapshots in repository")
	}

	newest := snapshots[0]
	for _, s := range snapshots {
		if s.Time.After(newest.Time) {
			newest = s
		}
	}
	return &newest, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: constat <config.yaml>")
		os.Exit(1)
	}

	cfg, err := loadConfig(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	failed := false

	for _, t := range cfg.Targets {
		snap, err := newestSnapshot(t.Source)
		if err != nil {
			fmt.Printf("ERROR %s: %v\n", t.Name, err)
			failed = true
			continue
		}

		age := time.Since(snap.Time).Round(time.Second)
		max_age := t.Assert.NewestSnapshotAgeMax

		if age > max_age {
			fmt.Printf("FAIL  %s: newest snapshot %s is %s old (max %s)\n",
				t.Name, snap.ShortID, age, max_age)
			failed = true
		} else {
			fmt.Printf("PASS  %s: newest snapshot %s is %s old (max %s)\n",
				t.Name, snap.ShortID, age, max_age)
		}
	}

	if failed {
		os.Exit(1)
	}
}

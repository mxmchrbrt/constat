package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Paths    []string  `json:"paths"`
}

func main() {
	cmd := exec.Command("restic",
		"-r", "/home/user/claude-workspace/constat-lab/repo",
		"--password-file", "/home/user/claude-workspace/constat-lab/pass",
		"--json",
		"snapshots",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "restic failed: %v\noutput: %s\n", err, out)
		os.Exit(1)
	}

	var snapshots []Snapshot
	if err := json.Unmarshal(out, &snapshots); err != nil {
		fmt.Fprintf(os.Stderr, "parsing restic output: %v\n", err)
		os.Exit(1)
	}

	if len(snapshots) == 0 {
		fmt.Fprintln(os.Stderr, "no snapshots in repository")
		os.Exit(1)
	}

	newest := snapshots[0]
	for _, s := range snapshots {
		if s.Time.After(newest.Time) {
			newest = s
		}
	}

	age := time.Since(newest.Time)
	fmt.Printf("newest snapshot: %s\n", newest.ShortID)
	fmt.Printf("taken:           %s\n", newest.Time.Format(time.RFC3339))
	fmt.Printf("age:             %s\n", age.Round(time.Second))
}

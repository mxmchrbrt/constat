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
	Name   string      `yaml:"name"`
	Source Source      `yaml:"source"`
	Assert []yaml.Node `yaml:"assert"`
}

type Source struct {
	Kind         string `yaml:"kind"`
	Repo         string `yaml:"repo"`
	PasswordFile string `yaml:"password_file"`
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
	return &cfg, nil
}

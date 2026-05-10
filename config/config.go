package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type APIConfig struct {
	Name     string   `yaml:"name" json:"name"`
	Key      string   `yaml:"key" json:"key"`
	Trackers []string `yaml:"trackers" json:"trackers"`
}

type TrackerConfig struct {
	Type      string   `yaml:"type,omitempty" json:"type,omitempty"`
	Enable    bool     `yaml:"enable" json:"enable"`
	Domain    string   `yaml:"domain,omitempty" json:"domain,omitempty"`
	AltDomain string   `yaml:"alt_domain,omitempty" json:"alt_domain,omitempty"`
	Categories []string `yaml:"categories" json:"categories"`
	Username   string   `yaml:"username,omitempty" json:"username,omitempty"`
	Password   string   `yaml:"password,omitempty" json:"password,omitempty"`
}

type Config struct {
	Port         string                   `yaml:"port" json:"port"`
	PingInterval int                      `yaml:"ping_interval" json:"ping_interval"`
	APIs         []APIConfig              `yaml:"apis" json:"apis"`
	Trackers     map[string]TrackerConfig `yaml:"trackers" json:"trackers"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Port == "" {
		cfg.Port = "9117"
	}
	if cfg.PingInterval < 1 {
		cfg.PingInterval = 10
	}
	if cfg.APIs == nil {
		cfg.APIs = []APIConfig{}
	}
	if cfg.Trackers == nil {
		cfg.Trackers = map[string]TrackerConfig{}
	}

	return &cfg, nil
}

func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	return nil
}
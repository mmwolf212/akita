package config

import (
	"time"
	"os"
	"flag"
	"fmt"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Logs          LogsConfig          `yaml:"logs"`
	Watch         WatchConfig         `yaml:"watch"`
	Pipeline      PipelineConfig      `yaml:"pipeline"`
	Alerts        AlertsConfig        `yaml:"alerts"`
	State         StateConfig         `yaml:"state"`
	Observability ObservabilityConfig `yaml:"observability"`
}

type LogsConfig struct {
	Discovery       string        `yaml:"discovery"`
	Pin             []string      `yaml:"pin"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

type WatchConfig struct {
	Domains   []string        `yaml:"domains"`
	Typosquat TyposquatConfig `yaml:"typosquat"`
}

type TyposquatConfig struct {
	Enabled         bool     `yaml:"enabled"`
	Homoglyphs      bool     `yaml:"homoglyphs"`
	TLDSwaps        []string `yaml:"tld_swaps"`
	MaxEditDistance int      `yaml:"max_edit_distance"`
}

type PipelineConfig struct {
	BatchSize      int           `yaml:"batch_size"`
	PollInterval   time.Duration `yaml:"poll_interval"`
	MatcherWorkers int           `yaml:"matcher_workers"`
	ChannelBuffer  int           `yaml:"channel_buffer"`
	DedupTTL       time.Duration `yaml:"dedup_ttl"`
}

type AlertsConfig struct {
	Stdout  bool          `yaml:"stdout"`
	JSONL   JSONLConfig   `yaml:"jsonl"`
	Webhook WebhookConfig `yaml:"webhook"`
}

type JSONLConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type WebhookConfig struct {
	Enabled bool          `yaml:"enabled"`
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
	Retries int           `yaml:"retries"`
}

type StateConfig struct {
	Path string `yaml:"path"`
}

type ObservabilityConfig struct {
	HTTPAddr  string `yaml:"http_addr"`
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`
}

// Load reads configuration with precedence: flags > env > file > defaults.
func Load() (*Config, error) {
	cfg := Defaults()

	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	if *configPath == "" {
		*configPath = os.Getenv("AKITA_CONFIG")
	}

	if *configPath != "" {
		return loadFromFile(*configPath)
	}

	if len(cfg.Watch.Domains) == 0 {
		return nil, fmt.Errorf("watch.domains must not be empty")
	}

	return &cfg, nil
}

func loadFromFile(path string) (*Config, error) {
	cfg := Defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if len(cfg.Watch.Domains) == 0 {
		return nil, fmt.Errorf("watch.domains must not be empty")
	}

	return &cfg, nil
}

// Defaults returns a Config populated with sane default values.
func Defaults() Config {
	return Config{
		Logs: LogsConfig{
			Discovery: "dynamic",
			RefreshInterval: 12 * time.Hour,
		},
		Pipeline: PipelineConfig{
			BatchSize: 256,
			PollInterval: 10 * time.Second,
			MatcherWorkers: 4, 
			ChannelBuffer: 4096,
			DedupTTL: 24 * time.Hour,
		},
		Alerts: AlertsConfig{
			Stdout: true,
			Webhook: WebhookConfig{
				Timeout: 5 * time.Second,
				Retries: 3,
			},
		},
		State: StateConfig{
			Path: "./akita.state",
		},
		Observability: ObservabilityConfig {
			HTTPAddr: ":9090",
			LogLevel: "info",
			LogFormat: "json",
		},
	}
}

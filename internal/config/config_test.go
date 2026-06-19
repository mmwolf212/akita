package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()

	if cfg.Logs.Discovery != "dynamic"{ t.Error("Discovery Logs not set as dynamic.")}
	if cfg.Pipeline.BatchSize != 256 {t.Error("Batch Size not 256.")}
	if cfg.State.Path != "./akita.state" {t.Error("Chcek state path.")}
	if cfg.Pipeline.DedupTTL != 24 * time.Hour {t.Error("Check Dedup interval.")}
	if cfg.Alerts.Stdout !=true {t.Error("Stdout alert not enabled by default.")}
}

func TestLoadFromFile(t *testing.T) {
	yaml := []byte(`
watch:
  domains: [example.com, example.org]
pipeline:
  batch_size: 512
`)
	path := writeTempConfig(t, yaml)

	cfg, err := loadFromFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Watch.Domains) != 2 {
		t.Fatalf("Watch.Domains length = %d, want 2", len(cfg.Watch.Domains))
	}
	if cfg.Watch.Domains[0] != "example.com" {
		t.Errorf("Watch.Domains[0] = %q, want %q", cfg.Watch.Domains[0], "example.com")
	}
	if cfg.Pipeline.BatchSize != 512 {
		t.Errorf("Pipeline.BatchSize = %d, want 512", cfg.Pipeline.BatchSize)
	}

	if cfg.Pipeline.MatcherWorkers != 4 {
		t.Errorf("Pipeline.MatcherWorkers = %d, want 4 (default)", cfg.Pipeline.MatcherWorkers)
	}
	if cfg.Observability.LogLevel != "info" {
		t.Errorf("Observability.LogLevel = %q, want %q (default)", cfg.Observability.LogLevel, "info")
	}
}

func TestLoadFromFileMissingDomains(t *testing.T) {
	yaml := []byte(`
pipeline:
  batch_size: 512
`)
	path := writeTempConfig(t, yaml)

	_, err := loadFromFile(path)
	if err == nil {
		t.Fatal("expected error when domains are missing, got nil")
	}
}

func TestLoadFromFileNotFound(t *testing.T) {
	_, err := loadFromFile("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestLoadFromFileInvalidYAML(t *testing.T) {
	path := writeTempConfig(t, []byte(`{{{not yaml`))

	_, err := loadFromFile(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// writeTempConfig writes data to a temp file and returns its path.
// The file is automatically cleaned up when the test finishes.
func writeTempConfig(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}


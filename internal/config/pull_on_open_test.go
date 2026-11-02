package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPullOnOpenIsOnByDefault(t *testing.T) {
	cfg := validBase()

	if !cfg.PullOnOpenEnabled() {
		t.Fatal("a configuration without a sync section must pull on open")
	}
}

func TestPullOnOpenCanBeSwitchedOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	yaml := `interval: "1m"
repositories:
  - path: "/repo"
commit:
  message: "msg"
sync:
  pull_on_open: false
editors:
  - goland
`

	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.PullOnOpenEnabled() {
		t.Fatal("pull_on_open: false must switch the pull off")
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPullOnOpenCanBeSetExplicitlyOn(t *testing.T) {
	on := true
	cfg := validBase()
	cfg.Sync.PullOnOpen = &on

	if !cfg.PullOnOpenEnabled() {
		t.Fatal("pull_on_open: true must keep the pull on")
	}
}

func TestExampleConfigShowsThePullSetting(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Sync.PullOnOpen == nil || !*cfg.Sync.PullOnOpen {
		t.Fatal("expected the example to show pull_on_open: true")
	}
}

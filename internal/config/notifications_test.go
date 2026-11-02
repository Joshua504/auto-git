package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNotificationsAreOffByDefault(t *testing.T) {
	cfg := validBase()

	if cfg.NotificationsEnabled() {
		t.Fatal("a configuration without a notifications section must have notifications off")
	}
}

func TestNotificationsCanBeSwitchedOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	yaml := `interval: "1m"
repositories:
  - path: "/repo"
commit:
  message: "msg"
notifications:
  enabled: true
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

	if !cfg.NotificationsEnabled() {
		t.Fatal("notifications.enabled: true must switch notifications on")
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationsCanBeSetExplicitlyOff(t *testing.T) {
	off := false
	cfg := validBase()
	cfg.Notifications.Enabled = &off

	if cfg.NotificationsEnabled() {
		t.Fatal("notifications.enabled: false must keep notifications off")
	}
}

func TestExampleConfigShowsNotificationsOff(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Notifications.Enabled == nil || *cfg.Notifications.Enabled {
		t.Fatal("expected the example to show notifications.enabled: false")
	}
}

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

func TestNotificationEventEnabled_FallsBackToGlobal(t *testing.T) {
	on := true
	cfg := validBase()
	cfg.Notifications.Enabled = &on

	// No per-event override — should inherit global.
	for _, event := range AllNotificationEvents {
		if !cfg.NotificationEventEnabled(event) {
			t.Errorf("event %s should be enabled via global flag", event)
		}
	}
}

func TestNotificationEventEnabled_ExplicitOverride(t *testing.T) {
	on := true
	off := false
	cfg := validBase()
	cfg.Notifications.Enabled = &on
	cfg.Notifications.Events = map[string]*bool{
		"backup_success": &off, // explicitly disable success notifications
	}

	if cfg.NotificationEventEnabled(NotificationBackupSuccess) {
		t.Error("backup_success should be disabled by explicit override")
	}

	// Other events should still inherit the global on.
	if !cfg.NotificationEventEnabled(NotificationBackupFailure) {
		t.Error("backup_failure should be enabled via global flag")
	}
}

func TestNotificationEventEnabled_ExplicitOnWhenGlobalOff(t *testing.T) {
	off := false
	on := true
	cfg := validBase()
	cfg.Notifications.Enabled = &off
	cfg.Notifications.Events = map[string]*bool{
		"backup_failure": &on, // only allow failure notifications
	}

	if cfg.NotificationEventEnabled(NotificationBackupSuccess) {
		t.Error("backup_success should be off (global off, no override)")
	}

	if !cfg.NotificationEventEnabled(NotificationBackupFailure) {
		t.Error("backup_failure should be on (explicit override)")
	}
}

func TestNotificationEventEnabled_LoadFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	yaml := `interval: "1m"
repositories:
  - path: "/repo"
commit:
  message: "msg"
notifications:
  enabled: true
  events:
    backup_success: false
    queue_sync: true
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

	if cfg.NotificationEventEnabled(NotificationBackupSuccess) {
		t.Error("backup_success should be disabled by YAML override")
	}

	if !cfg.NotificationEventEnabled(NotificationQueueSync) {
		t.Error("queue_sync should be enabled by YAML override")
	}

	// branch_diverged not in events map — should inherit global (true).
	if !cfg.NotificationEventEnabled(NotificationBranchDiverged) {
		t.Error("branch_diverged should inherit global enabled flag")
	}
}

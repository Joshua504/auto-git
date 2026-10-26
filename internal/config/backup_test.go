package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validBase is a configuration that passes validation before any backup settings.
func validBase() Config {
	return Config{
		Interval:     "1m",
		Repositories: []Repository{{Path: "/repo"}},
		Commit:       CommitConfig{Message: "msg"},
		Editors:      []string{"goland"},
	}
}

func TestBackupSectionIsOptional(t *testing.T) {
	if err := validBase().Validate(); err != nil {
		t.Fatalf("a configuration without a backup section must stay valid: %v", err)
	}
}

func TestBackupModeValidation(t *testing.T) {
	for _, mode := range []string{"", "commit", "branch"} {
		cfg := validBase()
		cfg.Backup.Mode = mode

		if err := cfg.Validate(); err != nil {
			t.Errorf("mode %q should be valid: %v", mode, err)
		}
	}

	for _, mode := range []string{"branches", "Branch", "snapshot", " branch"} {
		cfg := validBase()
		cfg.Backup.Mode = mode

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "backup.mode") {
			t.Errorf("mode %q should be rejected with a message naming backup.mode, got: %v", mode, err)
		}
	}
}

func TestBackupPrefixValidation(t *testing.T) {
	valid := []string{"", "autogit", "backups/wip", "my-backups", "a.b_c"}

	for _, prefix := range valid {
		cfg := validBase()
		cfg.Backup.Prefix = prefix

		if err := cfg.Validate(); err != nil {
			t.Errorf("prefix %q should be valid: %v", prefix, err)
		}
	}

	invalid := []string{
		"heads", "heads/x", "tags", "remotes/origin", "stash", // Git's own namespaces
		"a..b", "/lead", "trail/", "a//b", "bad prefix", "x.lock", "a/b.lock/c", "bad~name", ".hidden",
	}

	for _, prefix := range invalid {
		cfg := validBase()
		cfg.Backup.Prefix = prefix

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "backup.prefix") {
			t.Errorf("prefix %q should be rejected with a message naming backup.prefix, got: %v", prefix, err)
		}
	}
}

func TestBackupMachineValidation(t *testing.T) {
	for _, machine := range []string{"", "laptop", "my-pc.1", "Work_Laptop"} {
		cfg := validBase()
		cfg.Backup.Machine = machine

		if err := cfg.Validate(); err != nil {
			t.Errorf("machine %q should be valid: %v", machine, err)
		}
	}

	for _, machine := range []string{"my pc", "-lead", "a/b", "name?"} {
		cfg := validBase()
		cfg.Backup.Machine = machine

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "backup.machine") {
			t.Errorf("machine %q should be rejected with a message naming backup.machine, got: %v", machine, err)
		}
	}
}

func TestBackupSectionIsReadFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	yaml := `interval: "1m"
repositories:
  - path: "/repo"
commit:
  message: "msg"
backup:
  mode: "branch"
  prefix: "snapshots"
  machine: "desk"
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

	want := BackupConfig{Mode: "branch", Prefix: "snapshots", Machine: "desk"}
	if cfg.Backup != want {
		t.Fatalf("backup section = %+v, want %+v", cfg.Backup, want)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The example is what people copy, so it should show the recommended mode.
func TestExampleConfigUsesBranchMode(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Backup.Mode != "branch" {
		t.Fatalf("expected the example to use branch mode, got %q", cfg.Backup.Mode)
	}
}

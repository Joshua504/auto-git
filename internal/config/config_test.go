package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	content := `
interval: 1h

repositories:
  - path: /home/gamp/project1
  - path: /home/gamp/project2

commit:
  message: "Auto backup"
`

	file := t.TempDir() + "/config.yaml"

	err := os.WriteFile(file, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	config, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}

	if config.Interval != "1h" {
		t.Errorf("expected interval %q, got %q", "1h", config.Interval)
	}

	if len(config.Repositories) != 2 {
		t.Errorf("expected 2 repositories, got %d", len(config.Repositories))
	}

	if config.Repositories[0].Path != "/home/gamp/project1" {
		t.Errorf("unexpected first repository path: %q", config.Repositories[0].Path)
	}

	if config.Commit.Message != "Auto backup" {
		t.Errorf("unexpected commit message: %q", config.Commit.Message)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/path/that/does/not/exist/config.yaml")

	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	content := `
interval: 1h
repositories:
  - path: /home/gamp/project1
  - path: [invalid
`

	file := t.TempDir() + "/config.yaml"

	err := os.WriteFile(file, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Load(file)

	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestValidateMissingInterval(t *testing.T) {
	config := Config{
		Interval: "",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{
			Message: "Auto backup",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for missing interval, got nil")
	}
}

func TestValidateMissingRepositories(t *testing.T) {
	config := Config{
		Interval:     "1h",
		Repositories: []Repository{},
		Commit: CommitConfig{
			Message: "Auto backup",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for missing repositories, got nil")
	}
}

func TestValidateMissingRepositoryPath(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: ""},
		},
		Commit: CommitConfig{
			Message: "Auto backup",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for an empty repository path, got nil")
	}
}

func TestValidateMissingCommitMessage(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{
			Message: "",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for missing commit message, got nil")
	}
}

func TestValidateValidConfig(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
			{Path: "/home/gamp/project2"},
		},
		Commit: CommitConfig{
			Message: "Auto backup",
		},
	}

	err := config.Validate()

	if err != nil {
		t.Fatalf("expected valid configuration, got error: %v", err)
	}
}

func TestValidateInvalidInterval(t *testing.T) {
	config := Config{
		Interval: "banana",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{
			Message: "Auto backup",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for invalid interval, got nil")
	}
}

func TestValidateZeroInterval(t *testing.T) {
	config := Config{
		Interval: "0s",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{
			Message: "Auto backup",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for zero interval, got nil")
	}
}

func TestValidateNegativeInterval(t *testing.T) {
	config := Config{
		Interval: "-1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{
			Message: "Auto backup",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for negative interval, got nil")
	}
}

func TestValidateWhitespaceRepositoryPath(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "   "},
		},
		Commit: CommitConfig{
			Message: "Auto backup",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for whitespace-only repository path, got nil")
	}
}

func TestValidateWhitespaceCommitMessage(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{
			Message: "   ",
		},
	}

	err := config.Validate()

	if err == nil {
		t.Fatal("expected an error for whitespace-only commit message, got nil")
	}
}

// --- Schedule config validation tests ---

func TestValidateScheduleEmptyPolicy(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("expected empty schedule to be valid, got: %v", err)
	}
}

func TestValidateScheduleFixedPolicy(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{Policy: "fixed"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("expected fixed policy to be valid, got: %v", err)
	}
}

func TestValidateScheduleSmartPolicy(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{Policy: "smart"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("expected smart policy to be valid, got: %v", err)
	}
}

func TestValidateScheduleHybridPolicy(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{Policy: "hybrid"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("expected hybrid policy to be valid, got: %v", err)
	}
}

func TestValidateScheduleInvalidPolicy(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{Policy: "invalid"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for invalid policy, got nil")
	}
}

func TestValidateScheduleInvalidInterval(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{Interval: "banana"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for invalid schedule interval, got nil")
	}
}

func TestValidateScheduleInvalidActivityWindow(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{ActivityWindow: "banana"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for invalid activity_window, got nil")
	}
}

func TestValidateScheduleInvalidSafetyInterval(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{SafetyInterval: "banana"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for invalid safety_interval, got nil")
	}
}

func TestValidateScheduleNegativeSafetyInterval(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{SafetyInterval: "-1h"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for negative safety_interval, got nil")
	}
}

func TestValidateScheduleInvalidMinInterval(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{MinInterval: "banana"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for invalid min_interval, got nil")
	}
}

func TestValidateScheduleNegativeMinInterval(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1", Schedule: ScheduleConfig{MinInterval: "-1h"}},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for negative min_interval, got nil")
	}
}

func TestValidateScheduleFullConfig(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{
				Path: "/home/gamp/project1",
				Schedule: ScheduleConfig{
					Policy:         "smart",
					Interval:       "5m",
					ActivityWindow: "2h",
					SafetyInterval: "48h",
				},
			},
		},
		Commit: CommitConfig{Message: "Auto backup"},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("expected full schedule config to be valid, got: %v", err)
	}
}

func TestLoadScheduleConfig(t *testing.T) {
	content := `
interval: 1h

repositories:
  - path: /home/gamp/project1
    schedule:
      policy: "smart"
      interval: "5m"
      activity_window: "2h"
      safety_interval: "48h"

commit:
  message: "Auto backup"
`

	file := t.TempDir() + "/config.yaml"

	err := os.WriteFile(file, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Repositories) != 1 {
		t.Fatalf("expected 1 repository, got %d", len(cfg.Repositories))
	}

	sched := cfg.Repositories[0].Schedule
	if sched.Policy != "smart" {
		t.Errorf("expected policy smart, got %q", sched.Policy)
	}
	if sched.Interval != "5m" {
		t.Errorf("expected interval 5m, got %q", sched.Interval)
	}
	if sched.ActivityWindow != "2h" {
		t.Errorf("expected activity_window 2h, got %q", sched.ActivityWindow)
	}
	if sched.SafetyInterval != "48h" {
		t.Errorf("expected safety_interval 48h, got %q", sched.SafetyInterval)
	}
}

func TestPowerCfgBatteryIntervalEnabled(t *testing.T) {
	cfg := PowerCfg{BatteryInterval: "15m"}
	if !cfg.BatteryIntervalEnabled() {
		t.Error("expected BatteryIntervalEnabled to be true")
	}

	cfg2 := PowerCfg{}
	if cfg2.BatteryIntervalEnabled() {
		t.Error("expected BatteryIntervalEnabled to be false when empty")
	}
}

func TestPowerCfgLowBatteryThreshold(t *testing.T) {
	cfg := PowerCfg{LowBatteryPercent: 20}
	if got := cfg.LowBatteryThreshold(); got != 20 {
		t.Errorf("expected threshold 20, got %d", got)
	}

	cfg2 := PowerCfg{}
	if got := cfg2.LowBatteryThreshold(); got != 0 {
		t.Errorf("expected threshold 0, got %d", got)
	}
}

func TestPowerCfgValidateValid(t *testing.T) {
	cfg := PowerCfg{BatteryInterval: "15m", LowBatteryPercent: 20}
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestPowerCfgValidateEmpty(t *testing.T) {
	cfg := PowerCfg{}
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected empty config to be valid, got error: %v", err)
	}
}

func TestPowerCfgValidateInvalidBatteryInterval(t *testing.T) {
	cfg := PowerCfg{BatteryInterval: "not-a-duration"}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for invalid battery_interval, got nil")
	}
}

func TestPowerCfgValidateNegativeLowBatteryPercent(t *testing.T) {
	cfg := PowerCfg{LowBatteryPercent: -10}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for negative low_battery_percent, got nil")
	}
}

func TestPowerCfgValidateHighLowBatteryPercent(t *testing.T) {
	cfg := PowerCfg{LowBatteryPercent: 101}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for low_battery_percent > 100, got nil")
	}
}

func TestLoadPowerConfig(t *testing.T) {
	content := `
interval: 1h

repositories:
  - path: /home/gamp/project1

commit:
  message: "Auto backup"

power:
  battery_interval: "15m"
  low_battery_percent: 20
`

	file := t.TempDir() + "/config.yaml"

	err := os.WriteFile(file, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Power.BatteryInterval != "15m" {
		t.Errorf("expected battery_interval 15m, got %q", cfg.Power.BatteryInterval)
	}
	if cfg.Power.LowBatteryPercent != 20 {
		t.Errorf("expected low_battery_percent 20, got %d", cfg.Power.LowBatteryPercent)
	}
}

func TestValidateWithPowerConfig(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{Message: "Auto backup"},
		Power: PowerCfg{
			BatteryInterval:   "15m",
			LowBatteryPercent: 20,
		},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("expected valid config with power settings, got error: %v", err)
	}
}

func TestValidateWithInvalidPowerConfig(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{Message: "Auto backup"},
		Power: PowerCfg{
			BatteryInterval: "not-a-duration",
		},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for invalid power config, got nil")
	}
}

// --- Verification config tests ---

func TestVerificationIsOffByDefault(t *testing.T) {
	cfg := VerificationCfg{}
	if cfg.VerificationEnabled() {
		t.Error("expected verification to be off by default")
	}
}

func TestVerificationCanBeSwitchedOn(t *testing.T) {
	enabled := true
	cfg := VerificationCfg{Enabled: &enabled}
	if !cfg.VerificationEnabled() {
		t.Error("expected verification to be enabled")
	}
}

func TestVerificationCanBeSetExplicitlyOff(t *testing.T) {
	disabled := false
	cfg := VerificationCfg{Enabled: &disabled}
	if cfg.VerificationEnabled() {
		t.Error("expected verification to be disabled")
	}
}

func TestVerificationIntervalDefault(t *testing.T) {
	cfg := VerificationCfg{}
	if got := cfg.VerificationInterval(); got != 24*60*time.Minute {
		t.Errorf("expected default interval 24h, got %v", got)
	}
}

func TestVerificationIntervalCustom(t *testing.T) {
	cfg := VerificationCfg{Interval: "12h"}
	if got := cfg.VerificationInterval(); got != 12*60*time.Minute {
		t.Errorf("expected interval 12h, got %v", got)
	}
}

func TestVerificationValidateValid(t *testing.T) {
	enabled := true
	cfg := VerificationCfg{Enabled: &enabled, Interval: "12h"}
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestVerificationValidateEmpty(t *testing.T) {
	cfg := VerificationCfg{}
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected empty config to be valid, got error: %v", err)
	}
}

func TestVerificationValidateInvalidInterval(t *testing.T) {
	cfg := VerificationCfg{Interval: "not-a-duration"}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for invalid verification interval, got nil")
	}
}

func TestLoadVerificationConfig(t *testing.T) {
	content := `
interval: 1h

repositories:
  - path: /home/gamp/project1

commit:
  message: "Auto backup"

verification:
  enabled: true
  interval: "12h"
`

	file := t.TempDir() + "/config.yaml"

	err := os.WriteFile(file, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.Verification.VerificationEnabled() {
		t.Error("expected verification to be enabled")
	}
	if cfg.Verification.Interval != "12h" {
		t.Errorf("expected interval 12h, got %q", cfg.Verification.Interval)
	}
}

func TestValidateWithVerificationConfig(t *testing.T) {
	enabled := true
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{Message: "Auto backup"},
		Verification: VerificationCfg{
			Enabled:  &enabled,
			Interval: "12h",
		},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("expected valid config with verification settings, got error: %v", err)
	}
}

func TestValidateWithInvalidVerificationConfig(t *testing.T) {
	config := Config{
		Interval: "1h",
		Repositories: []Repository{
			{Path: "/home/gamp/project1"},
		},
		Commit: CommitConfig{Message: "Auto backup"},
		Verification: VerificationCfg{
			Interval: "not-a-duration",
		},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("expected error for invalid verification config, got nil")
	}
}

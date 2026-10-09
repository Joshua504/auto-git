package config

import (
	"os"
	"testing"
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

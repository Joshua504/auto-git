package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configWithMessage(message string) Config {
	cfg := validBase()
	cfg.Commit.Message = message

	return cfg
}

func TestCommitMessageTemplatesAreAccepted(t *testing.T) {
	valid := []string{
		"chore: automatic backup",
		"backup: {summary}",
		"{summary} ({datetime})",
		"{branch}@{host} {repo}: {files} files on {date} at {time}",
		"backup: {summary}\n\n{files} file(s) saved on {host}",
		"text with { spaces in braces } is ordinary text",
		"a lone { brace",
		"empty {} braces",
	}

	for _, message := range valid {
		if err := configWithMessage(message).Validate(); err != nil {
			t.Errorf("message %q should be valid: %v", message, err)
		}
	}
}

func TestUnknownPlaceholderStopsAutoGitAtStartup(t *testing.T) {
	tests := map[string]string{
		"backup: {sumary}":      "sumary",
		"{Summary}":             "Summary",
		"{summary} and {nope}":  "nope",
		"{datetime}{timestamp}": "timestamp",
	}

	for message, name := range tests {
		err := configWithMessage(message).Validate()
		if err == nil {
			t.Errorf("message %q should be rejected", message)

			continue
		}

		// The error names the mistake and lists what is allowed.
		for _, want := range []string{"{" + name + "}", "commit.message", "{summary}", "{datetime}"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("message %q: error %q should mention %q", message, err, want)
			}
		}
	}
}

func TestMessagePlaceholdersReturnsACopy(t *testing.T) {
	names := MessagePlaceholders()
	names[0] = "changed"

	if MessagePlaceholders()[0] == "changed" {
		t.Fatal("callers must not be able to change the list")
	}
}

func TestExampleConfigUsesAMessageTemplate(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(cfg.Commit.Message, "{summary}") {
		t.Fatalf("expected the example to show a template, got %q", cfg.Commit.Message)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMessageTemplateIsReadFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	yaml := `interval: "1m"
repositories:
  - path: "/repo"
commit:
  message: |
    backup: {summary}

    {files} file(s) on {host}
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

	if err := cfg.Validate(); err != nil {
		t.Fatalf("a multi-line template should be valid: %v", err)
	}

	if !strings.Contains(cfg.Commit.Message, "backup: {summary}") || !strings.Contains(cfg.Commit.Message, "{files} file(s) on {host}") {
		t.Fatalf("the multi-line message was not read correctly: %q", cfg.Commit.Message)
	}
}

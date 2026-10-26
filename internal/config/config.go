package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Interval     string       `yaml:"interval"`
	Repositories []Repository `yaml:"repositories"`
	Commit       CommitConfig `yaml:"commit"`
	Backup       BackupConfig `yaml:"backup"`
	Editors      []string     `yaml:"editors"`
}

type Repository struct {
	Path string `yaml:"path"`
}

type CommitConfig struct {
	Message string `yaml:"message"`
}

// BackupConfig chooses how repositories are protected. Every field is optional;
// leaving the whole section out keeps the original behavior.
type BackupConfig struct {
	// Mode is "commit" (the default) to commit on your current branch, or
	// "branch" to keep snapshots on a separate backup branch instead.
	Mode string `yaml:"mode"`

	// Prefix is the namespace of backup branches in branch mode. Default: "autogit".
	Prefix string `yaml:"prefix"`

	// Machine names this computer in the backup branch name. Default: the host name.
	Machine string `yaml:"machine"`
}

var (
	prefixPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)
	machinePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

	// Git's own ref namespaces, which a backup prefix must not use.
	reservedPrefixes = map[string]bool{
		"heads": true, "tags": true, "remotes": true, "stash": true,
		"notes": true, "bisect": true, "replace": true,
	}
)

func (b BackupConfig) validate() error {
	switch b.Mode {
	case "", "commit", "branch":
	default:
		return fmt.Errorf("backup.mode %q is not valid; use \"commit\" or \"branch\"", b.Mode)
	}

	if b.Prefix != "" {
		if !prefixPattern.MatchString(b.Prefix) || strings.Contains(b.Prefix, "..") {
			return fmt.Errorf("backup.prefix %q is not valid; use letters, digits, '.', '_' and '-', separated by '/'", b.Prefix)
		}

		segments := strings.Split(b.Prefix, "/")

		for _, segment := range segments {
			if strings.HasSuffix(segment, ".lock") {
				return fmt.Errorf("backup.prefix %q is not valid; a part may not end in .lock", b.Prefix)
			}
		}

		if reservedPrefixes[segments[0]] {
			return fmt.Errorf("backup.prefix %q would mix snapshots with Git's own refs; choose another name", b.Prefix)
		}
	}

	if b.Machine != "" && !machinePattern.MatchString(b.Machine) {
		return fmt.Errorf("backup.machine %q is not valid; use letters, digits, '.', '_' and '-'", b.Machine)
	}

	return nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var config Config

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return Config{}, err
	}

	return config, nil
}

func (c Config) Validate() error {
	if c.Interval == "" {
		return fmt.Errorf("interval is required")
	}

	interval, err := time.ParseDuration(c.Interval)
	if err != nil {
		return fmt.Errorf("invalid interval %q: %w", c.Interval, err)
	}

	if interval <= 0 {
		return fmt.Errorf("interval must be greater than zero")
	}

	if len(c.Repositories) == 0 {
		return fmt.Errorf("at least one repository is required")
	}

	for _, repository := range c.Repositories {
		if strings.TrimSpace(repository.Path) == "" {

			return fmt.Errorf("repository path is required")
		}
	}

	if strings.TrimSpace(c.Commit.Message) == "" {
		return fmt.Errorf("commit message is required")
	}

	return c.Backup.validate()
}

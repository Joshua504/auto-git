package config

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Interval      string         `yaml:"interval"`
	Repositories  []Repository   `yaml:"repositories"`
	Commit        CommitConfig   `yaml:"commit"`
	Backup        BackupConfig   `yaml:"backup"`
	Sync          SyncConfig     `yaml:"sync"`
	Notifications NotificationCfg `yaml:"notifications"`
	Editors       []string       `yaml:"editors"`
}

type Repository struct {
	Path string `yaml:"path"`
}

type CommitConfig struct {
	Message string `yaml:"message"`
}

// messagePlaceholders are the names a commit message may use, in braces.
var messagePlaceholders = []string{"summary", "files", "branch", "host", "repo", "date", "time", "datetime"}

// MessagePlaceholders returns the placeholders a commit message may use.
func MessagePlaceholders() []string {
	return append([]string(nil), messagePlaceholders...)
}

// placeholderPattern finds {word} in a commit message. Braces around anything
// containing a space are ordinary text and are left alone.
var placeholderPattern = regexp.MustCompile(`\{([^{}\s]+)\}`)

// validateMessageTemplate rejects an unknown placeholder at startup, so a typo
// such as {sumary} cannot end up written literally into every commit.
func validateMessageTemplate(message string) error {
	for _, match := range placeholderPattern.FindAllStringSubmatch(message, -1) {
		if slices.Contains(messagePlaceholders, match[1]) {
			continue
		}

		listed := make([]string, len(messagePlaceholders))
		for i, name := range messagePlaceholders {
			listed[i] = "{" + name + "}"
		}

		return fmt.Errorf("commit.message uses {%s}, which is not a placeholder; use one of: %s",
			match[1], strings.Join(listed, ", "))
	}

	return nil
}

// SyncConfig controls how AutoGit brings in changes made elsewhere.
type SyncConfig struct {
	// PullOnOpen makes AutoGit fast-forward each repository to its upstream when
	// an editor opens, before the first backup. It is on unless set to false.
	PullOnOpen *bool `yaml:"pull_on_open"`
}

// PullOnOpenEnabled reports whether to pull new remote commits when an editor opens.
func (c Config) PullOnOpenEnabled() bool {
	return c.Sync.PullOnOpen == nil || *c.Sync.PullOnOpen
}

// NotificationCfg controls desktop notifications for events that need attention.
type NotificationCfg struct {
	// Enabled turns on popup notifications with an audible alert.
	// When false (the default) AutoGit only writes to the log.
	Enabled *bool `yaml:"enabled"`
}

// NotificationsEnabled reports whether desktop notifications are turned on.
func (c Config) NotificationsEnabled() bool {
	return c.Notifications.Enabled != nil && *c.Notifications.Enabled
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

	// Retention controls how old backup branch snapshots are cleaned up.
	// Only applies when mode is "branch".
	Retention RetentionCfg `yaml:"retention"`
}

// RetentionCfg controls backup branch snapshot cleanup.
type RetentionCfg struct {
	// MaxAge is the maximum age of backup snapshots, in days.
	// Snapshots older than this are pruned. Zero means no age limit.
	MaxAge int `yaml:"max_age"`

	// MaxCommits is the maximum number of snapshots to keep per backup branch.
	// Oldest snapshots are removed when the count exceeds this limit.
	// Zero means no count limit.
	MaxCommits int `yaml:"max_commits"`
}

// ShouldPrune reports whether branch retention is configured.
func (r RetentionCfg) ShouldPrune() bool {
	return r.MaxAge > 0 || r.MaxCommits > 0
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

	if b.Retention.MaxAge < 0 {
		return fmt.Errorf("backup.retention.max_age must be zero or positive")
	}

	if b.Retention.MaxCommits < 0 {
		return fmt.Errorf("backup.retention.max_commits must be zero or positive")
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

	if err := validateMessageTemplate(c.Commit.Message); err != nil {
		return err
	}

	return c.Backup.validate()
}

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
	Interval      string            `yaml:"interval"`
	Repositories  []Repository      `yaml:"repositories"`
	Commit        CommitConfig      `yaml:"commit"`
	Backup        BackupConfig      `yaml:"backup"`
	Sync          SyncConfig        `yaml:"sync"`
	Verification  VerificationCfg   `yaml:"verification"`
	Notifications NotificationCfg   `yaml:"notifications"`
	Power         PowerCfg          `yaml:"power"`
	OfflineQueue  OfflineQueueCfg   `yaml:"offline_queue"`
	Editors       []string          `yaml:"editors"`
}

// OfflineQueueCfg controls the persistent offline backup queue.
type OfflineQueueCfg struct {
	// Enabled turns on the offline backup queue. When enabled, backup
	// operations that fail due to network issues are stored locally and
	// retried when connectivity returns.
	Enabled *bool `yaml:"enabled"`

	// Path is the directory where the queue database is stored.
	// When empty, defaults to ~/.config/autogit/queue.db.
	Path string `yaml:"path"`

	// RetryDelay is how long to wait between retry cycles.
	// Defaults to 30s.
	RetryDelay string `yaml:"retry_delay"`

	// MaxRetries is the maximum number of retry attempts before marking
	// an entry as permanently failed. Defaults to 5.
	MaxRetries int `yaml:"max_retries"`
}

// OfflineQueueEnabled reports whether the offline queue is turned on.
func (c Config) OfflineQueueEnabled() bool {
	return c.OfflineQueue.Enabled != nil && *c.OfflineQueue.Enabled
}

// OfflineQueuePath returns the configured queue database path.
func (c Config) OfflineQueuePath() string {
	if c.OfflineQueue.Path != "" {
		return c.OfflineQueue.Path
	}
	return ""
}

// OfflineQueueRetryDelay returns the configured retry delay.
func (c Config) OfflineQueueRetryDelay() string {
	if c.OfflineQueue.RetryDelay != "" {
		return c.OfflineQueue.RetryDelay
	}
	return "30s"
}

// OfflineQueueMaxRetries returns the configured max retries.
func (c Config) OfflineQueueMaxRetries() int {
	if c.OfflineQueue.MaxRetries > 0 {
		return c.OfflineQueue.MaxRetries
	}
	return 5
}

type Repository struct {
	Path      string         `yaml:"path"`
	Schedule  ScheduleConfig `yaml:"schedule,omitempty"`
	Retention RetentionCfg   `yaml:"retention,omitempty"`
}

// ScheduleConfig holds per-repository scheduling options. When left empty the
// repository inherits the global interval and uses the fixed (always-backup)
// policy.
type ScheduleConfig struct {
	// Policy selects the scheduling strategy. Values:
	//
	//   "fixed"   — backup on every tick (the original behavior).
	//   "smart"   — backup only when changes are detected, with a safety
	//               fallback interval.
	//   "hybrid"  — like smart, but enforces a minimum interval between
	//               backups to throttle very active repositories.
	//
	// When empty, defaults to "fixed".
	Policy string `yaml:"policy,omitempty"`

	// Interval overrides the global interval for this repository.
	// When empty, the global interval is used.
	Interval string `yaml:"interval,omitempty"`

	// ActivityWindow is how far back the smart and hybrid policies look for
	// recent changes. A repository with changes within this window is
	// considered active. Defaults to 1h when policy is "smart" or "hybrid".
	ActivityWindow string `yaml:"activity_window,omitempty"`

	// SafetyInterval is the maximum time between backups even when nothing
	// has changed. Used only by the "smart" policy. Zero means no forced
	// backup fallback. Defaults to 24h.
	SafetyInterval string `yaml:"safety_interval,omitempty"`

	// MinInterval is the minimum time between two backups for the same
	// repository. Used only by the "hybrid" policy. Defaults to 1m.
	MinInterval string `yaml:"min_interval,omitempty"`
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

// NotificationEvent identifies a category of notification.
type NotificationEvent string

const (
	// NotificationBackupSuccess is posted when a backup push completes.
	NotificationBackupSuccess NotificationEvent = "backup_success"
	// NotificationBackupFailure is posted when a backup push fails.
	NotificationBackupFailure NotificationEvent = "backup_failure"
	// NotificationManualIntervention is posted when a repository needs user
	// action (e.g. merge conflict, diverged history that cannot be resolved).
	NotificationManualIntervention NotificationEvent = "manual_intervention"
	// NotificationQueueSync is posted when pending offline backups are
	// synchronized after connectivity returns.
	NotificationQueueSync NotificationEvent = "queue_sync"
	// NotificationBranchDiverged is posted when a backup branch has diverged
	// from its target and requires attention.
	NotificationBranchDiverged NotificationEvent = "branch_diverged"
)

// AllNotificationEvents lists every known notification event type.
var AllNotificationEvents = []NotificationEvent{
	NotificationBackupSuccess,
	NotificationBackupFailure,
	NotificationManualIntervention,
	NotificationQueueSync,
	NotificationBranchDiverged,
}

// NotificationCfg controls desktop notifications for events that need attention.
// When Enabled is true (or nil with any event explicitly set) notifications are
// delivered for the events whose flags are true.  By default all events are
// enabled when notifications are turned on.
type NotificationCfg struct {
	// Enabled turns on popup notifications with an audible alert.
	// When false (the default) AutoGit only writes to the log.
	Enabled *bool `yaml:"enabled"`

	// Events maps each notification event type to a boolean.  An omitted key
	// inherits the global Enabled flag: when Enabled is true the event is on by
	// default; when Enabled is false (or nil) the event is off unless the key is
	// explicitly set to true.
	Events map[string]*bool `yaml:"events"`
}

// NotificationsEnabled reports whether desktop notifications are turned on
// globally.
func (c Config) NotificationsEnabled() bool {
	return c.Notifications.Enabled != nil && *c.Notifications.Enabled
}

// NotificationEventEnabled reports whether a specific notification event is
// enabled.  It returns true when the global flag is on and the event is not
// explicitly disabled, or when the event is explicitly enabled regardless of
// the global flag.
func (c Config) NotificationEventEnabled(event NotificationEvent) bool {
	cfg := c.Notifications

	// If the event has an explicit setting, honour it.
	if cfg.Events != nil {
		if v, ok := cfg.Events[string(event)]; ok && v != nil {
			return *v
		}
	}

	// Otherwise fall back to the global flag.
	return c.NotificationsEnabled()
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

	// KeepDaily keeps the last snapshot of each of the last N days.
	// Zero means no daily retention.
	KeepDaily int `yaml:"keep_daily"`

	// KeepWeekly keeps the last snapshot of each of the last N weeks.
	// Zero means no weekly retention.
	KeepWeekly int `yaml:"keep_weekly"`

	// KeepMonthly keeps the last snapshot of each of the last N months.
	// Zero means no monthly retention.
	KeepMonthly int `yaml:"keep_monthly"`
}

// ShouldPrune reports whether branch retention is configured.
func (r RetentionCfg) ShouldPrune() bool {
	return r.MaxAge > 0 || r.MaxCommits > 0 || r.KeepDaily > 0 || r.KeepWeekly > 0 || r.KeepMonthly > 0
}

// validate checks that all retention fields are non-negative.
func (r RetentionCfg) validate() error {
	if r.MaxAge < 0 {
		return fmt.Errorf("retention.max_age must be zero or positive")
	}
	if r.MaxCommits < 0 {
		return fmt.Errorf("retention.max_commits must be zero or positive")
	}
	if r.KeepDaily < 0 {
		return fmt.Errorf("retention.keep_daily must be zero or positive")
	}
	if r.KeepWeekly < 0 {
		return fmt.Errorf("retention.keep_weekly must be zero or positive")
	}
	if r.KeepMonthly < 0 {
		return fmt.Errorf("retention.keep_monthly must be zero or positive")
	}
	return nil
}

// IsEmpty reports whether the retention config has any fields set.
func (r RetentionCfg) IsEmpty() bool {
	return r.MaxAge == 0 && r.MaxCommits == 0 && r.KeepDaily == 0 && r.KeepWeekly == 0 && r.KeepMonthly == 0
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

	if err := b.Retention.validate(); err != nil {
		return fmt.Errorf("backup.retention: %w", err)
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

		if err := repository.Schedule.validate(); err != nil {
			return fmt.Errorf("repository %s: %w", repository.Path, err)
		}

		if !repository.Retention.IsEmpty() {
			if err := repository.Retention.validate(); err != nil {
				return fmt.Errorf("repository %s: %w", repository.Path, err)
			}
		}
	}

	if strings.TrimSpace(c.Commit.Message) == "" {
		return fmt.Errorf("commit message is required")
	}

	if err := validateMessageTemplate(c.Commit.Message); err != nil {
		return err
	}

	if err := c.Backup.validate(); err != nil {
		return err
	}

	if err := c.Verification.validate(); err != nil {
		return err
	}

	return c.Power.validate()
}

var validPolicies = map[string]bool{"": true, "fixed": true, "smart": true, "hybrid": true}

func (s ScheduleConfig) validate() error {
	if !validPolicies[s.Policy] {
		return fmt.Errorf("schedule.policy %q is not valid; use \"fixed\", \"smart\", or \"hybrid\"", s.Policy)
	}

	if s.Interval != "" {
		_, err := time.ParseDuration(s.Interval)
		if err != nil {
			return fmt.Errorf("schedule.interval %q is not a valid duration: %w", s.Interval, err)
		}
	}

	if s.ActivityWindow != "" {
		_, err := time.ParseDuration(s.ActivityWindow)
		if err != nil {
			return fmt.Errorf("schedule.activity_window %q is not a valid duration: %w", s.ActivityWindow, err)
		}
	}

	if s.SafetyInterval != "" {
		dur, err := time.ParseDuration(s.SafetyInterval)
		if err != nil {
			return fmt.Errorf("schedule.safety_interval %q is not a valid duration: %w", s.SafetyInterval, err)
		}
		if dur < 0 {
			return fmt.Errorf("schedule.safety_interval must be zero or positive")
		}
	}

	if s.MinInterval != "" {
		dur, err := time.ParseDuration(s.MinInterval)
		if err != nil {
			return fmt.Errorf("schedule.min_interval %q is not a valid duration: %w", s.MinInterval, err)
		}
		if dur < 0 {
			return fmt.Errorf("schedule.min_interval must be zero or positive")
		}
	}

	return nil
}

// VerificationCfg controls how AutoGit verifies that backups are recoverable.
// When the whole section is omitted, verification is disabled.
type VerificationCfg struct {
	// Enabled turns on periodic backup verification. When false (the default),
	// verification only runs when explicitly requested via the CLI.
	Enabled *bool `yaml:"enabled"`

	// Interval is how often to run verification when enabled.
	// When empty, defaults to 24h.
	Interval string `yaml:"interval"`
}

// VerificationEnabled reports whether periodic verification is turned on.
func (v VerificationCfg) VerificationEnabled() bool {
	return v.Enabled != nil && *v.Enabled
}

// VerificationInterval returns the verification interval duration.
// Returns 24h as the default when not configured.
func (v VerificationCfg) VerificationInterval() time.Duration {
	if v.Interval != "" {
		if dur, err := time.ParseDuration(v.Interval); err == nil {
			return dur
		}
	}
	return 24 * time.Hour
}

func (v VerificationCfg) validate() error {
	if v.Interval != "" {
		_, err := time.ParseDuration(v.Interval)
		if err != nil {
			return fmt.Errorf("verification.interval %q is not a valid duration: %w", v.Interval, err)
		}
	}
	return nil
}

// PowerCfg controls how AutoGit adjusts its behavior according to the
// computer's power state. When the whole section is omitted, AutoGit ignores
// the power state and always uses the normal interval.
type PowerCfg struct {
	// BatteryInterval overrides the backup interval when running on battery.
	// When empty, the normal interval is used even on battery.
	BatteryInterval string `yaml:"battery_interval"`

	// LowBatteryPercent is the battery percentage below which automatic backups
	// are paused. Set to 0 to disable (the default). Manual backups still work
	// regardless of battery level.
	LowBatteryPercent int `yaml:"low_battery_percent"`
}

// BatteryIntervalEnabled reports whether a battery-specific interval is configured.
func (p PowerCfg) BatteryIntervalEnabled() bool {
	return p.BatteryInterval != ""
}

// LowBatteryThreshold returns the battery percentage threshold, or 0 if
// low-battery mode is disabled.
func (p PowerCfg) LowBatteryThreshold() int {
	return p.LowBatteryPercent
}

func (p PowerCfg) validate() error {
	if p.BatteryInterval != "" {
		_, err := time.ParseDuration(p.BatteryInterval)
		if err != nil {
			return fmt.Errorf("power.battery_interval %q is not a valid duration: %w", p.BatteryInterval, err)
		}
	}

	if p.LowBatteryPercent < 0 || p.LowBatteryPercent > 100 {
		return fmt.Errorf("power.low_battery_percent must be between 0 and 100")
	}

	return nil
}

// GetRetention returns the retention configuration for a repository.
// If the repository has per-repo retention configured, it returns that;
// otherwise it falls back to the global backup retention settings.
func (c Config) GetRetention(repoPath string) RetentionCfg {
	for _, repo := range c.Repositories {
		if repo.Path == repoPath && !repo.Retention.IsEmpty() {
			return repo.Retention
		}
	}
	return c.Backup.Retention
}

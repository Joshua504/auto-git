// Package notify sends desktop notifications for AutoGit events.
//
// It provides an abstraction over the freedesktop.org Desktop Notifications
// Specification (via the beeep library on Linux) so that notifications are
// optional, configurable per event type, and deduplicated to avoid spamming
// the user with repeated alerts for the same unresolved error.
package notify

import (
	"sync"
	"time"

	"github.com/gen2brain/beeep"
)

func init() {
	beeep.AppName = "AutoGit"
}

// Event identifies a category of notification that can be enabled or disabled
// independently in the configuration.
type Event string

const (
	// BackupSuccess is posted when a backup push completes.
	BackupSuccess Event = "backup_success"
	// BackupFailure is posted when a backup push fails.
	BackupFailure Event = "backup_failure"
	// ManualIntervention is posted when a repository needs user action.
	ManualIntervention Event = "manual_intervention"
	// QueueSync is posted when pending offline backups are synchronized.
	QueueSync Event = "queue_sync"
	// BranchDiverged is posted when a backup branch has diverged.
	BranchDiverged Event = "branch_diverged"
)

// Provider sends notifications to the desktop environment. The default
// implementation uses the freedesktop.org D-Bus notifications protocol on
// Linux.
type Provider interface {
	Notify(title, message string)
}

// linuxProvider wraps beeep, which uses notify-send (D-Bus) on Linux.
type linuxProvider struct{}

func (p linuxProvider) Notify(title, message string) {
	_ = beeep.Alert(title, message, "")
}

// nopProvider discards all notifications silently.
type nopProvider struct{}

func (p nopProvider) Notify(string, string) {}

// Sender controls whether desktop notifications are delivered and deduplicates
// repeated alerts for the same error.
type Sender struct {
	provider Provider

	// eventEnabled reports whether a given event type is allowed.
	eventEnabled func(Event) bool

	// deduplication state: tracks the last notification sent for each
	// error-like event so that identical messages are suppressed for a
	// cooldown period.
	mu          sync.Mutex
	lastSent    map[string]time.Time
	cooldown    time.Duration
	seenCount   map[string]int
	maxRepeats  int
}

// SenderConfig holds optional configuration for a Sender.
type SenderConfig struct {
	// Cooldown is how long to wait before allowing the same notification
	// message again. Defaults to 5 minutes.
	Cooldown time.Duration

	// MaxRepeats is how many times the same notification may be sent before
	// it is permanently suppressed until the error changes. A value of 0
	// means unlimited repeats (disabled deduplication). Defaults to 3.
	MaxRepeats int
}

// DefaultSenderConfig returns the standard deduplication settings.
func DefaultSenderConfig() SenderConfig {
	return SenderConfig{
		Cooldown:   5 * time.Minute,
		MaxRepeats: 3,
	}
}

// New returns a Sender that delivers notifications when eventEnabled returns
// true for the given event type. Pass nil for eventEnabled to enable all
// events.
func New(eventEnabled func(Event) bool) Sender {
	cfg := DefaultSenderConfig()
	return Sender{
		provider:     linuxProvider{},
		eventEnabled: eventEnabled,
		lastSent:     make(map[string]time.Time),
		cooldown:     cfg.Cooldown,
		seenCount:    make(map[string]int),
		maxRepeats:   cfg.MaxRepeats,
	}
}

// NewDisabled returns a Sender that never posts notifications.
func NewDisabled() Sender {
	return Sender{
		provider:   nopProvider{},
		lastSent:   make(map[string]time.Time),
		seenCount:  make(map[string]int),
	}
}

// ShouldNotify reports whether the sender will actually post notifications.
// It returns true when the provider is not a no-op.
func (s Sender) ShouldNotify() bool {
	_, isNop := s.provider.(nopProvider)
	return !isNop
}

// isDedupEligible reports whether an event type should be deduplicated.
// Only error-like events (failure, manual intervention) are deduplicated;
// success and informational events are always delivered.
func isDedupEligible(event Event) bool {
	switch event {
	case BackupFailure, ManualIntervention:
		return true
	default:
		return false
	}
}

// dedupKey builds a stable key for deduplication from the event and message.
func dedupKey(event Event, message string) string {
	return string(event) + "|" + message
}

// shouldDedup checks whether a notification with the given key should be
// suppressed. It returns true when the same message was sent recently or has
// exceeded the maximum repeat count.
func (s *Sender) shouldDedup(key string) bool {
	if !s.maxRepeatsReached(key) && !s.withinCooldown(key) {
		s.recordSend(key)
		return false
	}
	return true
}

func (s *Sender) withinCooldown(key string) bool {
	if ts, ok := s.lastSent[key]; ok {
		return time.Since(ts) < s.cooldown
	}
	return false
}

func (s *Sender) maxRepeatsReached(key string) bool {
	if s.maxRepeats <= 0 {
		return false
	}
	return s.seenCount[key] >= s.maxRepeats
}

func (s *Sender) recordSend(key string) {
	s.lastSent[key] = time.Now()
	s.seenCount[key]++
}

// Send posts a notification for the given event type. If the event is disabled
// or the message is a duplicate within the cooldown window, the call is a no-op.
func (s Sender) Send(event Event, title, message string) {
	// A nil provider means the sender was not initialised (zero value).
	if s.provider == nil {
		return
	}

	// Check if the event is enabled.
	if s.eventEnabled != nil && !s.eventEnabled(event) {
		return
	}

	// Check deduplication for error-like events.
	if isDedupEligible(event) {
		s.mu.Lock()
		dup := s.shouldDedup(dedupKey(event, message))
		s.mu.Unlock()
		if dup {
			return
		}
	}

	s.provider.Notify(title, message)
}

// Success posts an informational notification for a completed action.
// It is not deduplicated.
func (s Sender) Success(title, message string) {
	s.Send(BackupSuccess, title, message)
}

// Warning posts a notification for a non-fatal issue that still requires
// attention (skipped repo, diverged branch, etc.). It is not deduplicated.
func (s Sender) Warning(title, message string) {
	s.Send(ManualIntervention, title, message)
}

// Error posts a notification for a failure. It is deduplicated: the same
// error message will not be repeated within the cooldown window or beyond
// the maximum repeat count.
func (s Sender) Error(title, message string) {
	s.Send(BackupFailure, title, message)
}

// QueueSynced posts a notification when pending offline backups have been
// synchronized. It is not deduplicated.
func (s Sender) QueueSynced(title, message string) {
	s.Send(QueueSync, title, message)
}

// BranchDiverged posts a notification when a backup branch has diverged from
// its target. It is not deduplicated.
func (s Sender) BranchDiverged(title, message string) {
	s.Send(BranchDiverged, title, message)
}

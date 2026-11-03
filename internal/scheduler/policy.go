package scheduler

import (
	"sync"
	"time"
)

// Policy determines whether a repository should be backed up at a given tick.
// A scheduler calls ShouldBackup before running a backup and RecordBackup after
// a successful backup. Implementations may also use RecordActivity to learn
// when new changes appear.
type Policy interface {
	// ShouldBackup reports whether the repository at path should be backed up
	// now. It is called on every scheduler tick.
	ShouldBackup(path string) bool

	// RecordBackup is called after a successful backup for the repository.
	RecordBackup(path string)

	// RecordActivity is called when new uncommitted changes are detected in
	// the repository, so the policy can adjust its timing.
	RecordActivity(path string)
}

// FixedPolicy always returns true — it replicates the original behavior where
// every tick produces a backup attempt regardless of whether anything changed.
type FixedPolicy struct{}

// ShouldBackup always returns true for a fixed policy.
func (FixedPolicy) ShouldBackup(string) bool { return true }

// RecordBackup is a no-op for a fixed policy.
func (FixedPolicy) RecordBackup(string) {}

// RecordActivity is a no-op for a fixed policy.
func (FixedPolicy) RecordActivity(string) {}

// SmartPolicy backs up only when there are uncommitted changes, with a fallback
// safety interval that forces a backup even when nothing has changed.
//
// Parameters:
//   - ActivityWindow: how far back to look for recent changes. If a repository
//     has had changes within this window, it is considered active and will be
//     backed up on the next tick (if changes are still present).
//   - SafetyInterval: maximum time between backups even when nothing has
//     changed. This ensures a repository is never left un-backed-up for too
//     long. Zero means no safety fallback (backup only when changes exist).
type SmartPolicy struct {
	ActivityWindow time.Duration
	SafetyInterval time.Duration

	mu       sync.Mutex
	backups  map[string]time.Time // last backup time per repo
	activity map[string]time.Time // last activity time per repo
}

// NewSmartPolicy creates a SmartPolicy with the given activity window and
// safety interval. If activityWindow is zero, it defaults to 1 hour.
// If safetyInterval is zero, there is no forced backup fallback.
func NewSmartPolicy(activityWindow, safetyInterval time.Duration) *SmartPolicy {
	if activityWindow <= 0 {
		activityWindow = 1 * time.Hour
	}
	return &SmartPolicy{
		ActivityWindow: activityWindow,
		SafetyInterval: safetyInterval,
		backups:        make(map[string]time.Time),
		activity:       make(map[string]time.Time),
	}
}

// ShouldBackup returns true when the repository has recent activity or when
// the safety interval has elapsed since the last backup.
func (p *SmartPolicy) ShouldBackup(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	lastBackup, backedUp := p.backups[path]
	lastActivity, active := p.activity[path]

	// If there is recent activity (changes detected), back up.
	if active && now.Sub(lastActivity) <= p.ActivityWindow {
		return true
	}

	// If the safety interval has elapsed since the last backup, force one.
	if p.SafetyInterval > 0 && backedUp && now.Sub(lastBackup) >= p.SafetyInterval {
		return true
	}

	// Never backed up yet — do an initial backup to establish a baseline.
	if !backedUp {
		return true
	}

	return false
}

// RecordBackup records the time of the last successful backup.
func (p *SmartPolicy) RecordBackup(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.backups[path] = time.Now()
}

// RecordActivity records that new changes were detected in the repository.
func (p *SmartPolicy) RecordActivity(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activity[path] = time.Now()
}

// HybridPolicy combines smart detection with a configurable interval. It
// backs up immediately when changes are detected, but also respects a minimum
// interval between backups to avoid excessive work on very active repositories.
//
// Parameters:
//   - MinInterval: minimum time between two backups for the same repository.
//   - ActivityWindow: how far back to look for recent changes.
type HybridPolicy struct {
	MinInterval    time.Duration
	ActivityWindow time.Duration

	mu       sync.Mutex
	backups  map[string]time.Time
	activity map[string]time.Time
}

// NewHybridPolicy creates a HybridPolicy with the given minimum interval and
// activity window. If minInterval is zero, it defaults to 1 minute.
func NewHybridPolicy(minInterval, activityWindow time.Duration) *HybridPolicy {
	if minInterval <= 0 {
		minInterval = 1 * time.Minute
	}
	if activityWindow <= 0 {
		activityWindow = 1 * time.Hour
	}
	return &HybridPolicy{
		MinInterval:    minInterval,
		ActivityWindow: activityWindow,
		backups:        make(map[string]time.Time),
		activity:       make(map[string]time.Time),
	}
}

// ShouldBackup returns true when the repository has recent activity and the
// minimum interval has elapsed since the last backup.
func (p *HybridPolicy) ShouldBackup(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	lastBackup, backedUp := p.backups[path]
	lastActivity, active := p.activity[path]

	// Respect minimum interval.
	if backedUp && now.Sub(lastBackup) < p.MinInterval {
		return false
	}

	// If there is recent activity, back up.
	if active && now.Sub(lastActivity) <= p.ActivityWindow {
		return true
	}

	// Never backed up yet — do an initial backup.
	if !backedUp {
		return true
	}

	return false
}

// RecordBackup records the time of the last successful backup.
func (p *HybridPolicy) RecordBackup(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.backups[path] = time.Now()
}

// RecordActivity records that new changes were detected in the repository.
func (p *HybridPolicy) RecordActivity(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activity[path] = time.Now()
}

package scheduler

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Joshua504/auto-git/internal/power"
)

// PowerChecker abstracts the power-state query used by the scheduler.
type PowerChecker interface {
	GetState() (power.State, error)
}

// PowerConfig holds the battery-aware scheduling settings.
type PowerConfig struct {
	BatteryInterval   time.Duration // interval to use on battery; 0 means use normal
	LowBatteryPercent int           // pause auto-backups below this %; 0 disables
}

// Scheduler is the original fixed-interval scheduler that runs a single task
// on every tick. It is kept for backward compatibility with the existing
// watcher in main.go.
type Scheduler struct {
	interval time.Duration
	task     func()
}

func New(interval time.Duration, task func()) (*Scheduler, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("interval must be greater than zero")
	}

	if task == nil {
		return nil, fmt.Errorf("task cannot be nil")
	}

	return &Scheduler{
		interval: interval,
		task:     task,
	}, nil
}

func (s *Scheduler) Start(stop <-chan struct{}) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.task()

		case <-stop:
			return
		}
	}
}

// RepoEntry describes one repository in a RepoScheduler.
type RepoEntry struct {
	Path     string
	Interval time.Duration
	Policy   Policy
}

// RepoScheduler manages backups for multiple repositories, each with its own
// interval and scheduling policy. It supports manual triggers that bypass the
// policy check for a specific repository.
type RepoScheduler struct {
	mu      sync.Mutex
	repos   []RepoEntry
	running bool

	// trigger sends a path to force a backup for one repository immediately,
	// regardless of its policy.
	trigger chan string

	// Optional power-aware scheduling. When set, the scheduler adjusts
	// intervals on battery and can pause automatic backups at low battery.
	powerChecker PowerChecker
	powerConfig  PowerConfig
}

// NewRepoScheduler creates a scheduler for the given repositories.
func NewRepoScheduler(repos []RepoEntry) *RepoScheduler {
	return &RepoScheduler{
		repos:   repos,
		trigger: make(chan string, len(repos)),
	}
}

// SetPowerConfig enables battery-aware scheduling. If powerChecker is nil,
// the scheduler falls back to assuming AC power.
func (rs *RepoScheduler) SetPowerConfig(checker PowerChecker, cfg PowerConfig) {
	rs.powerChecker = checker
	rs.powerConfig = cfg
}

// Trigger sends a manual backup request for the repository at path.
// It returns false if the scheduler is not running.
func (rs *RepoScheduler) Trigger(path string) bool {
	rs.mu.Lock()
	running := rs.running
	rs.mu.Unlock()
	if !running {
		return false
	}
	// Non-blocking send; if the channel is full, the request is dropped.
	select {
	case rs.trigger <- path:
		return true
	default:
		return false
	}
}

// BackupFunc is called for one repository backup attempt.
// It returns whether the repository had changes and any error.
type BackupFunc func(path string) (hasChanges bool, err error)

// Start runs the scheduler until stop is closed.
//
// Each repository is backed up on its own interval. Before each backup the
// configured policy is consulted: if it returns false the repository is
// skipped for that tick. A manual trigger sent via Trigger() bypasses the
// policy check.
//
// When power-aware scheduling is configured via SetPowerConfig, the scheduler
// adjusts intervals on battery and can pause automatic backups at low battery.
// Manual triggers always bypass the power check.
//
// The backup function is called for each repository. After a successful backup
// the policy's RecordBackup is called. If the backup reported changes,
// RecordActivity is called so the policy knows the repository is active.
func (rs *RepoScheduler) Start(stop <-chan struct{}, backup BackupFunc) {
	rs.mu.Lock()
	rs.running = true
	rs.mu.Unlock()

	defer func() {
		rs.mu.Lock()
		rs.running = false
		rs.mu.Unlock()
	}()

	// Build per-repo ticker goroutines.
	var wg sync.WaitGroup

	for _, entry := range rs.repos {
		ticker := time.NewTicker(entry.Interval)
		wg.Add(1)
		go func(e RepoEntry, ticker *time.Ticker) {
			defer ticker.Stop()
			defer wg.Done()

			// Track the last known power type so we can detect transitions.
			var lastPowerType int

			for {
				select {
				case <-ticker.C:
					// Check power state before backup.
					if rs.powerChecker != nil {
						state, err := rs.powerChecker.GetState()
						if err == nil {
							// Detect power state transitions and adjust interval.
							if int(state.PowerType) != lastPowerType {
								lastPowerType = int(state.PowerType)
								effectiveInterval := e.Interval
								if state.OnBattery() && rs.powerConfig.BatteryInterval > 0 {
									effectiveInterval = rs.powerConfig.BatteryInterval
								}
								ticker.Reset(effectiveInterval)
							}

							// Check low-battery threshold — skip automatic backup.
							if rs.powerConfig.LowBatteryPercent > 0 && state.Battery >= 0 {
								if state.Battery < rs.powerConfig.LowBatteryPercent {
									log.Printf("Skipping automatic backup for %s: battery at %d%% (threshold %d%%)",
										e.Path, state.Battery, rs.powerConfig.LowBatteryPercent)
									continue
								}
							}
						}
					}

					// Check policy before backup.
					if e.Policy != nil && !e.Policy.ShouldBackup(e.Path) {
						continue
					}

					hasChanges, err := backup(e.Path)
					if err != nil {
						continue
					}

					// Record with the policy so it can adjust timing.
					if e.Policy != nil {
						e.Policy.RecordBackup(e.Path)
						if hasChanges {
							e.Policy.RecordActivity(e.Path)
						}
					}

				case <-stop:
					return
				}
			}
		}(entry, ticker)
	}

	// Handle manual triggers — these bypass the policy check.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case path := <-rs.trigger:
				hasChanges, err := backup(path)
				if err != nil {
					continue
				}

				// Find the policy for this repo and record the result.
				rs.mu.Lock()
				for _, e := range rs.repos {
					if e.Path == path && e.Policy != nil {
						e.Policy.RecordBackup(e.Path)
						if hasChanges {
							e.Policy.RecordActivity(e.Path)
						}
						break
					}
				}
				rs.mu.Unlock()

			case <-stop:
				return
			}
		}
	}()

	// Wait for stop, then wait for all goroutines to finish.
	<-stop
	wg.Wait()
}

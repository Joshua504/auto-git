package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Joshua504/auto-git/internal/config"
	"github.com/Joshua504/auto-git/internal/editor"
	"github.com/Joshua504/auto-git/internal/git"
	"github.com/Joshua504/auto-git/internal/notify"
	"github.com/Joshua504/auto-git/internal/power"
	"github.com/Joshua504/auto-git/internal/queue"
	"github.com/Joshua504/auto-git/internal/scheduler"
)

// Default scheduling durations used when a policy is configured but a specific
// value is not provided.
const (
	defaultActivityWindow = 1 * time.Hour
	defaultSafetyInterval = 24 * time.Hour
	defaultMinInterval    = 1 * time.Minute
)

func main() {
	// Handle subcommands before the general flag set so that `autogit install`
	// and `autogit uninstall` work without requiring a config file.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "install":
			installFlag := flag.NewFlagSet("install", flag.ExitOnError)
			installFlagConfig := installFlag.String("config", "",
				"path to the configuration file")
			installFlag.Parse(os.Args[2:])

			if err := cmdInstall(*installFlagConfig); err != nil {
				log.Fatalf("Install failed: %v", err)
			}
			return

		case "uninstall":
			if err := cmdUninstall(); err != nil {
				log.Fatalf("Uninstall failed: %v", err)
			}
			return

		case "verify":
			verifyFlag := flag.NewFlagSet("verify", flag.ExitOnError)
			verifyFlagConfig := verifyFlag.String("config", "",
				"path to the configuration file")
			verifyFlag.Parse(os.Args[2:])

			if err := cmdVerify(*verifyFlagConfig); err != nil {
				log.Fatalf("Verification failed: %v", err)
			}
			return

		case "queue":
			queueFlag := flag.NewFlagSet("queue", flag.ExitOnError)
			queueFlagConfig := queueFlag.String("config", "",
				"path to the configuration file")
			queueFlag.Parse(os.Args[2:])

			if err := cmdQueue(*queueFlagConfig); err != nil {
				log.Fatalf("Queue command failed: %v", err)
			}
			return

		case "health":
			healthFlag := flag.NewFlagSet("health", flag.ExitOnError)
			healthFlagConfig := healthFlag.String("config", "",
				"path to the configuration file")
			healthFlag.Parse(os.Args[2:])

			if err := cmdHealth(*healthFlagConfig); err != nil {
				log.Fatalf("Health check failed: %v", err)
			}
			return

		case "restore":
			restoreFlag := flag.NewFlagSet("restore", flag.ExitOnError)
			restoreFlagConfig := restoreFlag.String("config", "",
				"path to the configuration file")
			restoreFlag.Parse(os.Args[2:])

			if err := cmdRestore(*restoreFlagConfig); err != nil {
				log.Fatalf("Restore failed: %v", err)
			}
			return

		case "history":
			historyFlag := flag.NewFlagSet("history", flag.ExitOnError)
			historyFlagConfig := historyFlag.String("config", "",
				"path to the configuration file")
			historyFlag.Parse(os.Args[2:])

			if err := cmdHistory(*historyFlagConfig); err != nil {
				log.Fatalf("History command failed: %v", err)
			}
			return

		case "compare":
			compareFlag := flag.NewFlagSet("compare", flag.ExitOnError)
			compareFlagConfig := compareFlag.String("config", "",
				"path to the configuration file")
			compareFlag.Parse(os.Args[2:])

			if err := cmdCompare(*compareFlagConfig); err != nil {
				log.Fatalf("Compare failed: %v", err)
			}
			return

		case "cleanup":
			cleanupFlag := flag.NewFlagSet("cleanup", flag.ExitOnError)
			cleanupFlagConfig := cleanupFlag.String("config", "",
				"path to the configuration file")
			cleanupFlag.Parse(os.Args[2:])

			if err := cmdCleanup(*cleanupFlagConfig); err != nil {
				log.Fatalf("Cleanup failed: %v", err)
			}
			return

		case "storage":
			if err := cmdStorage(""); err != nil {
				log.Fatalf("Storage check failed: %v", err)
			}
			return
		}
	}

	configFlag := flag.String("config", "",
		"path to the configuration file (default: ./config.yaml, then ~/.config/autogit/config.yaml)")
	flag.Parse()

	configPath, err := config.Locate(*configFlag)
	if err != nil {
		log.Fatalf("Error locating configuration: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Error loading configuration %s: %v", configPath, err)
	}

	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		log.Fatalf("Error parsing backup interval: %v", err)
	}

	backupOptions := git.Options{
		Mode:    cfg.Backup.Mode,
		Prefix:  cfg.Backup.Prefix,
		Machine: cfg.Backup.Machine,
		Retention: git.RetentionCfg{
			MaxAge:     cfg.Backup.Retention.MaxAge,
			MaxCommits: cfg.Backup.Retention.MaxCommits,
		},
	}

	eventEnabled := func(e notify.Event) bool {
		return cfg.NotificationEventEnabled(config.NotificationEvent(e))
	}
	nc := notify.New(eventEnabled)

	// Initialize the offline backup queue if enabled.
	var q *queue.Queue
	var processor *queue.Processor
	if cfg.OfflineQueueEnabled() {
		queuePath := cfg.OfflineQueuePath()
		if queuePath == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				homeDir = "."
			} else {
				queuePath = filepath.Join(homeDir, ".config", "autogit")
				if err := os.MkdirAll(queuePath, 0755); err != nil {
					log.Printf("Warning: could not create queue directory: %v", err)
					queuePath = "."
				}
			}
		}
		queuePath = filepath.Join(queuePath, "queue.db")

		var err error
		q, err = queue.New(queuePath)
		if err != nil {
			log.Printf("Warning: could not open offline queue: %v (offline queue disabled)", err)
			q = nil
		} else {
			log.Printf("Offline backup queue enabled at %s", queuePath)

			pendingCount, _ := q.CountPending()
			if pendingCount > 0 {
				log.Printf("Found %d pending backup(s) from previous session", pendingCount)
			}

			retryDelayStr := cfg.OfflineQueueRetryDelay()
				retryDelay, err := time.ParseDuration(retryDelayStr)
			if err != nil {
				log.Printf("Warning: invalid retry_delay %q, using 30s", retryDelayStr)
				retryDelay = 30 * time.Second
			}

			processor = queue.NewProcessor(q, func(repoPath string) error {
				return git.Push(repoPath)
			}, queue.ProcessorConfig{
				RetryDelay: retryDelay,
				OnCompleted: func(completed int) {
					nc.QueueSynced("AutoGit: Queue Synced",
						fmt.Sprintf("%d pending backup(s) synchronized", completed))
				},
			})

			go processor.Start()
		}
	}

	// backupOne performs a single repository backup and returns whether it had
	// changes. It is used both by the scheduler and by the legacy watcher path.
	backupOne := func(path string) (hasChanges bool, err error) {
		log.Printf("Checking repository: %s", path)

		hadChanges, _ := git.HasChanges(path)

		if err := git.BackupWith(path, cfg.Commit.Message, backupOptions); err != nil {
			var skipped *git.SkippedError
			if errors.As(err, &skipped) {
				log.Printf("Skipping %s: %s", path, skipped.Reason)
				nc.Warning("AutoGit: Skipped", fmt.Sprintf("%s — %s", path, skipped.Reason))
				return false, err
			}

			// If the offline queue is enabled and the error looks like a network
			// issue, queue the operation for later retry.
			if q != nil && git.IsRetryableGitError(err) {
				commitHash, hashErr := git.CurrentCommitHash(path)
				if hashErr != nil {
					log.Printf("Could not read commit hash for queue: %v", hashErr)
				} else if commitHash != "" {
					remoteURL, _ := git.RemoteURL(path)
					if _, _, addErr := q.Add(path, commitHash, remoteURL); addErr != nil {
						log.Printf("Could not queue backup for %s: %v", path, addErr)
					} else {
						log.Printf("Queued backup for %s (commit %s) for offline retry", path, commitHash)
					}
				}
			}

			log.Printf("Backup failed for %s: %v", path, err)
			nc.Error("AutoGit: Backup Failed", fmt.Sprintf("%s — %v", path, err))
			return false, err
		}

		// If the backup succeeded and the queue is enabled, clear any queued
		// entries for this repo with the same commit hash.
		if q != nil {
			commitHash, hashErr := git.CurrentCommitHash(path)
			if hashErr == nil && commitHash != "" {
				pending, _ := q.Pending()
				for _, entry := range pending {
					if entry.RepoPath == path && entry.CommitHash == commitHash {
						if err := q.MarkCompleted(entry.ID); err != nil {
							log.Printf("Could not mark queued entry as completed: %v", err)
						}
					}
				}
			}
		}

		log.Printf("Backup check completed for %s", path)
		if hadChanges {
			nc.Success("AutoGit: Backup Complete", fmt.Sprintf("Pushed changes from %s", path))
		}
		return hadChanges, nil
	}

	// backupTask iterates all repositories — used by the legacy single-task
	// scheduler when no per-repo scheduling is configured.
	backupTask := func() {
		for _, repository := range cfg.Repositories {
			backupOne(repository.Path)
		}
	}

	pullTask := func() {
		for _, repository := range cfg.Repositories {
			result, err := git.SyncOnOpen(repository.Path)

			if message := openSyncMessage(repository.Path, cfg.Backup.Mode, result, err); message != "" {
				log.Print(message)

				// Notify for events that need attention.
				if err != nil {
					var skipped *git.SkippedError
					if errors.As(err, &skipped) {
						nc.Warning("AutoGit: Skipped", fmt.Sprintf("%s — %s", repository.Path, skipped.Reason))
					} else {
						nc.Error("AutoGit: Sync Failed", fmt.Sprintf("%s — %v", repository.Path, err))
					}
				} else if result.Outcome == git.OpenSyncFastForwarded {
					nc.Success("AutoGit: Pulled", fmt.Sprintf("Pulled %s into %s", pluralize(result.Commits, "new commit"), repository.Path))
				} else if result.Outcome == git.OpenSyncDiverged {
					nc.Warning("AutoGit: Diverged", fmt.Sprintf("%s has commits the upstream lacks, and vice versa.", repository.Path))
				} else if result.Outcome == git.OpenSyncBlocked {
					nc.Warning("AutoGit: Blocked", fmt.Sprintf("Uncommitted changes in %s block pulling.", repository.Path))
				}
			}
		}
	}

	// Build per-repo scheduler entries with policies.
	repoEntries := make([]scheduler.RepoEntry, 0, len(cfg.Repositories))
	hasSmartScheduling := false

	for _, repository := range cfg.Repositories {
		repoInterval := interval
		if repository.Schedule.Interval != "" {
			parsed, err := time.ParseDuration(repository.Schedule.Interval)
			if err != nil {
				log.Fatalf("Invalid schedule.interval for %s: %v", repository.Path, err)
			}
			repoInterval = parsed
		}

		policy := createPolicy(repository.Schedule)
		if policy != nil {
			hasSmartScheduling = true
		}

		repoEntries = append(repoEntries, scheduler.RepoEntry{
			Path:     repository.Path,
			Interval: repoInterval,
			Policy:   policy,
		})
	}

	// Create the power provider for battery-aware scheduling.
	var powerProvider power.Provider
	{
		provider, err := power.NewLinuxProvider("")
		if err != nil {
			log.Printf("Warning: could not detect battery: %v (power-aware scheduling disabled)", err)
			powerProvider = power.NopProvider{}
		} else {
			powerProvider = provider
		}
	}

	// Build the power config from the configuration file.
	powerCfg := scheduler.PowerConfig{
		LowBatteryPercent: cfg.Power.LowBatteryThreshold(),
	}
	if cfg.Power.BatteryIntervalEnabled() {
		batteryInterval, err := time.ParseDuration(cfg.Power.BatteryInterval)
		if err != nil {
			log.Fatalf("Invalid power.battery_interval: %v", err)
		}
		powerCfg.BatteryInterval = batteryInterval
	}

	// Create the appropriate scheduler.
	var s *scheduler.Scheduler
	var rs *scheduler.RepoScheduler

	if hasSmartScheduling {
		rs = scheduler.NewRepoScheduler(repoEntries)
		if powerCfg.BatteryInterval > 0 || powerCfg.LowBatteryPercent > 0 {
			rs.SetPowerConfig(powerProvider, powerCfg)
		}
	} else {
		var err error
		s, err = scheduler.New(interval, backupTask)
		if err != nil {
			log.Fatalf("Error creating scheduler: %v", err)
		}
	}

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	// Show which file was used, since there is more than one place it can come from.
	displayPath, err := filepath.Abs(configPath)
	if err != nil {
		displayPath = configPath
	}

	fmt.Println("AutoGit configuration loaded successfully.")
	fmt.Printf("Configuration file: %s\n", displayPath)
	fmt.Printf("Backup interval: %s\n", cfg.Interval)

	backupMode := cfg.Backup.Mode
	if backupMode == "" {
		backupMode = git.ModeCommit
	}

	fmt.Printf("Backup mode: %s\n", backupMode)

	pullOnOpen := cfg.PullOnOpenEnabled()
	fmt.Printf("Pull new commits when an editor opens: %t\n", pullOnOpen)
	fmt.Printf("Repositories configured: %d\n", len(cfg.Repositories))
	fmt.Printf("Editors monitored: %v\n", cfg.Editors)

	// Show power-aware scheduling status.
	if powerCfg.BatteryInterval > 0 || powerCfg.LowBatteryPercent > 0 {
		state, err := powerProvider.GetState()
		if err != nil {
			log.Printf("Warning: could not read power state: %v", err)
		} else {
			fmt.Printf("Power state: %s (battery %d%%)\n", state.PowerType, state.Battery)
		}
		if powerCfg.BatteryInterval > 0 {
			fmt.Printf("Battery interval: %s\n", powerCfg.BatteryInterval)
		}
		if powerCfg.LowBatteryPercent > 0 {
			fmt.Printf("Low battery threshold: %d%%\n", powerCfg.LowBatteryPercent)
		}
	}

	fmt.Println("AutoGit is waiting for a configured editor to open.")

	// Build verification task if periodic verification is enabled.
	var verifyTask func()
	if cfg.Verification.VerificationEnabled() {
		verifyInterval := cfg.Verification.VerificationInterval()
		log.Printf("Periodic verification enabled (interval: %s)", verifyInterval)

		verifyTask = func() {
			backupOptions := git.Options{
				Mode:    cfg.Backup.Mode,
				Prefix:  cfg.Backup.Prefix,
				Machine: cfg.Backup.Machine,
				Retention: git.RetentionCfg{
					MaxAge:     cfg.Backup.Retention.MaxAge,
					MaxCommits: cfg.Backup.Retention.MaxCommits,
				},
			}

			for _, repository := range cfg.Repositories {
				result := git.VerifyBackup(repository.Path, backupOptions, "")
				if result.OK {
					log.Printf("Verification OK for %s", repository.Path)
				} else {
					log.Printf("Verification FAILED for %s: %v", repository.Path, result.Issues)
					for _, issue := range result.Issues {
						nc.Warning("AutoGit: Verification Failed", fmt.Sprintf("%s — %s", repository.Path, issue))
					}
				}
			}
		}
	}

	w := &watcher{
		isRunning: func() bool { return editor.IsRunning(cfg.Editors) },
		pollEvery: editorPollInterval,
		scheduler: s,
		repoSched: rs,
		backup:    backupTask,
		backupOne: backupOne,
		notify:    nc,
		verify:    verifyTask,
	}

	if pullOnOpen {
		w.pull = pullTask
	}

	w.run(ctx)

	// Clean up the offline queue processor.
	if processor != nil {
		processor.Stop()
	}
	if q != nil {
		q.Close()
	}

	fmt.Println("AutoGit stopped.")
}

// openSyncMessage describes what the check on editor open found, in words that
// fit the backup mode. It returns "" when there is nothing worth saying.
func openSyncMessage(path, mode string, result git.OpenSyncResult, err error) string {
	if err != nil {
		var skipped *git.SkippedError
		if errors.As(err, &skipped) {
			return fmt.Sprintf("Skipping %s: %s", path, skipped.Reason)
		}

		return fmt.Sprintf("Could not check %s for new commits: %v", path, err)
	}

	switch result.Outcome {
	case git.OpenSyncFastForwarded:
		return fmt.Sprintf("Pulled %s into %s", pluralize(result.Commits, "new commit"), path)

	case git.OpenSyncDiverged:
		message := fmt.Sprintf("Not pulling in %s: your branch and its upstream each have commits the other lacks.", path)

		if mode == git.ModeBranch {
			return message + " Run git pull yourself to combine them."
		}

		return message + " AutoGit will try to combine them during the next backup."

	case git.OpenSyncBlocked:
		files := result.Conflicts
		listed := strings.Join(files, ", ")

		if len(files) > 3 {
			listed = fmt.Sprintf("%s and %d more", strings.Join(files[:3], ", "), len(files)-3)
		}

		message := fmt.Sprintf("Not pulling in %s: your uncommitted changes touch files that also changed upstream (%s).", path, listed)

		if mode == git.ModeBranch {
			return message + " Commit or stash them, then pull."
		}

		return message + " AutoGit will commit them and try to combine the two during the next backup."

	default:
		return fmt.Sprintf("Nothing to pull in %s", path)
	}
}

func pluralize(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}

	return fmt.Sprintf("%d %ss", count, noun)
}

// createPolicy builds a scheduling policy from the repository's schedule config.
// It returns nil when the policy is "fixed" or empty (the default behavior).
func createPolicy(sc config.ScheduleConfig) scheduler.Policy {
	switch sc.Policy {
	case "smart":
		activityWindow := defaultActivityWindow
		if sc.ActivityWindow != "" {
			if dur, err := time.ParseDuration(sc.ActivityWindow); err == nil {
				activityWindow = dur
			}
		}
		safetyInterval := defaultSafetyInterval
		if sc.SafetyInterval != "" {
			if dur, err := time.ParseDuration(sc.SafetyInterval); err == nil {
				safetyInterval = dur
			}
		}
		return scheduler.NewSmartPolicy(activityWindow, safetyInterval)

	case "hybrid":
		minInterval := defaultMinInterval
		if sc.MinInterval != "" {
			if dur, err := time.ParseDuration(sc.MinInterval); err == nil {
				minInterval = dur
			}
		}
		activityWindow := defaultActivityWindow
		if sc.ActivityWindow != "" {
			if dur, err := time.ParseDuration(sc.ActivityWindow); err == nil {
				activityWindow = dur
			}
		}
		return scheduler.NewHybridPolicy(minInterval, activityWindow)

	default:
		// "fixed" or empty — no policy, always backup.
		return nil
	}
}

// cmdVerify runs backup verification for all configured repositories.
func cmdVerify(configFlag string) error {
	configPath, err := config.Locate(configFlag)
	if err != nil {
		return fmt.Errorf("locating configuration: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	eventEnabled := func(e notify.Event) bool {
		return cfg.NotificationEventEnabled(config.NotificationEvent(e))
	}
	nc := notify.New(eventEnabled)

	backupOptions := git.Options{
		Mode:    cfg.Backup.Mode,
		Prefix:  cfg.Backup.Prefix,
		Machine: cfg.Backup.Machine,
		Retention: git.RetentionCfg{
			MaxAge:     cfg.Backup.Retention.MaxAge,
			MaxCommits: cfg.Backup.Retention.MaxCommits,
		},
	}

	paths := make([]string, 0, len(cfg.Repositories))
	for _, repo := range cfg.Repositories {
		paths = append(paths, repo.Path)
	}

	results := git.VerifyAllBackups(paths, backupOptions)

	anyFailed := false
	for _, result := range results {
		status := "✓ OK"
		if !result.OK {
			status = "✗ FAILED"
			anyFailed = true
		}

		fmt.Printf("%s: %s\n", result.Path, status)

		if result.RemoteReachable {
			fmt.Printf("  Remote reachable: yes\n")
		} else {
			fmt.Printf("  Remote reachable: no\n")
		}

		if result.RefExists {
			fmt.Printf("  Backup reference: exists\n")
		} else {
			fmt.Printf("  Backup reference: missing\n")
		}

		if result.HashMatches {
			fmt.Printf("  Commit hash: matches\n")
		} else {
			fmt.Printf("  Commit hash: mismatch\n")
		}

		if result.Fetchable {
			fmt.Printf("  Snapshot fetchable: yes\n")
		} else {
			fmt.Printf("  Snapshot fetchable: no\n")
		}

		if len(result.Issues) > 0 {
			fmt.Printf("  Issues:\n")
			for _, issue := range result.Issues {
				fmt.Printf("    - %s\n", issue)
				nc.Warning("AutoGit: Verification Failed", fmt.Sprintf("%s — %s", result.Path, issue))
			}
		}
	}

	if anyFailed {
		return fmt.Errorf("verification failed for one or more repositories")
	}

	fmt.Println("\nAll backups verified successfully.")
	return nil
}

// cmdQueue displays the status of the offline backup queue.
func cmdQueue(configFlag string) error {
	configPath, err := config.Locate(configFlag)
	if err != nil {
		return fmt.Errorf("locating configuration: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	queuePath := cfg.OfflineQueuePath()
	if queuePath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			homeDir = "."
		} else {
			queuePath = filepath.Join(homeDir, ".config", "autogit")
		}
	}
	queuePath = filepath.Join(queuePath, "queue.db")

	q, err := queue.New(queuePath)
	if err != nil {
		return fmt.Errorf("opening queue database: %w", err)
	}
	defer q.Close()

	pending, err := q.Pending()
	if err != nil {
		return fmt.Errorf("reading pending entries: %w", err)
	}

	completed, err := q.Completed()
	if err != nil {
		return fmt.Errorf("reading completed entries: %w", err)
	}

	failed, err := q.Failed()
	if err != nil {
		return fmt.Errorf("reading failed entries: %w", err)
	}

	fmt.Println("AutoGit Offline Backup Queue Status")
	fmt.Println("====================================")
	fmt.Println()

	fmt.Printf("Pending:   %d\n", len(pending))
	fmt.Printf("Completed: %d\n", len(completed))
	fmt.Printf("Failed:    %d\n", len(failed))
	fmt.Println()

	if len(pending) > 0 {
		fmt.Println("Pending backups:")
		for _, e := range pending {
			fmt.Printf("  %-40s  %s  (retry: %d)\n",
				truncate(e.RepoPath, 40), e.CommitHash[:8], e.RetryCount)
		}
		fmt.Println()
	}

	if len(completed) > 0 {
		fmt.Println("Recently completed:")
		count := 0
		for _, e := range completed {
			if count >= 10 {
				break
			}
			fmt.Printf("  %-40s  %s  (completed: %s)\n",
				truncate(e.RepoPath, 40), e.CommitHash[:8],
				e.CompletedAt.Format("2006-01-02 15:04:05"))
			count++
		}
		fmt.Println()
	}

	if len(failed) > 0 {
		fmt.Println("Failed backups:")
		for _, e := range failed {
			fmt.Printf("  %-40s  %s  (error: %s)\n",
				truncate(e.RepoPath, 40), e.CommitHash[:8], e.LastError)
		}
		fmt.Println()
	}

	return nil
}

// cmdHealth runs a health check on all configured repositories.
func cmdHealth(configFlag string) error {
	configPath, err := config.Locate(configFlag)
	if err != nil {
		return fmt.Errorf("locating configuration: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	paths := make([]string, 0, len(cfg.Repositories))
	for _, repo := range cfg.Repositories {
		paths = append(paths, repo.Path)
	}

	results := git.CheckAllHealth(paths)

	fmt.Println("AutoGit Repository Health Report")
	fmt.Println("=================================")
	fmt.Println()

	healthyCount := 0
	degradedCount := 0
	unhealthyCount := 0

	for _, result := range results {
		switch result.Status {
		case git.Healthy:
			healthyCount++
		case git.Degraded:
			degradedCount++
		case git.Unhealthy:
			unhealthyCount++
		}

		// Print status icon and path.
		switch result.Status {
		case git.Healthy:
			fmt.Printf("✓ %s\n", result.Path)
		case git.Degraded:
			fmt.Printf("⚠ %s\n", result.Path)
		case git.Unhealthy:
			fmt.Printf("✗ %s\n", result.Path)
		}

		fmt.Printf("  Status: %s\n", result.Status)

		if result.Exists {
			fmt.Printf("  Path exists: yes\n")
		} else {
			fmt.Printf("  Path exists: no\n")
		}

		if result.IsGitRepo {
			fmt.Printf("  Git repository: yes\n")
		} else {
			fmt.Printf("  Git repository: no\n")
		}

		if result.Branch != "" {
			fmt.Printf("  Branch: %s\n", result.Branch)
		}

		if result.HasRemote {
			fmt.Printf("  Remote: %s (%s)\n", result.RemoteName, result.RemoteURL)
		} else {
			fmt.Printf("  Remote: not configured\n")
		}

		if result.HasRemote {
			if result.RemoteReachable {
				fmt.Printf("  Remote reachable: yes\n")
			} else {
				fmt.Printf("  Remote reachable: no\n")
			}

			if result.AuthOK {
				fmt.Printf("  Authentication: OK\n")
			} else {
				fmt.Printf("  Authentication: failed\n")
			}
		}

		if result.Busy {
			fmt.Printf("  Git operation in progress: %s\n", result.BusyReason)
		}

		if result.RemoteReachable {
			if result.BehindCount > 0 {
				fmt.Printf("  Behind remote: %d commit(s)\n", result.BehindCount)
			}
			if result.AheadCount > 0 {
				fmt.Printf("  Ahead of remote: %d commit(s)\n", result.AheadCount)
			}
			if result.BehindCount == 0 && result.AheadCount == 0 {
				fmt.Printf("  Sync status: in sync with remote\n")
			}
		}

		if len(result.Issues) > 0 {
			fmt.Printf("  Issues:\n")
			for _, issue := range result.Issues {
				fmt.Printf("    - %s\n", issue)
			}
		}

		if len(result.Warnings) > 0 {
			fmt.Printf("  Warnings:\n")
			for _, warning := range result.Warnings {
				fmt.Printf("    - %s\n", warning)
			}
		}

		fmt.Println()
	}

	// Summary.
	fmt.Println("Summary")
	fmt.Println("-------")
	fmt.Printf("Total repositories: %d\n", len(results))
	fmt.Printf("  Healthy:   %d\n", healthyCount)
	fmt.Printf("  Degraded:  %d\n", degradedCount)
	fmt.Printf("  Unhealthy: %d\n", unhealthyCount)

	if unhealthyCount > 0 || degradedCount > 0 {
		return fmt.Errorf("one or more repositories have health issues")
	}

	return nil
}

// cmdStorage displays a storage usage report for all configured repositories.
func cmdStorage(configFlag string) error {
	// Parse storage-specific flags including --config.
	sf := flag.NewFlagSet("storage", flag.ContinueOnError)
	sfConfig := sf.String("config", configFlag, "path to the configuration file")
	repoFlag := sf.String("repo", "", "target a specific repository path")
	formatFlag := sf.String("format", "text", "output format: text or json")
	topFilesFlag := sf.Int("top-files", 10, "number of largest files to show per snapshot")
	detailsFlag := sf.Bool("details", false, "include per-file breakdown and growth analysis")
	sf.Parse(os.Args[2:])

	configPath, err := config.Locate(*sfConfig)
	if err != nil {
		return fmt.Errorf("locating configuration: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Determine which repository(s) to analyze.
	var repoPaths []string
	if *repoFlag != "" {
		repoPaths = []string{*repoFlag}
	} else {
		repoPaths = make([]string, 0, len(cfg.Repositories))
		for _, repo := range cfg.Repositories {
			repoPaths = append(repoPaths, repo.Path)
		}
	}

	if len(repoPaths) == 0 {
		fmt.Println("No repositories configured.")
		return nil
	}

	backupOptions := git.Options{
		Mode:    cfg.Backup.Mode,
		Prefix:  cfg.Backup.Prefix,
		Machine: cfg.Backup.Machine,
		Retention: git.RetentionCfg{
			MaxAge:     cfg.Backup.Retention.MaxAge,
			MaxCommits: cfg.Backup.Retention.MaxCommits,
		},
	}

	summary := git.CheckAllStorage(repoPaths, backupOptions, *topFilesFlag)

	switch *formatFlag {
	case "json":
		printStorageJSON(summary)
	default:
		printStorageText(summary, *detailsFlag)
	}

	return nil
}

// printStorageText formats the storage summary as a human-readable report.
func printStorageText(summary git.StorageSummary, details bool) {
	fmt.Println("AutoGit Storage Usage Report")
	fmt.Println("============================")
	fmt.Println()

	for _, r := range summary.Reports {
		fmt.Printf("Repository: %s\n", r.Path)
		if r.Error != "" {
			fmt.Printf("  Error: %s\n\n", r.Error)
			continue
		}
		fmt.Printf("  .git directory size:    %s\n", formatBytes(r.GitDirSize))
		fmt.Printf("  Backup commits:         %d\n", r.BackupCommits)
		fmt.Printf("  Backup object size:     %s\n", formatBytes(r.BackupObjectSize))
		fmt.Printf("  Latest snapshot size:   %s\n", formatBytes(r.LatestSnapshotSize))

		if r.GrowthRate.TotalCommits > 0 {
			fmt.Printf("  Growth rate:            %.2f commits/day\n", r.GrowthRate.CommitsPerDay)
			fmt.Printf("  First backup:           %s\n", r.GrowthRate.FirstBackup.Format("2006-01-02"))
			fmt.Printf("  Last backup:            %s\n", r.GrowthRate.LastBackup.Format("2006-01-02"))
		}

		if details && len(r.TopFiles) > 0 {
			fmt.Printf("  Top %d largest files:\n", len(r.TopFiles))
			for i, f := range r.TopFiles {
				fmt.Printf("    %2d. %-50s %s\n", i+1, f.Path, formatBytes(f.Size))
			}
		}

		fmt.Println()
	}

	// Summary totals.
	fmt.Println("Summary")
	fmt.Println("-------")
	fmt.Printf("Total repositories:     %d\n", len(summary.Reports))
	fmt.Printf("Total .git size:        %s\n", formatBytes(summary.TotalGitDirSize))
	fmt.Printf("Total backup commits:   %d\n", summary.TotalBackupCommits)
	fmt.Printf("Total backup objects:   %s\n", formatBytes(summary.TotalBackupObjectSize))

	if len(summary.Recommendations) > 0 {
		fmt.Println()
		fmt.Println("Recommendations")
		fmt.Println("---------------")
		for _, rec := range summary.Recommendations {
			fmt.Printf("  • %s\n", rec)
		}
	}
}

// printStorageJSON outputs the storage summary as JSON.
func printStorageJSON(summary git.StorageSummary) {
	type jsonLargeFile struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	type jsonGrowthRate struct {
		CommitsPerDay float64   `json:"commitsPerDay"`
		FirstBackup   time.Time `json:"firstBackup"`
		LastBackup    time.Time `json:"lastBackup"`
		TotalCommits  int       `json:"totalCommits"`
	}
	type jsonReport struct {
		Path               string          `json:"path"`
		GitDirSize         int64           `json:"gitDirSize"`
		BackupCommits      int             `json:"backupCommits"`
		BackupObjectSize   int64           `json:"backupObjectSize"`
		LatestSnapshotSize int64           `json:"latestSnapshotSize"`
		TopFiles           []jsonLargeFile `json:"topFiles"`
		GrowthRate         jsonGrowthRate  `json:"growthRate"`
		Error              string          `json:"error,omitempty"`
	}
	type jsonSummary struct {
		Reports             []jsonReport `json:"reports"`
		TotalGitDirSize     int64        `json:"totalGitDirSize"`
		TotalBackupCommits  int          `json:"totalBackupCommits"`
		TotalBackupObjectSize int64      `json:"totalBackupObjectSize"`
		Recommendations     []string     `json:"recommendations"`
	}

	reports := make([]jsonReport, 0, len(summary.Reports))
	for _, r := range summary.Reports {
		jr := jsonReport{
			Path:               r.Path,
			GitDirSize:         r.GitDirSize,
			BackupCommits:      r.BackupCommits,
			BackupObjectSize:   r.BackupObjectSize,
			LatestSnapshotSize: r.LatestSnapshotSize,
			GrowthRate: jsonGrowthRate{
				CommitsPerDay: r.GrowthRate.CommitsPerDay,
				FirstBackup:   r.GrowthRate.FirstBackup,
				LastBackup:    r.GrowthRate.LastBackup,
				TotalCommits:  r.GrowthRate.TotalCommits,
			},
			Error: r.Error,
		}
		for _, f := range r.TopFiles {
			jr.TopFiles = append(jr.TopFiles, jsonLargeFile{Path: f.Path, Size: f.Size})
		}
		reports = append(reports, jr)
	}

	js := jsonSummary{
		Reports:             reports,
		TotalGitDirSize:     summary.TotalGitDirSize,
		TotalBackupCommits:  summary.TotalBackupCommits,
		TotalBackupObjectSize: summary.TotalBackupObjectSize,
		Recommendations:     summary.Recommendations,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(js)
}

// formatBytes returns a human-readable byte size string (e.g., "1.5 MB").
func formatBytes(b int64) string {
	if b == 0 {
		return "0 B"
	}

	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}

	div := float64(b)
	exp := 0
	units := []string{"B", "KB", "MB", "GB", "TB"}
	for n := div; n >= float64(unit) && exp < len(units)-1; n = n / float64(unit) {
		div /= float64(unit)
		exp++
	}

	return fmt.Sprintf("%.1f %s", div, units[exp])
}

// cmdRestore handles the restore subcommand for recovering work from backup snapshots.
func cmdRestore(configFlag string) error {
	configPath, err := config.Locate(configFlag)
	if err != nil {
		return fmt.Errorf("locating configuration: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Parse restore-specific flags from os.Args[2:] since the flag.FlagSet already
	// consumed -config. We need to re-parse for restore-specific flags.
	opFlag := flag.NewFlagSet("restore", flag.ContinueOnError)
	op := opFlag.String("op", "list", "operation: list, show, diff, file, restore")
	hash := opFlag.String("hash", "", "snapshot commit hash")
	hashFrom := opFlag.String("from", "", "source snapshot hash (for diff)")
	hashTo := opFlag.String("to", "", "target snapshot hash or WORKTREE (for diff)")
	filePath := opFlag.String("file", "", "file path within the snapshot (for file op)")
	destPath := opFlag.String("dest", "", "destination path for restored file")
	destDir := opFlag.String("dir", "", "destination directory for full snapshot restore")
	force := opFlag.Bool("force", false, "restore into current working tree (requires explicit confirmation)")
	machine := opFlag.String("machine", "", "filter by machine name")
	branch := opFlag.String("branch", "", "filter by branch name")
	repoFlag := opFlag.String("repo", "", "target a specific repository path")

	opFlag.Parse(os.Args[2:])

	backupOptions := git.Options{
		Mode:    cfg.Backup.Mode,
		Prefix:  cfg.Backup.Prefix,
		Machine: cfg.Backup.Machine,
		Retention: git.RetentionCfg{
			MaxAge:     cfg.Backup.Retention.MaxAge,
			MaxCommits: cfg.Backup.Retention.MaxCommits,
		},
	}

	// Determine which repository(s) to operate on.
	var repoPaths []string
	if *repoFlag != "" {
		repoPaths = []string{*repoFlag}
	} else {
		repoPaths = make([]string, 0, len(cfg.Repositories))
		for _, repo := range cfg.Repositories {
			repoPaths = append(repoPaths, repo.Path)
		}
	}

	switch *op {
	case "list":
		return restoreList(repoPaths, backupOptions, *machine, *branch)

	case "show":
		if len(repoPaths) != 1 {
			return fmt.Errorf("show requires exactly one repository (use --repo)")
		}
		if *hash == "" {
			return fmt.Errorf("show requires --hash")
		}
		return restoreShow(repoPaths[0], *hash)

	case "diff":
		if len(repoPaths) != 1 {
			return fmt.Errorf("diff requires exactly one repository (use --repo)")
		}
		if *hashFrom == "" || *hashTo == "" {
			return fmt.Errorf("diff requires --from and --to")
		}
		return restoreDiff(repoPaths[0], *hashFrom, *hashTo)

	case "file":
		if len(repoPaths) != 1 {
			return fmt.Errorf("file requires exactly one repository (use --repo)")
		}
		if *hash == "" || *filePath == "" || *destPath == "" {
			return fmt.Errorf("file requires --hash, --file, and --dest")
		}
		return restoreFile(repoPaths[0], *hash, *filePath, *destPath)

	case "restore":
		if len(repoPaths) != 1 {
			return fmt.Errorf("restore requires exactly one repository (use --repo)")
		}
		if *hash == "" {
			return fmt.Errorf("restore requires --hash")
		}
		if *destDir != "" {
			return restoreToDir(repoPaths[0], backupOptions, *hash, *destDir)
		} else if *force {
			return restoreToWorktree(repoPaths[0], *hash)
		} else {
			return fmt.Errorf("restore requires either --dir <path> or --force to restore into the current working tree")
		}

	default:
		return fmt.Errorf("unknown operation %q (use list, show, diff, file, or restore)", *op)
	}
}

func restoreList(repoPaths []string, opts git.Options, machine, branch string) error {
	if len(repoPaths) == 0 {
		fmt.Println("No repositories configured.")
		return nil
	}

	found := false
	for _, path := range repoPaths {
		snapshots, err := git.ListSnapshots(path, opts, machine, branch)
		if err != nil {
			fmt.Printf("Error listing snapshots for %s: %v\n", path, err)
			continue
		}

		if len(snapshots) == 0 {
			fmt.Printf("%s: no snapshots found\n", path)
			continue
		}

		found = true
		fmt.Printf("Snapshots for %s:\n", path)
		fmt.Println(strings.Repeat("-", 100))
		fmt.Printf("%-12s  %-20s  %-15s  %s\n", "Hash", "Date", "Machine/Branch", "Message")
		fmt.Println(strings.Repeat("-", 100))

		for _, s := range snapshots {
			machineBranch := ""
			if s.Machine != "" {
				machineBranch = s.Machine + "/" + s.Branch
			}
			dateStr := s.Date.Format("2006-01-02 15:04")
			msg := s.Message
			if len(msg) > 50 {
				msg = msg[:47] + "..."
			}
			fmt.Printf("%-12s  %-20s  %-15s  %s\n", s.ShortHash, dateStr, machineBranch, msg)
		}
		fmt.Println()
	}

	if !found {
		fmt.Println("No snapshots found.")
	}

	return nil
}

func restoreShow(path, hash string) error {
	changes, err := git.SnapshotDiff(path, hash, "WORKTREE")
	if err != nil {
		return err
	}

	fmt.Printf("Files changed between snapshot %s and current working tree:\n", hash[:7])
	fmt.Println()

	if len(changes) == 0 {
		fmt.Println("  (no differences)")
		return nil
	}

	for _, c := range changes {
		status := c.Status
		switch status {
		case "M":
			status = "modified"
		case "A":
			status = "added"
		case "D":
			status = "deleted"
		case "R":
			status = "renamed"
		}
		fmt.Printf("  %-10s %s\n", status, c.Path)
	}

	return nil
}

func restoreDiff(path, fromHash, toHash string) error {
	changes, err := git.SnapshotDiff(path, fromHash, toHash)
	if err != nil {
		return err
	}

	toLabel := toHash[:7]
	if toHash == "WORKTREE" {
		toLabel = "working tree"
	}

	fmt.Printf("Files changed between %s and %s:\n", fromHash[:7], toLabel)
	fmt.Println()

	if len(changes) == 0 {
		fmt.Println("  (no differences)")
		return nil
	}

	for _, c := range changes {
		status := c.Status
		switch status {
		case "M":
			status = "modified"
		case "A":
			status = "added"
		case "D":
			status = "deleted"
		case "R":
			if c.From != "" {
				status = fmt.Sprintf("renamed from %s", c.From)
			} else {
				status = "renamed"
			}
		}
		fmt.Printf("  %-20s %s\n", status, c.Path)
	}

	return nil
}

func restoreFile(path, hash, filePath, destPath string) error {
	if err := git.RestoreFile(path, hash, filePath, destPath); err != nil {
		return err
	}

	fmt.Printf("Restored %s from snapshot %s to %s\n", filePath, hash[:7], destPath)
	return nil
}

func restoreToDir(path string, opts git.Options, hash, destDir string) error {
	// Preview changes first.
	changes, err := git.SnapshotDiff(path, hash, "WORKTREE")
	if err == nil && len(changes) > 0 {
		fmt.Printf("Restoring %d file(s) from snapshot %s to %s\n", len(changes), hash[:7], destDir)
	}

	if err := git.RestoreSnapshotToDir(path, opts, hash, destDir); err != nil {
		return err
	}

	fmt.Printf("Snapshot %s restored to %s\n", hash[:7], destDir)
	return nil
}

func restoreToWorktree(path, hash string) error {
	// Preview changes before modifying the working tree.
	changes, err := git.SnapshotDiff(path, hash, "WORKTREE")
	if err != nil {
		return err
	}

	if len(changes) > 0 {
		fmt.Println("The following files will be modified in your working tree:")
		fmt.Println()
		for _, c := range changes {
			status := c.Status
			switch status {
			case "M":
				status = "modified"
			case "A":
				status = "added"
			case "D":
				status = "deleted"
			}
			fmt.Printf("  %-10s %s\n", status, c.Path)
		}
		fmt.Println()
		fmt.Printf("Restoring %d file(s) from snapshot %s into the current working tree...\n", len(changes), hash[:7])
	} else {
		fmt.Println("No changes to restore — working tree already matches the snapshot.")
		return nil
	}

	if err := git.RestoreSnapshotToWorktree(path, hash); err != nil {
		return err
	}

	fmt.Printf("Snapshot %s restored to working tree.\n", hash[:7])
	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return "..." + s[len(s)-maxLen+3:]
}

// cmdHistory handles the history subcommand for exploring snapshot history.
func cmdHistory(configFlag string) error {
	configPath, err := config.Locate(configFlag)
	if err != nil {
		return fmt.Errorf("locating configuration: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Parse history-specific flags.
	hf := flag.NewFlagSet("history", flag.ContinueOnError)
	repoFlag := hf.String("repo", "", "target a specific repository path")
	machine := hf.String("machine", "", "filter by machine name")
	branch := hf.String("branch", "", "filter by branch name")
	search := hf.String("search", "", "search commit messages")
	format := hf.String("format", "text", "output format: text or json")
	limit := hf.Int("limit", 0, "maximum number of snapshots to show (0 = no limit)")
	chronological := hf.Bool("chronological", false, "show oldest-first instead of newest-first")
	details := hf.Bool("details", false, "include file lists, size estimates, and sync status")

	hf.Parse(os.Args[2:])

	backupOptions := git.Options{
		Mode:    cfg.Backup.Mode,
		Prefix:  cfg.Backup.Prefix,
		Machine: cfg.Backup.Machine,
		Retention: git.RetentionCfg{
			MaxAge:     cfg.Backup.Retention.MaxAge,
			MaxCommits: cfg.Backup.Retention.MaxCommits,
		},
	}

	// Determine which repository(s) to operate on.
	var repoPaths []string
	if *repoFlag != "" {
		repoPaths = []string{*repoFlag}
	} else {
		repoPaths = make([]string, 0, len(cfg.Repositories))
		for _, repo := range cfg.Repositories {
			repoPaths = append(repoPaths, repo.Path)
		}
	}

	filter := git.SnapshotFilter{
		Machine:       *machine,
		Branch:        *branch,
		Search:        *search,
		Limit:         *limit,
		Chronological: *chronological,
	}

	var allSnapshots []git.SnapshotInfo

	for _, path := range repoPaths {
		snapshots, err := git.ListSnapshotHistory(path, backupOptions, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading history for %s: %v\n", path, err)
			continue
		}

		if *details {
			git.EnrichSnapshots(path, backupOptions, snapshots, true, true, true)
		}

		allSnapshots = append(allSnapshots, snapshots...)
	}

	// Re-apply limit after merging across repositories.
	if *limit > 0 && len(allSnapshots) > *limit {
		allSnapshots = allSnapshots[:*limit]
	}

	if len(allSnapshots) == 0 {
		fmt.Println("No snapshots found.")
		return nil
	}

	switch *format {
	case "json":
		printHistoryJSON(allSnapshots)
	default:
		printHistoryText(allSnapshots, *details)
	}

	return nil
}

func printHistoryText(snapshots []git.SnapshotInfo, withDetails bool) {
	fmt.Printf("Snapshot History (%d snapshots)\n", len(snapshots))
	fmt.Println(strings.Repeat("-", 120))
	fmt.Printf("%-12s  %-20s  %-15s  %-10s  %s\n", "Hash", "Date", "Machine/Branch", "Synced", "Message")
	fmt.Println(strings.Repeat("-", 120))

	for _, s := range snapshots {
		machineBranch := ""
		if s.Machine != "" {
			machineBranch = s.Machine + "/" + s.Branch
		}
		dateStr := s.Date.Format("2006-01-02 15:04")
		synced := "no"
		if s.Synced {
			synced = "yes"
		}
		msg := s.Message
		if len(msg) > 50 {
			msg = msg[:47] + "..."
		}
		fmt.Printf("%-12s  %-20s  %-15s  %-10s  %s\n", s.ShortHash, dateStr, machineBranch, synced, msg)

		if withDetails {
			if len(s.Files) > 0 {
				fmt.Printf("  Files (%d): %s\n", len(s.Files), strings.Join(s.Files[:min(len(s.Files), 5)], ", "))
				if len(s.Files) > 5 {
					fmt.Printf("  ... and %d more\n", len(s.Files)-5)
				}
			}
			if s.Size > 0 {
				fmt.Printf("  Size: %s\n", formatSize(s.Size))
			}
		}
	}
	fmt.Println()
}

func printHistoryJSON(snapshots []git.SnapshotInfo) {
	type jsonSnapshot struct {
		Hash      string   `json:"hash"`
		ShortHash string   `json:"shortHash"`
		Date      string   `json:"date"`
		Author    string   `json:"author"`
		Machine   string   `json:"machine"`
		Branch    string   `json:"branch"`
		Message   string   `json:"message"`
		FileCount int      `json:"fileCount"`
		Files     []string `json:"files,omitempty"`
		Size      int64    `json:"size"`
		Synced    bool     `json:"synced"`
	}

	var result []jsonSnapshot
	for _, s := range snapshots {
		js := jsonSnapshot{
			Hash:      s.Hash,
			ShortHash: s.ShortHash,
			Date:      s.Date.Format(time.RFC3339),
			Author:    s.Author,
			Machine:   s.Machine,
			Branch:    s.Branch,
			Message:   s.Message,
			FileCount: len(s.Files),
			Size:      s.Size,
			Synced:    s.Synced,
		}
		if len(s.Files) > 0 {
			js.Files = s.Files
		}
		result = append(result, js)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// cmdCompare handles the compare subcommand for comparing two snapshots.
func cmdCompare(configFlag string) error {
	configPath, err := config.Locate(configFlag)
	if err != nil {
		return fmt.Errorf("locating configuration: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	cf := flag.NewFlagSet("compare", flag.ContinueOnError)
	repoFlag := cf.String("repo", "", "target a specific repository path")
	fromHash := cf.String("from", "", "source snapshot hash")
	toHash := cf.String("to", "", "target snapshot hash or WORKTREE")
	filePath := cf.String("file", "", "show diff for a specific file only")
	format := cf.String("format", "text", "output format: text or json")

	cf.Parse(os.Args[2:])

	if *fromHash == "" || *toHash == "" {
		return fmt.Errorf("compare requires --from and --to")
	}

	var repoPaths []string
	if *repoFlag != "" {
		repoPaths = []string{*repoFlag}
	} else {
		repoPaths = make([]string, 0, len(cfg.Repositories))
		for _, repo := range cfg.Repositories {
			repoPaths = append(repoPaths, repo.Path)
		}
	}

	if len(repoPaths) != 1 {
		return fmt.Errorf("compare requires exactly one repository (use --repo)")
	}

	path := repoPaths[0]

	if *filePath != "" {
		// Show diff for a single file.
		fileDiff, err := git.GetFileDiff(path, *fromHash, *toHash, *filePath)
		if err != nil {
			return err
		}

		switch *format {
		case "json":
			printFileDiffJSON(fileDiff)
		default:
			printFileDiffText(fileDiff)
		}
		return nil
	}

	// Full comparison.
	comparison, err := git.CompareSnapshots(path, *fromHash, *toHash)
	if err != nil {
		return err
	}

	switch *format {
	case "json":
		printComparisonJSON(comparison)
	default:
		printComparisonText(comparison)
	}

	return nil
}

func printComparisonText(c *git.SnapshotComparison) {
	toLabel := c.ToHash[:7]
	if c.ToHash == "WORKTREE" {
		toLabel = "working tree"
	}

	fmt.Printf("Comparing %s to %s\n", c.FromHash[:7], toLabel)
	fmt.Printf("Summary: %s\n", c.Summary())
	fmt.Println(strings.Repeat("-", 80))

	if len(c.Added) > 0 {
		fmt.Println("\nAdded files:")
		for _, f := range c.Added {
			marker := ""
			if f.Binary {
				marker = " (binary)"
			}
			fmt.Printf("  + %s%s\n", f.Path, marker)
		}
	}

	if len(c.Modified) > 0 {
		fmt.Println("\nModified files:")
		for _, f := range c.Modified {
			marker := ""
			if f.Binary {
				marker = " (binary)"
			}
			fmt.Printf("  M %s%s\n", f.Path, marker)
			if !f.Binary && f.DiffText != "" {
				fmt.Println(f.DiffText)
			}
		}
	}

	if len(c.Deleted) > 0 {
		fmt.Println("\nDeleted files:")
		for _, f := range c.Deleted {
			marker := ""
			if f.Binary {
				marker = " (binary)"
			}
			fmt.Printf("  - %s%s\n", f.Path, marker)
		}
	}

	if len(c.Renamed) > 0 {
		fmt.Println("\nRenamed files:")
		for _, f := range c.Renamed {
			marker := ""
			if f.Binary {
				marker = " (binary)"
			}
			fmt.Printf("  R %s -> %s%s\n", f.From, f.Path, marker)
			if !f.Binary && f.DiffText != "" {
				fmt.Println(f.DiffText)
			}
		}
	}

	fmt.Println()
}

func printComparisonJSON(c *git.SnapshotComparison) {
	type jsonFileDiff struct {
		Path   string `json:"path"`
		From   string `json:"from,omitempty"`
		Status string `json:"status"`
		Binary bool   `json:"binary"`
		Diff   string `json:"diff,omitempty"`
	}

	toJSON := func(fd git.FileDiff) jsonFileDiff {
		return jsonFileDiff{
			Path:   fd.Path,
			From:   fd.From,
			Status: fd.Status,
			Binary: fd.Binary,
			Diff:   fd.DiffText,
		}
	}

	type jsonComparison struct {
		FromHash string         `json:"fromHash"`
		ToHash   string         `json:"toHash"`
		Summary  string         `json:"summary"`
		Added    []jsonFileDiff `json:"added"`
		Modified []jsonFileDiff `json:"modified"`
		Deleted  []jsonFileDiff `json:"deleted"`
		Renamed  []jsonFileDiff `json:"renamed"`
	}

	result := jsonComparison{
		FromHash: c.FromHash,
		ToHash:   c.ToHash,
		Summary:  c.Summary(),
		Added:    make([]jsonFileDiff, len(c.Added)),
		Modified: make([]jsonFileDiff, len(c.Modified)),
		Deleted:  make([]jsonFileDiff, len(c.Deleted)),
		Renamed:  make([]jsonFileDiff, len(c.Renamed)),
	}

	for i, f := range c.Added {
		result.Added[i] = toJSON(f)
	}
	for i, f := range c.Modified {
		result.Modified[i] = toJSON(f)
	}
	for i, f := range c.Deleted {
		result.Deleted[i] = toJSON(f)
	}
	for i, f := range c.Renamed {
		result.Renamed[i] = toJSON(f)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}

func printFileDiffText(fd *git.FileDiff) {
	status := fd.Status
	switch status {
	case "M":
		status = "modified"
	case "A":
		status = "added"
	case "D":
		status = "deleted"
	case "R":
		if fd.From != "" {
			status = fmt.Sprintf("renamed from %s", fd.From)
		} else {
			status = "renamed"
		}
	}

	fmt.Printf("File: %s\n", fd.Path)
	fmt.Printf("Status: %s\n", status)
	if fd.Binary {
		fmt.Println("Type: binary (diff not available)")
	} else {
		fmt.Println("Type: text")
		if fd.DiffText != "" {
			fmt.Println()
			fmt.Println(fd.DiffText)
		} else {
			fmt.Println("(no changes)")
		}
	}
	fmt.Println()
}

func printFileDiffJSON(fd *git.FileDiff) {
	type jsonFileDiff struct {
		Path   string `json:"path"`
		From   string `json:"from,omitempty"`
		Status string `json:"status"`
		Binary bool   `json:"binary"`
		Diff   string `json:"diff,omitempty"`
	}

	result := jsonFileDiff{
		Path:   fd.Path,
		From:   fd.From,
		Status: fd.Status,
		Binary: fd.Binary,
		Diff:   fd.DiffText,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}

// cmdCleanup previews or executes backup branch cleanup.
func cmdCleanup(configFlag string) error {
	configPath, err := config.Locate(configFlag)
	if err != nil {
		return fmt.Errorf("locating configuration: %w", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Parse cleanup-specific flags.
	cf := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	dryRun := cf.Bool("dry-run", true, "preview cleanup without modifying branches (default)")
	force := cf.Bool("force", false, "execute cleanup, removing pruned snapshots")
	repoFlag := cf.String("repo", "", "target a specific repository path")
	cf.Parse(os.Args[2:])

	if *force {
		*dryRun = false
	}

	// Determine which repository(s) to operate on.
	var repos []config.Repository
	if *repoFlag != "" {
		for _, r := range cfg.Repositories {
			if r.Path == *repoFlag {
				repos = append(repos, r)
				break
			}
		}
		if len(repos) == 0 {
			return fmt.Errorf("repository %q not found in configuration", *repoFlag)
		}
	} else {
		repos = cfg.Repositories
	}

	if len(repos) == 0 {
		fmt.Println("No repositories configured.")
		return nil
	}

	// Build base backup options.
	baseOpts := git.Options{
		Mode:    cfg.Backup.Mode,
		Prefix:  cfg.Backup.Prefix,
		Machine: cfg.Backup.Machine,
	}

	totalSnapshots := 0
	totalKept := 0
	totalPruned := 0
	totalProtected := 0

	for _, repo := range repos {
		// Use per-repo retention if configured, otherwise global.
		retention := cfg.GetRetention(repo.Path)
		if !retention.ShouldPrune() {
			fmt.Printf("\n%s: no retention policy configured, skipping\n", repo.Path)
			continue
		}

		opts := baseOpts
		opts.Retention = git.RetentionCfg{
			MaxAge:      retention.MaxAge,
			MaxCommits:  retention.MaxCommits,
			KeepDaily:   retention.KeepDaily,
			KeepWeekly:  retention.KeepWeekly,
			KeepMonthly: retention.KeepMonthly,
		}

		names, err := git.NewBackupNames(repo.Path, opts)
		if err != nil {
			fmt.Printf("\n%s: error building backup names: %v\n", repo.Path, err)
			continue
		}

		kept, protected, err := git.EvaluateRetention(repo.Path, names, opts.Retention)
		if err != nil {
			fmt.Printf("\n%s: error evaluating retention: %v\n", repo.Path, err)
			continue
		}

		// List all snapshots for display.
		allCommits, err := git.ListSnapshotCommitsWithDate(repo.Path, names)
		if err != nil {
			fmt.Printf("\n%s: error listing snapshots: %v\n", repo.Path, err)
			continue
		}

		if len(allCommits) == 0 {
			fmt.Printf("\n%s: no snapshots found\n", repo.Path)
			continue
		}

		keptSet := make(map[string]bool)
		for _, h := range kept {
			keptSet[h] = true
		}
		protectedSet := make(map[string]bool)
		for _, h := range protected {
			protectedSet[h] = true
		}

		fmt.Printf("\n%s (%d snapshots)\n", repo.Path, len(allCommits))
		fmt.Println(strings.Repeat("-", 90))
		fmt.Printf("%-12s  %-20s  %s\n", "Hash", "Date", "Status")
		fmt.Println(strings.Repeat("-", 90))

		repoKept := 0
		repoPruned := 0
		repoProtected := 0
		for _, c := range allCommits {
			shortHash := c.Hash[:12]
			dateStr := c.Date.Format("2006-01-02 15:04")
			status := "prune"
			if keptSet[c.Hash] {
				status = "keep"
				repoKept++
			}
			if protectedSet[c.Hash] {
				status = "protected"
				repoProtected++
			}
			if keptSet[c.Hash] && !protectedSet[c.Hash] {
				// already counted
			}
			if !keptSet[c.Hash] {
				repoPruned++
			}
			fmt.Printf("%-12s  %-20s  %s\n", shortHash, dateStr, status)
		}

		fmt.Printf("\nSummary: %d kept, %d pruned, %d protected\n", repoKept, repoPruned, repoProtected)

		totalSnapshots += len(allCommits)
		totalKept += repoKept
		totalPruned += repoPruned
		totalProtected += repoProtected

		if !*dryRun {
			// Execute the actual pruning.
			pruned, err := git.PruneBackupBranch(repo.Path, names, opts.Retention)
			if err != nil {
				fmt.Printf("Error pruning %s: %v\n", repo.Path, err)
				continue
			}
			if pruned > 0 {
				fmt.Printf("Pruned %d snapshot(s) from %s\n", pruned, repo.Path)
			}
		}
	}

	if *dryRun {
		fmt.Printf("\n--- Dry run: no changes made ---\n")
	}

	fmt.Printf("\nTotal: %d snapshots, %d kept, %d pruned, %d protected\n",
		totalSnapshots, totalKept, totalPruned, totalProtected)

	return nil
}

// editorPollInterval is how often AutoGit checks whether an editor is open.
const editorPollInterval = 5 * time.Second

// watcher turns "an editor is open" into running backups.
//
// While an editor is open it backs up immediately and then on every scheduler
// tick. When the last editor closes it runs one final backup, so the edits
// made just before closing are not left uncommitted until the next session.
type watcher struct {
	isRunning func() bool          // reports whether a configured editor is open
	pollEvery time.Duration        // how often to call isRunning
	scheduler *scheduler.Scheduler // legacy single-task scheduler (nil when repoScheduler is set)
	repoSched *scheduler.RepoScheduler // per-repo scheduler with policies (nil when scheduler is set)
	backup    func()               // one backup pass over every configured repository
	backupOne func(string) (bool, error) // single-repo backup (used by repoSched)
	notify    notify.Sender        // sends desktop notifications for state changes

	// pull, when set, runs once each time an editor opens, before the first
	// backup, to bring in commits made elsewhere. It may be nil.
	pull func()

	// verify, when set, runs periodically to check that backups are recoverable.
	// It may be nil when periodic verification is not configured.
	verify func()
}

// run blocks until ctx is cancelled.
//
// Backups never overlap: the opening backup and the scheduled ones run on one
// goroutine, and the closing backup starts only after that goroutine has
// finished.
//
// Shutting AutoGit down (Ctrl+C, SIGTERM) deliberately skips the final backup,
// so that stopping the program stays quick.
func (w *watcher) run(ctx context.Context) {
	poll := time.NewTicker(w.pollEvery)
	defer poll.Stop()

	var stop, done chan struct{}

	// Periodic verification runs independently of the editor state.
	// It starts when verification is configured and continues until shutdown.
	var verifyStop, verifyDone chan struct{}
	if w.verify != nil {
		verifyStop = make(chan struct{})
		verifyDone = make(chan struct{})

		go func(stop <-chan struct{}, done chan<- struct{}) {
			defer close(done)

			// Wait a minute before the first verification so the initial backup
			// has a chance to run first.
			timer := time.NewTimer(1 * time.Minute)
			select {
			case <-timer.C:
			case <-stop:
				timer.Stop()
				return
			}

			// Run verification immediately, then on a 24h interval.
			w.verify()

			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					w.verify()
				case <-stop:
					return
				}
			}
		}(verifyStop, verifyDone)
	}

	for {
		running := w.isRunning()

		switch {
		case running && stop == nil:
			stop = make(chan struct{})
			done = make(chan struct{})

			go func(stop <-chan struct{}, done chan<- struct{}) {
				defer close(done)

				// Start from the latest code: take in anything pushed from another
				// computer before looking at your own changes.
				if w.pull != nil {
					w.pull()
				}

				// Catch up on anything that changed while no editor was open,
				// instead of waiting a full interval for the first backup.
				w.backup()

				// Start the appropriate scheduler.
				if w.repoSched != nil {
					w.repoSched.Start(stop, w.backupOne)
				} else {
					w.scheduler.Start(stop)
				}
			}(stop, done)

			log.Println("A configured editor is open. AutoGit backups enabled.")
			w.notify.Success("AutoGit: Backups Enabled", "A configured editor is open; backups are now active.")

		case !running && stop != nil:
			close(stop)
			<-done

			stop = nil
			done = nil

			log.Println("No configured editors are open. Running a final backup.")
			w.backup()
			log.Println("AutoGit backups paused.")
			w.notify.Success("AutoGit: Backups Paused", "No editors are open; backups are paused until you open one.")
		}

		select {
		case <-poll.C:
		case <-ctx.Done():
			if stop != nil {
				close(stop)
				<-done
			}

			if verifyStop != nil {
				close(verifyStop)
				<-verifyDone
			}

			return
		}
	}
}

package main

import (
	"context"
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
	"github.com/Joshua504/auto-git/internal/scheduler"
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

	nc := notify.New(cfg.NotificationsEnabled())

	backupTask := func() {
		for _, repository := range cfg.Repositories {
			log.Printf("Checking repository: %s", repository.Path)

			// Track whether the repo had changes so we can notify on a real push.
			hadChanges, _ := git.HasChanges(repository.Path)

			if err := git.BackupWith(repository.Path, cfg.Commit.Message, backupOptions); err != nil {
				// A busy repository is not a failure; the next cycle tries again.
				var skipped *git.SkippedError
				if errors.As(err, &skipped) {
					log.Printf("Skipping %s: %s", repository.Path, skipped.Reason)
					nc.Warning("AutoGit: Skipped", fmt.Sprintf("%s — %s", repository.Path, skipped.Reason))
					continue
				}

				log.Printf("Backup failed for %s: %v", repository.Path, err)
				nc.Error("AutoGit: Backup Failed", fmt.Sprintf("%s — %v", repository.Path, err))
				continue
			}

			log.Printf("Backup check completed for %s", repository.Path)
			if hadChanges {
				nc.Success("AutoGit: Backup Complete", fmt.Sprintf("Pushed changes from %s", repository.Path))
			}
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

	s, err := scheduler.New(interval, backupTask)
	if err != nil {
		log.Fatalf("Error creating scheduler: %v", err)
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
	fmt.Println("AutoGit is waiting for a configured editor to open.")

	w := &watcher{
		isRunning: func() bool { return editor.IsRunning(cfg.Editors) },
		pollEvery: editorPollInterval,
		scheduler: s,
		backup:    backupTask,
		notify:    nc,
	}

	if pullOnOpen {
		w.pull = pullTask
	}

	w.run(ctx)
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
	scheduler *scheduler.Scheduler // runs backup periodically while an editor is open
	backup    func()               // one backup pass over every configured repository
	notify    notify.Sender        // sends desktop notifications for state changes

	// pull, when set, runs once each time an editor opens, before the first
	// backup, to bring in commits made elsewhere. It may be nil.
	pull func()
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

				w.scheduler.Start(stop)
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

			return
		}
	}
}

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
	"syscall"
	"time"

	"github.com/Joshua504/auto-git/internal/config"
	"github.com/Joshua504/auto-git/internal/editor"
	"github.com/Joshua504/auto-git/internal/git"
	"github.com/Joshua504/auto-git/internal/scheduler"
)

func main() {
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

	backupTask := func() {
		for _, repository := range cfg.Repositories {
			log.Printf("Checking repository: %s", repository.Path)

			if err := git.Backup(repository.Path, cfg.Commit.Message); err != nil {
				// A busy repository is not a failure; the next cycle tries again.
				var skipped *git.SkippedError
				if errors.As(err, &skipped) {
					log.Printf("Skipping %s: %s", repository.Path, skipped.Reason)
					continue
				}

				log.Printf("Backup failed for %s: %v", repository.Path, err)
				continue
			}

			log.Printf("Backup check completed for %s", repository.Path)
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
	fmt.Printf("Repositories configured: %d\n", len(cfg.Repositories))
	fmt.Printf("Editors monitored: %v\n", cfg.Editors)
	fmt.Println("AutoGit is waiting for a configured editor to open.")

	w := &watcher{
		isRunning: func() bool { return editor.IsRunning(cfg.Editors) },
		pollEvery: editorPollInterval,
		scheduler: s,
		backup:    backupTask,
	}

	w.run(ctx)
	fmt.Println("AutoGit stopped.")
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

				// Catch up on anything that changed while no editor was open,
				// instead of waiting a full interval for the first backup.
				w.backup()

				w.scheduler.Start(stop)
			}(stop, done)

			log.Println("A configured editor is open. AutoGit backups enabled.")

		case !running && stop != nil:
			close(stop)
			<-done

			stop = nil
			done = nil

			log.Println("No configured editors are open. Running a final backup.")
			w.backup()
			log.Println("AutoGit backups paused.")
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

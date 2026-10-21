package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Joshua504/auto-git/internal/config"
	"github.com/Joshua504/auto-git/internal/editor"
	"github.com/Joshua504/auto-git/internal/git"
	"github.com/Joshua504/auto-git/internal/scheduler"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
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

	fmt.Println("AutoGit configuration loaded successfully.")
	fmt.Printf("Backup interval: %s\n", cfg.Interval)
	fmt.Printf("Repositories configured: %d\n", len(cfg.Repositories))
	fmt.Printf("Editors monitored: %v\n", cfg.Editors)
	fmt.Println("AutoGit is waiting for a configured editor to open.")

	monitorEditors(ctx, cfg.Editors, s)
	fmt.Println("AutoGit stopped.")
}

func monitorEditors(ctx context.Context, editors []string, s *scheduler.Scheduler) {
	checkInterval := time.NewTicker(5 * time.Second)
	defer checkInterval.Stop()

	var stop chan struct{}
	var done chan struct{}

	for {
		running := editor.IsRunning(editors)

		if running && stop == nil {
			stop = make(chan struct{})
			done = make(chan struct{})

			currentStop := stop
			currentDone := done

			go func() {
				defer close(currentDone)
				s.Start(currentStop)
			}()

			log.Println("A configured editor is open. AutoGit backups enabled.")
		} else if !running && stop != nil {
			close(stop)
			<-done

			stop = nil
			done = nil

			log.Println("No configured editors are open. AutoGit backups paused.")
		}

		select {
		case <-checkInterval.C:
		case <-ctx.Done():
			if stop != nil {
				close(stop)
				<-done
			}
			return
		}
	}
}

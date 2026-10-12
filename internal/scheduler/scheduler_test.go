package scheduler

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	gitbackup "github.com/Joshua504/auto-git/internal/git"
)

func TestNew(t *testing.T) {
	interval := time.Hour
	task := func() {}

	s, err := New(interval, task)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if s == nil {
		t.Fatal("expected a scheduler, got nil")
	}

	if s.interval != interval {
		t.Errorf("expected interval %v, got %v", interval, s.interval)
	}

	if s.task == nil {
		t.Fatal("expected task to be set, got nil")
	}
}

func TestNewZeroInterval(t *testing.T) {
	task := func() {}

	s, err := New(0, task)

	if err == nil {
		t.Fatal("expected an error for zero interval, got nil")
	}

	if s != nil {
		t.Fatal("expected a nil scheduler for zero interval")
	}
}

func TestNewNegativeInterval(t *testing.T) {
	task := func() {}

	s, err := New(-time.Hour, task)

	if err == nil {
		t.Fatal("expected an error for negative interval, got nil")
	}

	if s != nil {
		t.Fatal("expected a nil scheduler for negative interval")
	}
}

func TestNewNilTask(t *testing.T) {
	s, err := New(time.Hour, nil)

	if err == nil {
		t.Fatal("expected an error for nil task, got nil")
	}

	if s != nil {
		t.Fatal("expected a nil scheduler for nil task")
	}
}

func TestStartRunsTask(t *testing.T) {
	ran := make(chan struct{}, 1)

	task := func() {
		ran <- struct{}{}
	}

	s, err := New(10*time.Millisecond, task)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		s.Start(stop)
	}()

	select {
	case <-ran:
		close(stop)
		<-done

	case <-time.After(200 * time.Millisecond):
		close(stop)
		<-done
		t.Fatal("expected task to run, but it did not")
	}
}

func TestStartRunsTaskRepeatedly(t *testing.T) {
	ran := make(chan struct{}, 5)

	task := func() {
		ran <- struct{}{}
	}

	s, err := New(10*time.Millisecond, task)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		s.Start(stop)
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-ran:
			// One scheduled execution occurred.

		case <-time.After(200 * time.Millisecond):
			close(stop)
			<-done
			t.Fatalf("expected task execution %d, but it did not occur", i+1)
		}
	}

	close(stop)
	<-done
}

func TestStartStops(t *testing.T) {
	s, err := New(time.Hour, func() {})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		s.Start(stop)
	}()

	close(stop)

	select {
	case <-done:
		// Scheduler stopped successfully.

	case <-time.After(200 * time.Millisecond):
		t.Fatal("scheduler did not stop after stop signal")
	}
}

func TestScheduledBackup(t *testing.T) {
	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote.git")
	localPath := filepath.Join(tempDir, "local")

	// Create a temporary bare repository as the remote.
	cmd := exec.Command("git", "init", "--bare", remotePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create remote: %v\n%s", err, output)
	}

	// Create the local repository.
	if err := os.Mkdir(localPath, 0755); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "-C", localPath, "init")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to initialize local repository: %v\n%s", err, output)
	}

	// Configure Git identity for the test repository.
	for key, value := range map[string]string{
		"user.name":  "AutoGit Test",
		"user.email": "autogit-test@example.com",
	} {
		cmd = exec.Command("git", "-C", localPath, "config", key, value)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to configure Git: %v\n%s", err, output)
		}
	}

	// Connect the local repository to the temporary remote.
	cmd = exec.Command(
		"git", "-C", localPath,
		"remote", "add", "origin", remotePath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to configure remote: %v\n%s", err, output)
	}

	// Create a file that the scheduled backup should commit.
	filePath := filepath.Join(localPath, "test.txt")
	if err := os.WriteFile(filePath, []byte("scheduled backup"), 0644); err != nil {
		t.Fatal(err)
	}

	completed := make(chan struct{}, 1)
	taskErrors := make(chan error, 1)

	task := func() {
		if err := gitbackup.Backup(localPath, "scheduled backup test"); err != nil {
			select {
			case taskErrors <- err:
			default:
			}
			return
		}

		select {
		case completed <- struct{}{}:
		default:
		}
	}

	s, err := New(10*time.Millisecond, task)
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		s.Start(stop)
	}()

	select {
	case err := <-taskErrors:
		close(stop)
		<-done
		t.Fatalf("scheduled backup failed: %v", err)

	case <-completed:
		close(stop)
		<-done

	case <-time.After(3 * time.Second):
		close(stop)
		<-done
		t.Fatal("scheduled backup did not complete")
	}

	// Verify that the scheduled backup pushed a branch to the remote.
	cmd = exec.Command("git", "--git-dir", remotePath, "show-ref")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote did not receive the backup: %v\n%s", err, output)
	}

	// Verify the commit message on the remote branch.
	cmd = exec.Command("git", "--git-dir", remotePath, "log", "-1", "--pretty=%s", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to read remote commit: %v", err)
	}

	if string(output) != "scheduled backup test\n" {
		t.Errorf("unexpected remote commit message: %q", output)
	}
}

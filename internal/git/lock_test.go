package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLockFile(t *testing.T) {
	f := newSyncFixture(t)

	lf := lockFile(f.local)
	if !strings.Contains(lf, "autogit.lock") {
		t.Fatalf("expected lock file name to contain autogit.lock, got %q", lf)
	}

	// The lock file lives inside .git, not in the working tree.
	gitDir := mustGit(t, f.local, "rev-parse", "--git-dir")
	if !strings.Contains(lf, gitDir) {
		t.Fatalf("expected lock file inside %q, got %q", gitDir, lf)
	}
}

func TestAcquireLockExclusive(t *testing.T) {
	f := newSyncFixture(t)

	// First acquisition succeeds.
	lock1, skipped := acquireLock(f.local)
	if skipped != nil {
		t.Fatalf("first lock should succeed: %v", skipped)
	}
	defer releaseLock(lock1)

	// Second acquisition from the same process should also be blocked by flock.
	_, skipped = acquireLock(f.local)
	if skipped == nil {
		t.Fatal("second lock should be blocked while the first is held")
	}

	if !strings.Contains(skipped.Reason, "another AutoGit instance") {
		t.Fatalf("unexpected skip reason: %s", skipped.Reason)
	}
}

func TestConcurrentBackupsSerialized(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// Run two backups concurrently on the same repository.
	// They should serialize via the lock, not race.
	var wg sync.WaitGroup
	var errors []string
	var mu sync.Mutex

	wg.Add(2)

	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()

			// Add a small delay so both goroutines try to acquire the lock
			// at roughly the same time.
			time.Sleep(50 * time.Millisecond)

			err := BackupWith(f.local, "chore: automatic backup", Options{
				Mode:    ModeBranch,
				Machine: "testbox",
			})

			if err != nil {
				// One of them may be skipped because the other holds the lock.
				if !os.IsTimeout(err) && !strings.Contains(err.Error(), "backup skipped") {
					mu.Lock()
					errors = append(errors, err.Error())
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	if len(errors) > 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}
}

func TestConcurrentSyncOnOpenSerialized(t *testing.T) {
	f := newSyncFixture(t)

	// Push a commit from the other side so SyncOnOpen has something to pull.
	f.pushFromOther(t, "remote.txt", "remote\n", "remote work")

	var wg sync.WaitGroup
	var results []OpenSyncOutcome
	var mu sync.Mutex

	wg.Add(2)

	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()

			time.Sleep(50 * time.Millisecond)

			result, err := SyncOnOpen(f.local)
			if err != nil {
				// One may be skipped due to the lock.
				if !strings.Contains(err.Error(), "backup skipped") {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}

			mu.Lock()
			results = append(results, result.Outcome)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// At least one should succeed (fast-forwarded or up to date).
	if len(results) == 0 {
		t.Fatal("both SyncOnOpen calls were skipped or failed")
	}
}

func TestLockReleasedOnError(t *testing.T) {
	// Create a repository with no remote, so BackupWith will fail.
	tempDir := t.TempDir()
	local := filepath.Join(tempDir, "local")

	if out, err := exec.Command("git", "init", "-b", "main", local).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	configureIdentity(t, local)

	writeFile(t, filepath.Join(local, "a.txt"), "a\n")
	mustGit(t, local, "add", ".")
	mustGit(t, local, "commit", "-m", "initial")

	// BackupWith will fail because there's no remote, but the lock should
	// still be released. Try to acquire the lock afterward.
	BackupWith(local, "chore: backup", Options{Mode: ModeCommit})

	// If the lock was not released, this would return a SkippedError.
	lock, skipped := acquireLock(local)
	if skipped != nil {
		t.Fatalf("lock should have been released after error: %v", skipped)
	}
	releaseLock(lock)
}

func TestLockReleasedOnSyncError(t *testing.T) {
	// Create a repository with no remote.
	tempDir := t.TempDir()
	local := filepath.Join(tempDir, "local")

	if out, err := exec.Command("git", "init", "-b", "main", local).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	configureIdentity(t, local)

	writeFile(t, filepath.Join(local, "a.txt"), "a\n")
	mustGit(t, local, "add", ".")
	mustGit(t, local, "commit", "-m", "initial")

	// SyncOnOpen will fail (no remote), but the lock should be released.
	SyncOnOpen(local)

	lock, skipped := acquireLock(local)
	if skipped != nil {
		t.Fatalf("lock should have been released after SyncOnOpen error: %v", skipped)
	}
	releaseLock(lock)
}

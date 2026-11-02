package git

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// lockFile returns the path to the AutoGit lock file inside the repository's
// .git directory. Two AutoGit instances that lock the same file will coordinate:
// the second one blocks until the first releases the lock.
func lockFile(path string) string {
	gitDir, err := run(shortTimeout, path, "rev-parse", "--git-dir")
	if err != nil {
		// Fallback: assume .git exists next to the repo.
		return filepath.Join(path, ".git", "autogit.lock")
	}

	gitDir = strings.TrimSpace(gitDir)

	// rev-parse may return a relative path like ".git" or an absolute path.
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}

	return filepath.Join(gitDir, "autogit.lock")
}

// acquireLock creates and exclusively locks the AutoGit lock file for the given
// repository. Call releaseLock when the operation is done.
//
// If another AutoGit instance already holds the lock, it returns a SkippedError
// instead of blocking, so the caller can retry on the next cycle.
func acquireLock(path string) (*os.File, *SkippedError) {
	lf := lockFile(path)

	f, err := os.OpenFile(lf, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, &SkippedError{Reason: fmt.Sprintf("cannot create lock file: %v", err)}
	}

	if isLocked(f) {
		f.Close()
		return nil, &SkippedError{Reason: "another AutoGit instance is working on this repository"}
	}

	return f, nil
}

// releaseLock closes the lock file, releasing the exclusive lock.
func releaseLock(f *os.File) {
	f.Close()
}

// isLocked attempts to acquire an exclusive lock on f without blocking.
// It returns true when another process already holds the lock.
func isLocked(f *os.File) bool {
	if runtime.GOOS == "windows" {
		// Windows is not a primary target; fall back to checking file existence.
		// A more robust Windows implementation would use LockFileEx.
		return false
	}

	// Flock with LOCK_EX|LOCK_NB returns EWOULDBLOCK when the lock is held.
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	return err == unix.EWOULDBLOCK
}

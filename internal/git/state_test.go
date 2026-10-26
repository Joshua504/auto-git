package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireSkipped fails unless err is a SkippedError whose reason contains want.
func requireSkipped(t *testing.T, err error, want string) {
	t.Helper()

	var skipped *SkippedError

	if !errors.As(err, &skipped) {
		t.Fatalf("expected the backup to be skipped, got: %v", err)
	}

	if !strings.Contains(skipped.Reason, want) {
		t.Fatalf("expected the skip reason to mention %q, got %q", want, skipped.Reason)
	}
}

// snapshot captures everything a skipped backup must leave alone.
type snapshot struct {
	head   string
	status string
	remote string
}

func takeSnapshot(t *testing.T, f syncFixture) snapshot {
	t.Helper()

	return snapshot{
		head:   mustGit(t, f.local, "rev-parse", "HEAD"),
		status: mustGit(t, f.local, "status", "--porcelain"),
		remote: mustGit(t, f.remote, "rev-parse", "main"),
	}
}

func requireUntouched(t *testing.T, f syncFixture, before snapshot) {
	t.Helper()

	after := takeSnapshot(t, f)

	if after != before {
		t.Fatalf("a skipped backup changed the repository:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

// conflictingEdits leaves local one commit ahead and the remote one commit
// ahead, both editing the same line, with the remote commit already fetched.
func conflictingEdits(t *testing.T, f syncFixture) {
	t.Helper()

	f.pushFromOther(t, "base.txt", "remote version\n", "remote edit")

	writeFile(t, filepath.Join(f.local, "base.txt"), "local version\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "local edit")
	mustGit(t, f.local, "fetch")
}

// gitAllowedToFail runs git and ignores a non-zero exit. Setting up a conflict
// requires a command that is expected to fail.
func gitAllowedToFail(dir string, args ...string) {
	_ = exec.Command("git", append([]string{"-C", dir}, args...)...).Run()
}

func TestBackupSkipsDuringRebase(t *testing.T) {
	f := newSyncFixture(t)

	conflictingEdits(t, f)
	gitAllowedToFail(f.local, "rebase", "@{upstream}")

	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")

	before := takeSnapshot(t, f)

	requireSkipped(t, Backup(f.local, "chore: automatic backup"), "rebase")
	requireUntouched(t, f, before)

	if !strings.Contains(before.status, "?? new.txt") || !strings.Contains(before.status, "base.txt") {
		t.Fatalf("test setup is wrong, expected a conflicted rebase, got status:\n%s", before.status)
	}
}

func TestBackupSkipsDuringMerge(t *testing.T) {
	f := newSyncFixture(t)

	conflictingEdits(t, f)
	gitAllowedToFail(f.local, "merge", "origin/main")

	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")

	before := takeSnapshot(t, f)

	requireSkipped(t, Backup(f.local, "chore: automatic backup"), "merge")
	requireUntouched(t, f, before)
}

func TestBackupSkipsWithUnresolvedConflicts(t *testing.T) {
	f := newSyncFixture(t)

	// "git stash pop" can leave conflicts behind with no merge or rebase marker.
	writeFile(t, filepath.Join(f.local, "base.txt"), "stashed version\n")
	mustGit(t, f.local, "stash")

	writeFile(t, filepath.Join(f.local, "base.txt"), "committed version\n")
	mustGit(t, f.local, "commit", "-am", "other edit")

	gitAllowedToFail(f.local, "stash", "pop")

	if out := mustGit(t, f.local, "ls-files", "--unmerged"); out == "" {
		t.Fatal("test setup is wrong, expected unmerged files")
	}

	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")

	before := takeSnapshot(t, f)

	requireSkipped(t, Backup(f.local, "chore: automatic backup"), "unresolved merge conflicts")
	requireUntouched(t, f, before)
}

func TestBackupSkipsWhileIndexIsLocked(t *testing.T) {
	f := newSyncFixture(t)

	lockPath := mustGit(t, f.local, "rev-parse", "--git-path", lockFileName)
	if !filepath.IsAbs(lockPath) {
		lockPath = filepath.Join(f.local, lockPath)
	}

	writeFile(t, lockPath, "")
	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")

	before := takeSnapshot(t, f)

	requireSkipped(t, Backup(f.local, "chore: automatic backup"), lockFileName)
	requireUntouched(t, f, before)

	// Once the other git process is done, the very next backup goes through.
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatalf("expected the backup to run once the lock was gone: %v", err)
	}

	if !remoteHas(f.remote, "new.txt") {
		t.Fatal("expected new.txt to be backed up after the lock cleared")
	}
}

func TestBackupSkipsOnDetachedHead(t *testing.T) {
	f := newSyncFixture(t)

	mustGit(t, f.local, "checkout", "--detach")
	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")

	before := takeSnapshot(t, f)

	requireSkipped(t, Backup(f.local, "chore: automatic backup"), "detached")
	requireUntouched(t, f, before)
}

func TestBackupReportsMissingIdentity(t *testing.T) {
	tempDir := t.TempDir()
	remote := filepath.Join(tempDir, "remote.git")
	local := filepath.Join(tempDir, "local")

	if out, err := exec.Command("git", "init", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	if err := os.Mkdir(local, 0755); err != nil {
		t.Fatal(err)
	}

	mustGit(t, local, "init", "-b", "main")
	mustGit(t, local, "remote", "add", "origin", remote)

	writeFile(t, filepath.Join(local, "base.txt"), "base\n")
	mustGit(t, local, "add", ".")
	mustGit(t, local, "-c", "user.name=Setup", "-c", "user.email=setup@example.com", "commit", "-m", "init")
	mustGit(t, local, "push", "-u", "origin", "HEAD")

	// Refuse to guess an identity from the hostname, so the result does not
	// depend on the machine running the test.
	mustGit(t, local, "config", "user.useConfigOnly", "true")

	writeFile(t, filepath.Join(local, "new.txt"), "new\n")

	headBefore := mustGit(t, local, "rev-parse", "HEAD")

	err := Backup(local, "chore: automatic backup")
	if err == nil {
		t.Fatal("expected an error when no git identity is configured")
	}

	var skipped *SkippedError
	if errors.As(err, &skipped) {
		t.Fatalf("a missing identity needs the user to act, so it must not be a quiet skip: %v", err)
	}

	if !strings.Contains(err.Error(), "user.name") || !strings.Contains(err.Error(), "user.email") {
		t.Fatalf("expected the error to say how to fix it, got: %v", err)
	}

	if got := mustGit(t, local, "rev-parse", "HEAD"); got != headBefore {
		t.Fatal("no commit may be made without an identity")
	}

	// Nothing was staged before the check failed.
	if status := mustGit(t, local, "status", "--porcelain"); status != "?? new.txt" {
		t.Fatalf("expected new.txt to be left untouched and unstaged, got %q", status)
	}
}

func TestSkipIfLocked(t *testing.T) {
	if skipIfLocked(nil) != nil {
		t.Fatal("nil must stay nil")
	}

	plain := errors.New("git push failed: exit status 1: rejected")
	if got := skipIfLocked(plain); got != plain {
		t.Fatalf("an unrelated error must pass through unchanged, got %v", got)
	}

	locked := errors.New("git add failed: exit status 128: fatal: Unable to create '/repo/.git/index.lock': File exists.")

	var skipped *SkippedError
	if !errors.As(skipIfLocked(locked), &skipped) {
		t.Fatal("expected a lock error to become a skip")
	}

	if !strings.Contains(skipped.Reason, "/repo/.git/index.lock") {
		t.Fatalf("expected the lock file path in the reason, got %q", skipped.Reason)
	}
}

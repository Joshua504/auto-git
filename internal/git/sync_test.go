package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain prepares the test environment.
//
// It shortens the retry delay so retry-related tests do not sleep, and it
// isolates every git command from the developer's own git configuration.
// Without that, a global ignore file that already ignores ".env" would stop
// the guard tests from ever seeing the file, and tests would pass or fail
// depending on whose machine they run on.
func TestMain(m *testing.M) {
	retryDelay = 10 * time.Millisecond

	home, err := os.MkdirTemp("", "autogit-test-home")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating isolated home directory:", err)
		os.Exit(1)
	}

	for key, value := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_NOSYSTEM": "1",
	} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, "setting", key+":", err)
			os.Exit(1)
		}
	}

	// Identity set through the environment would hide the "no identity" case.
	for _, key := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL",
	} {
		_ = os.Unsetenv(key)
	}

	code := m.Run()

	_ = os.RemoveAll(home)

	os.Exit(code)
}

// mustGit runs a git command in dir and fails the test on error.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}

	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func configureIdentity(t *testing.T, dir string) {
	t.Helper()

	mustGit(t, dir, "config", "user.name", "AutoGit Test")
	mustGit(t, dir, "config", "user.email", "autogit-test@example.com")
}

// syncFixture is a bare remote plus two working clones that share it.
// "local" is the repository AutoGit would back up; "other" simulates
// a second computer pushing to the same remote.
type syncFixture struct {
	remote string
	local  string
	other  string
}

func newSyncFixture(t *testing.T) syncFixture {
	t.Helper()

	tempDir := t.TempDir()

	f := syncFixture{
		remote: filepath.Join(tempDir, "remote.git"),
		local:  filepath.Join(tempDir, "local"),
		other:  filepath.Join(tempDir, "other"),
	}

	// Pin the branch name so the tests do not depend on init.defaultBranch.
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", f.remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}

	if err := os.Mkdir(f.local, 0755); err != nil {
		t.Fatal(err)
	}

	mustGit(t, f.local, "init", "-b", "main")
	configureIdentity(t, f.local)
	mustGit(t, f.local, "remote", "add", "origin", f.remote)

	writeFile(t, filepath.Join(f.local, "base.txt"), "base\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "initial commit")
	mustGit(t, f.local, "push", "-u", "origin", "HEAD")

	if out, err := exec.Command("git", "clone", f.remote, f.other).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	configureIdentity(t, f.other)

	return f
}

// pushFromOther commits a file in the second clone and pushes it.
func (f syncFixture) pushFromOther(t *testing.T, name, content, message string) {
	t.Helper()

	// Catch up first: the other clone may be behind the local one.
	mustGit(t, f.other, "pull", "--rebase", "-q")

	writeFile(t, filepath.Join(f.other, name), content)
	mustGit(t, f.other, "add", ".")
	mustGit(t, f.other, "commit", "-m", message)
	mustGit(t, f.other, "push", "origin", "HEAD")
}

func TestIsBehindRemote(t *testing.T) {
	f := newSyncFixture(t)

	behind, err := IsBehindRemote(f.local)
	if err != nil {
		t.Fatal(err)
	}

	if behind {
		t.Fatal("expected local to be in sync before the remote changes")
	}

	f.pushFromOther(t, "other.txt", "other\n", "remote-only commit")

	if err := Fetch(f.local); err != nil {
		t.Fatal(err)
	}

	behind, err = IsBehindRemote(f.local)
	if err != nil {
		t.Fatal(err)
	}

	if !behind {
		t.Fatal("expected local to be behind after the remote received a new commit")
	}
}

func TestIsBehindRemoteNoUpstream(t *testing.T) {
	tempDir := t.TempDir()

	mustGit(t, tempDir, "init", "-b", "main")
	configureIdentity(t, tempDir)

	writeFile(t, filepath.Join(tempDir, "a.txt"), "a\n")
	mustGit(t, tempDir, "add", ".")
	mustGit(t, tempDir, "commit", "-m", "first")

	behind, err := IsBehindRemote(tempDir)
	if err != nil {
		t.Fatalf("a branch with no upstream should not be an error: %v", err)
	}

	if behind {
		t.Fatal("a branch with no upstream cannot be behind")
	}
}

func TestPullRebasesLocalCommitOntoRemote(t *testing.T) {
	f := newSyncFixture(t)

	f.pushFromOther(t, "other.txt", "other\n", "remote-only commit")

	writeFile(t, filepath.Join(f.local, "local.txt"), "local\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "local-only commit")

	if err := Fetch(f.local); err != nil {
		t.Fatal(err)
	}

	if err := Pull(f.local); err != nil {
		t.Fatalf("expected a clean rebase: %v", err)
	}

	// Both files must now exist locally.
	for _, name := range []string{"other.txt", "local.txt"} {
		if _, err := os.Stat(filepath.Join(f.local, name)); err != nil {
			t.Fatalf("expected %s after rebase: %v", name, err)
		}
	}

	if got := mustGit(t, f.local, "rev-list", "--count", "HEAD..@{upstream}"); got != "0" {
		t.Fatalf("expected no remote commits missing locally, got %s", got)
	}
}

func TestBackupWhenRemoteIsAhead(t *testing.T) {
	f := newSyncFixture(t)

	f.pushFromOther(t, "other.txt", "other\n", "remote-only commit")

	// Uncommitted local work that AutoGit should commit, rebase and push.
	writeFile(t, filepath.Join(f.local, "local.txt"), "local\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatalf("backup should succeed when the remote is ahead: %v", err)
	}

	// The remote must contain both sides' work.
	for _, name := range []string{"other.txt", "local.txt"} {
		mustGit(t, f.remote, "cat-file", "-e", "main:"+name)
	}

	if got := mustGit(t, f.local, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); got != "0\t0" {
		t.Fatalf("expected local and upstream to match, got %q", got)
	}
}

func TestBackupRebaseConflictIsAbortedAndNotPushed(t *testing.T) {
	f := newSyncFixture(t)

	// Both sides change the same line of the same file.
	f.pushFromOther(t, "base.txt", "remote version\n", "remote edit")
	remoteHead := mustGit(t, f.remote, "rev-parse", "main")

	writeFile(t, filepath.Join(f.local, "base.txt"), "local version\n")

	err := Backup(f.local, "chore: automatic backup")
	if err == nil {
		t.Fatal("expected Backup to fail on a rebase conflict")
	}

	// It must fail because of the rebase, not because a push was rejected.
	if !strings.Contains(err.Error(), "rebase aborted") {
		t.Fatalf("expected a rebase failure, got: %v", err)
	}

	// No rebase may be left in progress.
	rebaseDir := mustGit(t, f.local, "rev-parse", "--git-path", "rebase-merge")
	if !filepath.IsAbs(rebaseDir) {
		rebaseDir = filepath.Join(f.local, rebaseDir)
	}

	if _, statErr := os.Stat(rebaseDir); statErr == nil {
		t.Fatal("rebase was left in progress")
	}

	// The working tree must be clean and the local commit preserved.
	if status := mustGit(t, f.local, "status", "--porcelain"); status != "" {
		t.Fatalf("expected a clean working tree, got:\n%s", status)
	}

	if msg := mustGit(t, f.local, "log", "-1", "--format=%s"); msg != "chore: automatic backup" {
		t.Fatalf("expected the local backup commit to be preserved, got %q", msg)
	}

	// The remote must be untouched.
	if got := mustGit(t, f.remote, "rev-parse", "main"); got != remoteHead {
		t.Fatalf("remote changed despite the failed rebase: %s -> %s", remoteHead, got)
	}
}

func TestFetchTimesOut(t *testing.T) {
	tempDir := t.TempDir()

	mustGit(t, tempDir, "init", "-b", "main")

	// An "ext::" remote runs an arbitrary command; "sleep" never answers,
	// which simulates a hung network operation.
	mustGit(t, tempDir, "config", "protocol.ext.allow", "always")
	mustGit(t, tempDir, "remote", "add", "origin", "ext::sleep 30")

	oldTimeout, oldDelay := longTimeout, waitDelay
	longTimeout, waitDelay = 300*time.Millisecond, 200*time.Millisecond

	defer func() { longTimeout, waitDelay = oldTimeout, oldDelay }()

	start := time.Now()
	err := Fetch(tempDir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected fetch to fail")
	}

	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got: %v", err)
	}

	if elapsed > 5*time.Second {
		t.Fatalf("fetch did not stop promptly: %s", elapsed)
	}
}

func TestTimeoutIsNotRetryable(t *testing.T) {
	err := fmt.Errorf("git fetch %w after 2m0s", errTimedOut)

	if isRetryableGitError(err) {
		t.Fatal("a command that hit its timeout must not be retried")
	}

	// Other "timed out" failures, such as a network timeout, still retry.
	if !isRetryableGitError(errors.New("git push failed: connection timed out")) {
		t.Fatal("a network timeout reported by git should still be retryable")
	}
}

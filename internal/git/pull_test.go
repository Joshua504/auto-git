package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// requireUnchanged fails if SyncOnOpen touched anything it promised to leave alone.
func requireUnchanged(t *testing.T, f syncFixture, before snapshot) {
	t.Helper()

	requireUntouched(t, f, before)

	if exec.Command("git", "-C", f.local, "rev-parse", "--verify", "--quiet", "MERGE_HEAD").Run() == nil {
		t.Fatal("a merge was started")
	}

	if rebase := mustGit(t, f.local, "rev-parse", "--git-path", "rebase-merge"); rebase != "" {
		if !filepath.IsAbs(rebase) {
			rebase = filepath.Join(f.local, rebase)
		}

		if _, err := os.Stat(rebase); err == nil {
			t.Fatal("a rebase was started")
		}
	}

	if stashes := mustGit(t, f.local, "stash", "list"); stashes != "" {
		t.Fatalf("something was stashed: %q", stashes)
	}
}

func TestSyncOnOpenBringsInNewCommits(t *testing.T) {
	f := newSyncFixture(t)

	f.pushFromOther(t, "other.txt", "from the other computer\n", "work done elsewhere")
	f.pushFromOther(t, "more.txt", "more\n", "more work elsewhere")

	result, err := SyncOnOpen(f.local)
	if err != nil {
		t.Fatal(err)
	}

	if result.Outcome != OpenSyncFastForwarded || result.Commits != 2 {
		t.Fatalf("expected a fast-forward of 2 commits, got %+v", result)
	}

	// An exact fast-forward: your branch now points at the remote's commit,
	// with no merge commit in between.
	if local, remote := mustGit(t, f.local, "rev-parse", "HEAD"), mustGit(t, f.remote, "rev-parse", "main"); local != remote {
		t.Fatalf("expected HEAD %s to equal the remote's %s", local, remote)
	}

	for _, name := range []string{"other.txt", "more.txt"} {
		if _, err := os.Stat(filepath.Join(f.local, name)); err != nil {
			t.Fatalf("expected %s in the working tree: %v", name, err)
		}
	}

	if status := mustGit(t, f.local, "status", "--porcelain"); status != "" {
		t.Fatalf("expected a clean tree, got %q", status)
	}
}

func TestSyncOnOpenWhenNothingIsNew(t *testing.T) {
	f := newSyncFixture(t)

	before := takeSnapshot(t, f)

	result, err := SyncOnOpen(f.local)
	if err != nil {
		t.Fatal(err)
	}

	if result.Outcome != OpenSyncUpToDate || result.Commits != 0 {
		t.Fatalf("expected up to date, got %+v", result)
	}

	requireUnchanged(t, f, before)
}

func TestSyncOnOpenKeepsYourUncommittedWork(t *testing.T) {
	f := newSyncFixture(t)

	f.pushFromOther(t, "other.txt", "from elsewhere\n", "work done elsewhere")

	// Uncommitted work that does not touch what came in: a modified file, a
	// staged file, and a new file.
	writeFile(t, filepath.Join(f.local, "base.txt"), "my edit\n")
	writeFile(t, filepath.Join(f.local, "staged.txt"), "staged\n")
	mustGit(t, f.local, "add", "staged.txt")
	writeFile(t, filepath.Join(f.local, "mine.txt"), "mine\n")

	statusBefore := mustGit(t, f.local, "status", "--porcelain")

	result, err := SyncOnOpen(f.local)
	if err != nil {
		t.Fatal(err)
	}

	if result.Outcome != OpenSyncFastForwarded {
		t.Fatalf("expected a fast-forward, got %+v", result)
	}

	if _, err := os.Stat(filepath.Join(f.local, "other.txt")); err != nil {
		t.Fatal("expected the new commit's file to arrive")
	}

	if got := mustGit(t, f.local, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("your uncommitted work changed:\nbefore:\n%s\nafter:\n%s", statusBefore, got)
	}

	if data, _ := os.ReadFile(filepath.Join(f.local, "base.txt")); string(data) != "my edit\n" {
		t.Fatalf("your edit was lost: %q", data)
	}
}

func TestSyncOnOpenRefusesWhenYourEditsOverlap(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f syncFixture)
		file  string
	}{
		{
			name: "unstaged edit of a file that changed upstream",
			setup: func(t *testing.T, f syncFixture) {
				writeFile(t, filepath.Join(f.local, "base.txt"), "my edit\n")
			},
			file: "base.txt",
		},
		{
			name: "staged edit of a file that changed upstream",
			setup: func(t *testing.T, f syncFixture) {
				writeFile(t, filepath.Join(f.local, "base.txt"), "my staged edit\n")
				mustGit(t, f.local, "add", "base.txt")
			},
			file: "base.txt",
		},
		{
			name: "new file at a path upstream also adds",
			setup: func(t *testing.T, f syncFixture) {
				writeFile(t, filepath.Join(f.local, "incoming.txt"), "my own file\n")
			},
			file: "incoming.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSyncFixture(t)

			f.pushFromOther(t, "base.txt", "remote version\n", "edit base elsewhere")
			f.pushFromOther(t, "incoming.txt", "incoming\n", "add incoming elsewhere")

			tt.setup(t, f)

			before := takeSnapshot(t, f)
			indexBefore := mustGit(t, f.local, "ls-files", "--stage")

			result, err := SyncOnOpen(f.local)
			if err != nil {
				t.Fatal(err)
			}

			if result.Outcome != OpenSyncBlocked {
				t.Fatalf("expected the pull to be blocked, got %+v", result)
			}

			if !reflect.DeepEqual(result.Conflicts, []string{tt.file}) {
				t.Fatalf("expected the blocking file to be named %q, got %v", tt.file, result.Conflicts)
			}

			requireUnchanged(t, f, before)

			if got := mustGit(t, f.local, "ls-files", "--stage"); got != indexBefore {
				t.Fatal("the index changed")
			}
		})
	}
}

func TestSyncOnOpenLeavesDivergedBranchesAlone(t *testing.T) {
	f := newSyncFixture(t)

	// You committed on this computer while the other computer pushed.
	writeFile(t, filepath.Join(f.local, "local.txt"), "local\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "local work")

	f.pushFromOther(t, "other.txt", "other\n", "work elsewhere")

	before := takeSnapshot(t, f)

	result, err := SyncOnOpen(f.local)
	if err != nil {
		t.Fatal(err)
	}

	if result.Outcome != OpenSyncDiverged || result.Commits != 1 {
		t.Fatalf("expected diverged with 1 incoming commit, got %+v", result)
	}

	// No rebase, no merge commit, no history rewritten.
	requireUnchanged(t, f, before)
}

func TestSyncOnOpenSkipsRepositoriesInTheMiddleOfSomething(t *testing.T) {
	f := newSyncFixture(t)

	conflictingEdits(t, f)
	gitAllowedToFail(f.local, "rebase", "@{upstream}")

	before := takeSnapshot(t, f)

	_, err := SyncOnOpen(f.local)
	requireSkipped(t, err, "rebase")
	requireUntouched(t, f, before)
}

func TestSyncOnOpenSkipsADetachedHead(t *testing.T) {
	f := newSyncFixture(t)

	f.pushFromOther(t, "other.txt", "other\n", "work elsewhere")
	mustGit(t, f.local, "checkout", "--detach")

	before := takeSnapshot(t, f)

	_, err := SyncOnOpen(f.local)
	requireSkipped(t, err, "detached")
	requireUntouched(t, f, before)
}

func TestSyncOnOpenWithNoUpstream(t *testing.T) {
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
	configureIdentity(t, local)
	mustGit(t, local, "remote", "add", "origin", remote)

	writeFile(t, filepath.Join(local, "a.txt"), "a\n")
	mustGit(t, local, "add", ".")
	mustGit(t, local, "commit", "-m", "first")

	// The branch was never pushed, so it has no upstream: nothing to pull.
	result, err := SyncOnOpen(local)
	if err != nil {
		t.Fatalf("a branch without an upstream is not an error: %v", err)
	}

	if result.Outcome != OpenSyncUpToDate {
		t.Fatalf("expected up to date, got %+v", result)
	}
}

func TestSyncOnOpenWhenTheRemoteIsUnreachable(t *testing.T) {
	f := newSyncFixture(t)

	mustGit(t, f.local, "remote", "set-url", "origin", filepath.Join(filepath.Dir(f.remote), "gone.git"))

	before := takeSnapshot(t, f)

	if _, err := SyncOnOpen(f.local); err == nil {
		t.Fatal("expected an error when the remote cannot be reached")
	}

	// Only the remote moved in the snapshot, which cannot be read now, so
	// compare what is local.
	if got := mustGit(t, f.local, "rev-parse", "HEAD"); got != before.head {
		t.Fatal("HEAD moved")
	}

	if got := mustGit(t, f.local, "status", "--porcelain"); got != before.status {
		t.Fatal("the working tree changed")
	}
}

func TestSyncOnOpenRejectsADirectoryThatIsNotARepository(t *testing.T) {
	_, err := SyncOnOpen(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not a Git repository") {
		t.Fatalf("expected a not-a-repository error, got: %v", err)
	}
}

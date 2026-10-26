package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The machine name is fixed so the backup branch has a predictable name.
var branchOpts = Options{Mode: ModeBranch, Machine: "testbox"}

const (
	backupBranchName = "autogit/testbox/main"
	backupRemoteRef  = "refs/heads/" + backupBranchName
	backupLocalRef   = "refs/autogit/testbox/main"
)

// remoteRefTip returns the commit a ref points to on the remote, or "" if it does not exist.
func remoteRefTip(remote, ref string) string {
	out, err := exec.Command("git", "-C", remote, "rev-parse", "--verify", "--quiet", ref).Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

func backupFile(t *testing.T, f syncFixture, name string) string {
	t.Helper()

	return mustGit(t, f.remote, "show", backupRemoteRef+":"+name)
}

func backupHasFile(f syncFixture, name string) bool {
	return exec.Command("git", "-C", f.remote, "cat-file", "-e", backupRemoteRef+":"+name).Run() == nil
}

func runBranchBackup(t *testing.T, path string) {
	t.Helper()

	if err := BackupWith(path, "chore: automatic backup", branchOpts); err != nil {
		t.Fatalf("branch backup failed: %v", err)
	}
}

func TestBranchBackupLeavesYourWorkAlone(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// A messy but realistic state: a staged file, an unstaged edit, an untracked file.
	writeFile(t, filepath.Join(f.local, "base.txt"), "edited\n")
	writeFile(t, filepath.Join(f.local, "staged.txt"), "staged\n")
	mustGit(t, f.local, "add", "staged.txt")
	writeFile(t, filepath.Join(f.local, "untracked.txt"), "untracked\n")

	before := takeSnapshot(t, f)
	indexBefore := mustGit(t, f.local, "ls-files", "--stage")

	runBranchBackup(t, f.local)

	// Branch, index, working tree and the remote's real branch are untouched.
	requireUntouched(t, f, before)

	if got := mustGit(t, f.local, "ls-files", "--stage"); got != indexBefore {
		t.Fatalf("the index changed:\nbefore:\n%s\nafter:\n%s", indexBefore, got)
	}

	if got := mustGit(t, f.local, "branch", "--list"); got != "* main" {
		t.Fatalf("expected no new local branches, got %q", got)
	}

	if _, err := os.Stat(filepath.Join(f.local, ".git", "autogit-index")); err == nil {
		t.Fatal("the scratch index was left behind")
	}

	// Everything, including staged, unstaged and untracked work, is on the backup branch.
	for name, want := range map[string]string{
		"base.txt": "edited", "staged.txt": "staged", "untracked.txt": "untracked",
	} {
		if got := backupFile(t, f, name); got != want {
			t.Errorf("%s on the backup branch = %q, want %q", name, got, want)
		}
	}
}

func TestBranchBackupBuildsAChainAndSkipsUnchangedCycles(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, "work.txt"), "one\n")
	runBranchBackup(t, f.local)

	first := remoteRefTip(f.remote, backupRemoteRef)
	if first == "" {
		t.Fatal("expected the backup branch to exist on the remote")
	}

	// Nothing changed: no new commit.
	runBranchBackup(t, f.local)

	if got := remoteRefTip(f.remote, backupRemoteRef); got != first {
		t.Fatalf("an unchanged cycle created a commit: %s -> %s", first, got)
	}

	// A change adds exactly one commit on top of the previous snapshot.
	writeFile(t, filepath.Join(f.local, "work.txt"), "two\n")
	runBranchBackup(t, f.local)

	second := remoteRefTip(f.remote, backupRemoteRef)

	if second == first {
		t.Fatal("expected a new snapshot after a change")
	}

	if parent := mustGit(t, f.remote, "rev-parse", second+"^"); parent != first {
		t.Fatalf("expected the new snapshot to follow the old one, parent is %s", parent)
	}

	if got := backupFile(t, f, "work.txt"); got != "two" {
		t.Fatalf("work.txt = %q, want two", got)
	}

	if msg := mustGit(t, f.remote, "log", "-1", "--format=%s", second); msg != "chore: automatic backup" {
		t.Fatalf("unexpected commit message %q", msg)
	}
}

func TestBranchBackupMirrorsHeadWhenNothingIsUncommitted(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	head := mustGit(t, f.local, "rev-parse", "HEAD")

	runBranchBackup(t, f.local)

	// With a clean tree the backup branch simply points at your HEAD, which
	// still protects commits you have not pushed.
	if got := remoteRefTip(f.remote, backupRemoteRef); got != head {
		t.Fatalf("expected the backup branch at HEAD %s, got %s", head, got)
	}

	runBranchBackup(t, f.local)

	if got := remoteRefTip(f.remote, backupRemoteRef); got != head {
		t.Fatalf("a second clean cycle moved the branch to %s", got)
	}
}

func TestBranchBackupProtectsUnpushedCommits(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, "a.txt"), "one\n")
	runBranchBackup(t, f.local)

	remoteMain := mustGit(t, f.remote, "rev-parse", "main")

	// You commit locally and never push your branch.
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "my work")

	mine := mustGit(t, f.local, "rev-parse", "HEAD")

	runBranchBackup(t, f.local)

	tip := remoteRefTip(f.remote, backupRemoteRef)

	// The backup branch contains your commit, so it is safe on the remote.
	mustGit(t, f.remote, "merge-base", "--is-ancestor", mine, tip)

	if fields := strings.Fields(mustGit(t, f.remote, "rev-list", "--parents", "-n", "1", tip)); len(fields) != 3 {
		t.Fatalf("expected the snapshot to join the old snapshot and your commit, got %v", fields)
	}

	// Your real branch on the remote was never touched.
	if got := mustGit(t, f.remote, "rev-parse", "main"); got != remoteMain {
		t.Fatal("branch mode must never push your own branch")
	}

	// Once joined, further clean cycles add nothing.
	runBranchBackup(t, f.local)

	if got := remoteRefTip(f.remote, backupRemoteRef); got != tip {
		t.Fatalf("expected no new commit, tip moved to %s", got)
	}
}

func TestBranchBackupWorksOnDetachedHead(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	mustGit(t, f.local, "checkout", "--detach")
	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")

	// Commit mode has to skip a detached HEAD. Branch mode backs it up.
	runBranchBackup(t, f.local)

	tip := remoteRefTip(f.remote, "refs/heads/autogit/testbox/detached")
	if tip == "" {
		t.Fatal("expected a backup branch named detached")
	}

	if got := mustGit(t, f.remote, "show", tip+":new.txt"); got != "new" {
		t.Fatalf("new.txt = %q, want new", got)
	}
}

func TestBranchBackupWorksWhileARebaseIsInProgress(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	conflictingEdits(t, f)
	gitAllowedToFail(f.local, "rebase", "@{upstream}")

	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")

	before := takeSnapshot(t, f)

	// Commit mode skips this state. Branch mode only reads your files, so it
	// can safely back up your work while you resolve the conflict.
	runBranchBackup(t, f.local)

	requireUntouched(t, f, before)

	if tip := remoteRefTip(f.remote, "refs/heads/autogit/testbox/detached"); tip == "" {
		t.Fatal("expected the in-progress work to be backed up")
	}
}

func TestBranchBackupKeepsSecretsOut(t *testing.T) {
	f := newSyncFixture(t)
	logs := captureLogs(t)

	// A tracked file that later gets a secret added.
	writeFile(t, filepath.Join(f.local, "settings.txt"), "mode=dev\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "add settings")

	writeFile(t, filepath.Join(f.local, "settings.txt"), "mode=dev\ntoken="+fakeGitHubToken()+"\n")
	writeFile(t, filepath.Join(f.local, ".env"), "TOKEN=hunter2\n")
	writeFile(t, filepath.Join(f.local, "keep.txt"), "fine\n")

	before := takeSnapshot(t, f)

	runBranchBackup(t, f.local)

	requireUntouched(t, f, before)

	if backupHasFile(f, ".env") {
		t.Fatal(".env reached the backup branch")
	}

	// The modified file is backed up as it was before the secret was added.
	if got := backupFile(t, f, "settings.txt"); got != "mode=dev" {
		t.Fatalf("the secret reached the backup branch: %q", got)
	}

	if got := backupFile(t, f, "keep.txt"); got != "fine" {
		t.Fatalf("keep.txt = %q, want fine", got)
	}

	if countLogsMentioning(*logs, ".env") != 1 || countLogsMentioning(*logs, "settings.txt") != 1 {
		t.Fatalf("expected one warning for each blocked file, got %v", *logs)
	}
}

func TestBranchBackupLosesNothingWhileOffline(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	remoteURL := mustGit(t, f.local, "remote", "get-url", "origin")
	mustGit(t, f.local, "remote", "set-url", "origin", filepath.Join(filepath.Dir(f.remote), "gone.git"))

	writeFile(t, filepath.Join(f.local, "work.txt"), "one\n")

	if err := BackupWith(f.local, "chore: automatic backup", branchOpts); err == nil {
		t.Fatal("expected an error while the remote is unreachable")
	}

	first := mustGit(t, f.local, "rev-parse", backupLocalRef)

	writeFile(t, filepath.Join(f.local, "work.txt"), "two\n")

	if err := BackupWith(f.local, "chore: automatic backup", branchOpts); err == nil {
		t.Fatal("expected an error while the remote is unreachable")
	}

	second := mustGit(t, f.local, "rev-parse", backupLocalRef)

	if second == first {
		t.Fatal("expected a second local snapshot while offline")
	}

	// The network comes back: everything is pushed, including the first snapshot.
	mustGit(t, f.local, "remote", "set-url", "origin", remoteURL)
	runBranchBackup(t, f.local)

	if got := remoteRefTip(f.remote, backupRemoteRef); got != second {
		t.Fatalf("expected the remote to catch up to %s, got %s", second, got)
	}

	mustGit(t, f.remote, "merge-base", "--is-ancestor", first, second)
}

func TestBranchBackupContinuesAfterAReinstall(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, "work.txt"), "one\n")
	runBranchBackup(t, f.local)

	old := remoteRefTip(f.remote, backupRemoteRef)

	// A fresh clone has no local snapshot ref, only what the remote has.
	mustGit(t, f.local, "update-ref", "-d", backupLocalRef)

	writeFile(t, filepath.Join(f.local, "work.txt"), "two\n")
	runBranchBackup(t, f.local)

	tip := remoteRefTip(f.remote, backupRemoteRef)

	// A push that was not a fast-forward would have been rejected.
	mustGit(t, f.remote, "merge-base", "--is-ancestor", old, tip)

	if got := backupFile(t, f, "work.txt"); got != "two" {
		t.Fatalf("work.txt = %q, want two", got)
	}
}

func TestBranchBackupJoinsDivergedHistories(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, "work.txt"), "one\n")
	runBranchBackup(t, f.local)

	mine := remoteRefTip(f.remote, backupRemoteRef)

	// Something else writes a different snapshot to the same backup branch.
	writeFile(t, filepath.Join(f.other, "elsewhere.txt"), "elsewhere\n")
	mustGit(t, f.other, "add", ".")
	mustGit(t, f.other, "commit", "-m", "from elsewhere")
	mustGit(t, f.other, "push", "--force", "origin", "HEAD:"+backupRemoteRef)

	theirs := remoteRefTip(f.remote, backupRemoteRef)

	writeFile(t, filepath.Join(f.local, "work.txt"), "two\n")
	runBranchBackup(t, f.local)

	tip := remoteRefTip(f.remote, backupRemoteRef)

	// The new snapshot descends from both, so the push was a fast-forward.
	mustGit(t, f.remote, "merge-base", "--is-ancestor", mine, tip)
	mustGit(t, f.remote, "merge-base", "--is-ancestor", theirs, tip)

	if got := backupFile(t, f, "work.txt"); got != "two" {
		t.Fatalf("work.txt = %q, want two", got)
	}
}

func TestBranchBackupInARepositoryWithNoCommits(t *testing.T) {
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

	captureLogs(t)

	// Nothing to back up yet: no branch should appear.
	runBranchBackup(t, local)

	if refs := mustGit(t, remote, "for-each-ref"); refs != "" {
		t.Fatalf("expected an empty remote, got %q", refs)
	}

	writeFile(t, filepath.Join(local, "main.go"), "package main\n")
	writeFile(t, filepath.Join(local, ".env"), "TOKEN=hunter2\n")

	runBranchBackup(t, local)

	if got := mustGit(t, remote, "show", backupRemoteRef+":main.go"); got != "package main" {
		t.Fatalf("main.go = %q", got)
	}

	if exec.Command("git", "-C", remote, "cat-file", "-e", backupRemoteRef+":.env").Run() == nil {
		t.Fatal(".env reached the backup branch")
	}

	// Your repository still has no commits, and your files are untouched.
	if exec.Command("git", "-C", local, "rev-parse", "--verify", "--quiet", "HEAD").Run() == nil {
		t.Fatal("branch mode must not create a commit on your branch")
	}

	if status := mustGit(t, local, "status", "--porcelain"); !strings.Contains(status, "?? main.go") {
		t.Fatalf("expected main.go to stay untracked, got %q", status)
	}
}

func TestBranchBackupNamesFollowTheBranch(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	mustGit(t, f.local, "checkout", "-b", "feature/login")
	writeFile(t, filepath.Join(f.local, "login.txt"), "login\n")

	runBranchBackup(t, f.local)

	// A slash in the branch name must not create a nested ref that could
	// collide with a branch called "feature".
	if tip := remoteRefTip(f.remote, "refs/heads/autogit/testbox/feature+login"); tip == "" {
		t.Fatal("expected a backup branch named after feature/login")
	}
}

func TestBranchBackupDoesNotRunHooks(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	hook := filepath.Join(f.local, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho blocked by hook >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(f.local, "work.txt"), "one\n")

	// A failing pre-commit hook stops commit mode. Snapshots do not run hooks.
	runBranchBackup(t, f.local)

	if got := backupFile(t, f, "work.txt"); got != "one" {
		t.Fatalf("work.txt = %q, want one", got)
	}
}

func TestBranchBackupNeedsAnIdentityToCommit(t *testing.T) {
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
	mustGit(t, local, "config", "user.useConfigOnly", "true")

	writeFile(t, filepath.Join(local, "work.txt"), "one\n")

	err := BackupWith(local, "chore: automatic backup", branchOpts)
	if err == nil || !strings.Contains(err.Error(), "user.name") {
		t.Fatalf("expected an error explaining how to set the identity, got: %v", err)
	}

	if refs := mustGit(t, remote, "for-each-ref"); refs != "" {
		t.Fatalf("nothing may be pushed without an identity, got %q", refs)
	}
}

func TestBackupWithRejectsBadOptions(t *testing.T) {
	f := newSyncFixture(t)

	tests := []struct {
		name    string
		opts    Options
		message string
	}{
		{"unknown mode", Options{Mode: "snapshot"}, "unknown backup mode"},
		{"prefix inside Git's own refs", Options{Mode: ModeBranch, Prefix: "heads/x"}, "mix snapshots"},
		{"prefix that is not a valid ref", Options{Mode: ModeBranch, Prefix: "a..b"}, "not a valid backup branch name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := BackupWith(f.local, "msg", tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("expected an error containing %q, got: %v", tt.message, err)
			}
		})
	}
}

func TestRefLabel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"main", "main"},
		{"feature/login", "feature+login"},
		{"my-pc", "my-pc"},
		{"My Laptop.local", "My-Laptop-local"},
		{"release-1.2", "release-1-2"},
		{"  weird..name  ", "weird-name"},
		{"", "fallback"},
		{"///", "+++"},
		{"...", "fallback"},
	}

	for _, tt := range tests {
		if got := refLabel(tt.in, "fallback"); got != tt.want {
			t.Errorf("refLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

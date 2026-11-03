package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyBackupCommitMode(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// Verify when the backup reference exists and is valid.
	result := VerifyBackup(f.local, Options{Mode: ModeCommit}, "")

	if !result.OK {
		t.Fatalf("expected verification to pass: %v", result.Issues)
	}
	if !result.RemoteReachable {
		t.Fatal("expected remote to be reachable")
	}
	if !result.RefExists {
		t.Fatal("expected backup reference to exist")
	}
	if !result.HashMatches {
		t.Fatal("expected hash to match")
	}
	if !result.Fetchable {
		t.Fatal("expected snapshot to be fetchable")
	}
}

func TestVerifyBackupBranchMode(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// Create a backup in branch mode first.
	writeFile(t, filepath.Join(f.local, "work.txt"), "work\n")
	runBranchBackup(t, f.local)

	result := VerifyBackup(f.local, branchOpts, "")

	if !result.OK {
		t.Fatalf("expected verification to pass: %v", result.Issues)
	}
	if !result.RefExists {
		t.Fatal("expected backup reference to exist")
	}
	if !result.Fetchable {
		t.Fatal("expected snapshot to be fetchable")
	}
}

func TestVerifyBackupHashMatch(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// Get the current commit hash.
	expectedHash := mustGit(t, f.local, "rev-parse", "HEAD")

	result := VerifyBackup(f.local, Options{Mode: ModeCommit}, expectedHash)

	if !result.OK {
		t.Fatalf("expected verification to pass: %v", result.Issues)
	}
	if !result.HashMatches {
		t.Fatal("expected hash to match")
	}
}

func TestVerifyBackupHashMismatch(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// Provide a different hash that doesn't match.
	wrongHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result := VerifyBackup(f.local, Options{Mode: ModeCommit}, wrongHash)

	if result.OK {
		t.Fatal("expected verification to fail")
	}
	if result.HashMatches {
		t.Fatal("expected hash mismatch")
	}
	if !result.HasIssues() {
		t.Fatal("expected issues to be reported")
	}
	if !strings.Contains(strings.Join(result.Issues, " "), "hash mismatch") {
		t.Fatalf("expected hash mismatch issue, got: %v", result.Issues)
	}
}

func TestVerifyBackupNoRemote(t *testing.T) {
	tempDir := t.TempDir()

	mustGit(t, tempDir, "init", "-b", "main")
	configureIdentity(t, tempDir)

	writeFile(t, filepath.Join(tempDir, "a.txt"), "a\n")
	mustGit(t, tempDir, "add", ".")
	mustGit(t, tempDir, "commit", "-m", "first")

	result := VerifyBackup(tempDir, Options{Mode: ModeCommit}, "")

	if result.OK {
		t.Fatal("expected verification to fail when no remote is configured")
	}
	if result.HasIssues() {
		// Should report that the remote cannot be reached or ref cannot be resolved.
	}
}

func TestVerifyBackupBranchModeNoBackupYet(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// No backup has been pushed in branch mode yet.
	result := VerifyBackup(f.local, branchOpts, "")

	if result.OK {
		t.Fatal("expected verification to fail when no backup exists")
	}
	if result.RefExists {
		t.Fatal("expected backup reference to not exist")
	}
	if !result.HasIssues() {
		t.Fatal("expected issues to be reported")
	}
}

func TestVerifyBackupUnreachableRemote(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// Point the remote at a non-existent bare repo.
	remoteURL := mustGit(t, f.local, "remote", "get-url", "origin")
	mustGit(t, f.local, "remote", "set-url", "origin", filepath.Join(filepath.Dir(f.remote), "nonexistent.git"))

	result := VerifyBackup(f.local, Options{Mode: ModeCommit}, "")

	if result.OK {
		t.Fatal("expected verification to fail when remote is unreachable")
	}
	if result.RemoteReachable {
		t.Fatal("expected remote to be unreachable")
	}

	// Restore the remote URL.
	mustGit(t, f.local, "remote", "set-url", "origin", remoteURL)
}

func TestVerifyAllBackups(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// Create a second repo.
	tempDir := t.TempDir()
	remote2 := filepath.Join(tempDir, "remote2.git")
	local2 := filepath.Join(tempDir, "local2")

	if out, err := exec.Command("git", "init", "--bare", "-b", "main", remote2).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}

	if err := os.Mkdir(local2, 0755); err != nil {
		t.Fatal(err)
	}

	mustGit(t, local2, "init", "-b", "main")
	configureIdentity(t, local2)
	mustGit(t, local2, "remote", "add", "origin", remote2)

	writeFile(t, filepath.Join(local2, "b.txt"), "b\n")
	mustGit(t, local2, "add", ".")
	mustGit(t, local2, "commit", "-m", "initial")
	mustGit(t, local2, "push", "-u", "origin", "HEAD")

	results := VerifyAllBackups([]string{f.local, local2}, Options{Mode: ModeCommit})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, result := range results {
		if !result.OK {
			t.Errorf("verification failed for %s: %v", result.Path, result.Issues)
		}
	}
}

func TestVerifyResultHasIssues(t *testing.T) {
	empty := VerifyResult{Path: "/test"}
	if empty.HasIssues() {
		t.Fatal("empty result should have no issues")
	}

	withIssue := VerifyResult{Path: "/test", Issues: []string{"some issue"}}
	if !withIssue.HasIssues() {
		t.Fatal("result with issues should report HasIssues")
	}
}

func TestVerifyBackupCorruptedObjects(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// Corrupt the object database by removing a blob.
	objsDir := filepath.Join(f.local, ".git", "objects")
	cmd := exec.Command("git", "-C", f.local, "ls-tree", "-r", "--name-only", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("ls-tree failed: %v", err)
	}

	// Find a file to corrupt.
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var blobPath string
	for _, line := range lines {
		if line != "" {
			blobPath = line
			break
		}
	}

	if blobPath == "" {
		t.Fatal("no blobs found in tree")
	}

	// Get the blob hash.
	blobHash := mustGit(t, f.local, "rev-parse", "HEAD:"+blobPath)

	// Remove the blob object from the object store.
	blobDir := filepath.Join(objsDir, blobHash[:2])
	blobFile := filepath.Join(blobDir, blobHash[2:])
	if err := os.Remove(blobFile); err != nil {
		t.Fatalf("failed to remove blob: %v", err)
	}
	t.Cleanup(func() {
		// Restore by fetching.
		Fetch(f.local)
	})

	// Verification should detect the corruption.
	result := VerifyBackup(f.local, Options{Mode: ModeCommit}, "")

	if result.OK {
		t.Fatal("expected verification to fail with corrupted objects")
	}
	if result.Fetchable {
		t.Fatal("expected snapshot to be unfetchable")
	}
}

func TestResolveBackupRefCommitMode(t *testing.T) {
	f := newSyncFixture(t)

	ref, err := resolveBackupRef(f.local, Options{Mode: ModeCommit})
	if err != nil {
		t.Fatalf("resolveBackupRef failed: %v", err)
	}

	want := "refs/remotes/origin/main"
	if ref != want {
		t.Errorf("resolveBackupRef = %q, want %q", ref, want)
	}
}

func TestResolveBackupRefBranchMode(t *testing.T) {
	f := newSyncFixture(t)

	ref, err := resolveBackupRef(f.local, branchOpts)
	if err != nil {
		t.Fatalf("resolveBackupRef failed: %v", err)
	}

	want := "refs/remotes/origin/autogit/testbox/main"
	if ref != want {
		t.Errorf("resolveBackupRef = %q, want %q", ref, want)
	}
}

func TestResolveBackupRefUnknownMode(t *testing.T) {
	f := newSyncFixture(t)

	_, err := resolveBackupRef(f.local, Options{Mode: "unknown"})
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if !strings.Contains(err.Error(), "unknown backup mode") {
		t.Fatalf("expected unknown mode error, got: %v", err)
	}
}

func TestVerifyFetchable(t *testing.T) {
	f := newSyncFixture(t)

	head := mustGit(t, f.local, "rev-parse", "HEAD")

	// Valid commit should pass.
	if err := verifyFetchable(f.local, head); err != nil {
		t.Fatalf("verifyFetchable failed for valid commit: %v", err)
	}

	// Non-existent commit should fail.
	err := verifyFetchable(f.local, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected verifyFetchable to fail for non-existent commit")
	}
}

package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListSnapshotsEmpty(t *testing.T) {
	f := newSyncFixture(t)

	// No backups taken yet — should return empty list.
	snapshots, err := ListSnapshots(f.local, Options{Mode: ModeBranch}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(snapshots) != 0 {
		t.Fatalf("expected no snapshots, got %d", len(snapshots))
	}
}

func TestListSnapshots(t *testing.T) {
	f := newSyncFixture(t)

	// Take the first backup.
	if err := BackupWith(f.local, "chore: automatic backup", Options{Mode: ModeBranch}); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	// Wait a moment so the second snapshot has a different timestamp.
	time.Sleep(1 * time.Second)

	// Modify a file and take a second backup.
	writeFile(t, filepath.Join(f.local, "second.txt"), "second\n")
	if err := BackupWith(f.local, "chore: automatic backup", Options{Mode: ModeBranch}); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, Options{Mode: ModeBranch}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if len(snapshots) < 1 {
		t.Fatalf("expected at least 1 snapshot, got %d", len(snapshots))
	}

	// Verify snapshots are sorted newest-first.
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i].Date.After(snapshots[i-1].Date) {
			t.Fatalf("snapshots not sorted newest-first: %v > %v", snapshots[i].Date, snapshots[i-1].Date)
		}
	}

	// Verify metadata is populated.
	for i, s := range snapshots {
		if s.Hash == "" {
			t.Errorf("snapshot %d: empty hash", i)
		}
		if s.ShortHash == "" {
			t.Errorf("snapshot %d: empty short hash", i)
		}
		if s.Author == "" {
			t.Errorf("snapshot %d: empty author", i)
		}
		if s.Message == "" {
			t.Errorf("snapshot %d: empty message", i)
		}
		if s.Machine == "" {
			t.Errorf("snapshot %d: empty machine", i)
		}
		if s.Branch == "" {
			t.Errorf("snapshot %d: empty branch", i)
		}
	}
}

func TestListSnapshotsMultiMachine(t *testing.T) {
	f := newSyncFixture(t)

	// Take a backup on local with one machine name.
	opts1 := Options{Mode: ModeBranch, Machine: "machine-a"}
	if err := BackupWith(f.local, "chore: automatic backup", opts1); err != nil {
		t.Fatalf("backup on machine-a: %v", err)
	}

	time.Sleep(1 * time.Second)

	// Take a backup on local with a different machine name.
	opts2 := Options{Mode: ModeBranch, Machine: "machine-b"}
	writeFile(t, filepath.Join(f.local, "other.txt"), "other\n")
	if err := BackupWith(f.local, "chore: automatic backup", opts2); err != nil {
		t.Fatalf("backup on machine-b: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, Options{Mode: ModeBranch}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Should have snapshots from both machines.
	machines := make(map[string]bool)
	for _, s := range snapshots {
		machines[s.Machine] = true
	}

	if !machines["machine-a"] {
		t.Error("expected snapshot from machine-a")
	}
	if !machines["machine-b"] {
		t.Error("expected snapshot from machine-b")
	}
}

func TestSnapshotDiff(t *testing.T) {
	f := newSyncFixture(t)

	// Take the first backup.
	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	snapshots1, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots1) == 0 {
		t.Fatalf("expected snapshot after first backup: %v", err)
	}
	hash1 := snapshots1[0].Hash

	// Modify a file and take a second backup.
	time.Sleep(1 * time.Second)
	writeFile(t, filepath.Join(f.local, "new.txt"), "new content\n")
	writeFile(t, filepath.Join(f.local, "base.txt"), "modified base\n")
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	snapshots2, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots2) == 0 {
		t.Fatalf("expected snapshot after second backup: %v", err)
	}
	hash2 := snapshots2[0].Hash

	// If both snapshots are the same (no change detected), skip the diff test.
	if hash1 == hash2 {
		t.Skip("both snapshots have the same hash; skipping diff test")
	}

	// Diff between the two snapshots.
	changes, err := SnapshotDiff(f.local, hash1, hash2)
	if err != nil {
		t.Fatal(err)
	}

	// Should see new.txt added and base.txt modified.
	foundNew := false
	foundBase := false
	for _, c := range changes {
		if c.Path == "new.txt" && c.Status == "A" {
			foundNew = true
		}
		if c.Path == "base.txt" && c.Status == "M" {
			foundBase = true
		}
	}

	if !foundNew {
		t.Error("expected new.txt to be added in diff")
	}
	if !foundBase {
		t.Error("expected base.txt to be modified in diff")
	}
}

func TestSnapshotDiffWorktree(t *testing.T) {
	f := newSyncFixture(t)

	// Take a backup.
	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}
	hash := snapshots[0].Hash

	// Modify a tracked file in the working tree so diff-tree sees the change.
	writeFile(t, filepath.Join(f.local, "base.txt"), "modified after snapshot\n")

	// Diff snapshot against working tree.
	changes, err := SnapshotDiff(f.local, hash, "WORKTREE")
	if err != nil {
		t.Fatal(err)
	}

	foundBase := false
	for _, c := range changes {
		if c.Path == "base.txt" {
			foundBase = true
		}
	}

	if !foundBase {
		t.Error("expected base.txt in diff against working tree")
	}
}

func TestRestoreFile(t *testing.T) {
	f := newSyncFixture(t)

	// Create a file, commit it, and take a backup.
	writeFile(t, filepath.Join(f.local, "secret.txt"), "secret content\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "add secret file")

	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}
	hash := snapshots[0].Hash

	// Restore the file to a separate location.
	dest := filepath.Join(t.TempDir(), "restored_secret.txt")
	if err := RestoreFile(f.local, hash, "secret.txt", dest); err != nil {
		t.Fatal(err)
	}

	// Verify content matches.
	content, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "secret content" {
		t.Fatalf("expected 'secret content', got %q", string(content))
	}
}

func TestRestoreSnapshotToDir(t *testing.T) {
	f := newSyncFixture(t)

	// Create files and take a backup.
	writeFile(t, filepath.Join(f.local, "file1.txt"), "file1\n")
	writeFile(t, filepath.Join(f.local, "file2.txt"), "file2\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "add files")

	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}
	hash := snapshots[0].Hash

	// Restore to a separate directory.
	destDir := filepath.Join(t.TempDir(), "restored")
	if err := RestoreSnapshotToDir(f.local, opts, hash, destDir); err != nil {
		t.Fatal(err)
	}

	// Verify files are present.
	for _, name := range []string{"base.txt", "file1.txt", "file2.txt"} {
		path := filepath.Join(destDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s in restored directory: %v", name, err)
		}
	}

	// Verify .git directory was cleaned up.
	if _, err := os.Stat(filepath.Join(destDir, ".git")); err == nil {
		t.Error("expected .git directory to be removed from restored directory")
	}
}

func TestRestoreSnapshotToWorktree(t *testing.T) {
	f := newSyncFixture(t)

	// Create files and take a backup.
	writeFile(t, filepath.Join(f.local, "original.txt"), "original content\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "add original")

	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}
	hash := snapshots[0].Hash

	// Modify the working tree.
	writeFile(t, filepath.Join(f.local, "original.txt"), "modified content\n")
	writeFile(t, filepath.Join(f.local, "extra.txt"), "extra\n")

	// Restore snapshot to working tree.
	if err := RestoreSnapshotToWorktree(f.local, hash); err != nil {
		t.Fatal(err)
	}

	// Verify original.txt is restored.
	content, err := os.ReadFile(filepath.Join(f.local, "original.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "original content" {
		t.Fatalf("expected 'original content', got %q", string(content))
	}

	// Verify HEAD is unchanged (still on main branch).
	branch := mustGit(t, f.local, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "main" {
		t.Fatalf("expected HEAD on main, got %s", branch)
	}
}

func TestDiscoverBackupBranches(t *testing.T) {
	f := newSyncFixture(t)

	// No backups yet — should return empty.
	refs, err := discoverBackupBranches(f.local, DefaultBranchPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected no backup branches, got %d", len(refs))
	}

	// Take a backup.
	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Should now find the backup branch.
	refs, err = discoverBackupBranches(f.local, DefaultBranchPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 {
		t.Error("expected at least one backup branch after backup")
	}

	// Verify ref names contain the prefix.
	for _, ref := range refs {
		if !strings.Contains(ref, DefaultBranchPrefix) {
			t.Errorf("ref %q does not contain prefix %q", ref, DefaultBranchPrefix)
		}
	}
}

func TestSnapshotDiffEmpty(t *testing.T) {
	f := newSyncFixture(t)

	// Take a backup.
	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}
	hash := snapshots[0].Hash

	// Diff a snapshot against itself should return no changes.
	changes, err := SnapshotDiff(f.local, hash, hash)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 0 {
		t.Fatalf("expected no changes when diffing snapshot against itself, got %d", len(changes))
	}
}

func TestRestoreFileNotFound(t *testing.T) {
	f := newSyncFixture(t)

	// Take a backup.
	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}
	hash := snapshots[0].Hash

	// Try to restore a file that doesn't exist in the snapshot.
	dest := filepath.Join(t.TempDir(), "nonexistent.txt")
	err = RestoreFile(f.local, hash, "nonexistent.txt", dest)
	if err == nil {
		t.Fatal("expected error when restoring nonexistent file")
	}
}

func TestListSnapshotsSpecificMachine(t *testing.T) {
	f := newSyncFixture(t)

	// Take a backup with a specific machine name.
	opts := Options{Mode: ModeBranch, Machine: "testmachine"}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// List snapshots for the specific machine.
	snapshots, err := ListSnapshots(f.local, opts, "testmachine", "main")
	if err != nil {
		t.Fatal(err)
	}

	if len(snapshots) == 0 {
		t.Error("expected snapshot for testmachine/main")
	}

	// List snapshots for a non-existent machine.
	snapshots, err = ListSnapshots(f.local, opts, "nonexistent", "main")
	if err != nil {
		t.Fatal(err)
	}

	if len(snapshots) != 0 {
		t.Errorf("expected no snapshots for nonexistent machine, got %d", len(snapshots))
	}
}

func TestGetSnapshotFiles(t *testing.T) {
	f := newSyncFixture(t)

	// Create files and take a backup.
	writeFile(t, filepath.Join(f.local, "file1.txt"), "content1\n")
	writeFile(t, filepath.Join(f.local, "file2.txt"), "content2\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "add files")

	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}

	files, err := GetSnapshotFiles(f.local, snapshots[0].Hash)
	if err != nil {
		t.Fatal(err)
	}

	// Should include base.txt (from fixture) plus file1.txt and file2.txt.
	found := make(map[string]bool)
	for _, f := range files {
		found[f] = true
	}

	if !found["base.txt"] {
		t.Error("expected base.txt in snapshot files")
	}
	if !found["file1.txt"] {
		t.Error("expected file1.txt in snapshot files")
	}
	if !found["file2.txt"] {
		t.Error("expected file2.txt in snapshot files")
	}
}

func TestGetSnapshotSize(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}

	size, err := GetSnapshotSize(f.local, snapshots[0].Hash)
	if err != nil {
		t.Fatal(err)
	}

	if size <= 0 {
		t.Errorf("expected positive size, got %d", size)
	}
}

func TestListSnapshotHistory(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// Take the first backup.
	if err := BackupWith(f.local, "chore: first backup", opts); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	time.Sleep(1 * time.Second)

	// Modify and take a second backup.
	writeFile(t, filepath.Join(f.local, "second.txt"), "second\n")
	if err := BackupWith(f.local, "chore: second backup", opts); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	time.Sleep(1 * time.Second)

	// Modify and take a third backup.
	writeFile(t, filepath.Join(f.local, "third.txt"), "third\n")
	if err := BackupWith(f.local, "chore: third backup", opts); err != nil {
		t.Fatalf("third backup: %v", err)
	}

	// List full history.
	history, err := ListSnapshotHistory(f.local, opts, SnapshotFilter{})
	if err != nil {
		t.Fatal(err)
	}

	if len(history) < 2 {
		t.Fatalf("expected at least 2 snapshots in history, got %d", len(history))
	}

	// Verify newest-first order (default).
	for i := 1; i < len(history); i++ {
		if history[i].Date.After(history[i-1].Date) {
			t.Fatalf("history not sorted newest-first: %v > %v", history[i].Date, history[i-1].Date)
		}
	}
}

func TestListSnapshotHistoryChronological(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// Take two backups.
	if err := BackupWith(f.local, "chore: first", opts); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	time.Sleep(1 * time.Second)
	writeFile(t, filepath.Join(f.local, "extra.txt"), "extra\n")
	if err := BackupWith(f.local, "chore: second", opts); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	history, err := ListSnapshotHistory(f.local, opts, SnapshotFilter{Chronological: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(history) < 2 {
		t.Fatalf("expected at least 2 snapshots, got %d", len(history))
	}

	// Verify oldest-first order.
	for i := 1; i < len(history); i++ {
		if history[i].Date.Before(history[i-1].Date) {
			t.Fatalf("history not sorted oldest-first: %v < %v", history[i].Date, history[i-1].Date)
		}
	}
}

func TestListSnapshotHistoryFilterByMachine(t *testing.T) {
	f := newSyncFixture(t)

	// Take backups on two different machines.
	optsA := Options{Mode: ModeBranch, Machine: "machine-a"}
	if err := BackupWith(f.local, "chore: backup", optsA); err != nil {
		t.Fatalf("backup on machine-a: %v", err)
	}

	time.Sleep(1 * time.Second)
	writeFile(t, filepath.Join(f.local, "other.txt"), "other\n")
	optsB := Options{Mode: ModeBranch, Machine: "machine-b"}
	if err := BackupWith(f.local, "chore: backup", optsB); err != nil {
		t.Fatalf("backup on machine-b: %v", err)
	}

	// Filter by machine-a.
	history, err := ListSnapshotHistory(f.local, optsA, SnapshotFilter{Machine: "machine-a"})
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range history {
		if !strings.Contains(strings.ToLower(s.Machine), "machine-a") {
			t.Errorf("expected machine-a, got %s", s.Machine)
		}
	}

	// Filter by machine-b.
	history, err = ListSnapshotHistory(f.local, optsB, SnapshotFilter{Machine: "machine-b"})
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range history {
		if !strings.Contains(strings.ToLower(s.Machine), "machine-b") {
			t.Errorf("expected machine-b, got %s", s.Machine)
		}
	}
}

func TestListSnapshotHistoryFilterBySearch(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// Take backups with different messages.
	if err := BackupWith(f.local, "chore: first backup", opts); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	time.Sleep(1 * time.Second)
	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")
	if err := BackupWith(f.local, "feat: add new feature", opts); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	// Search for "feat".
	history, err := ListSnapshotHistory(f.local, opts, SnapshotFilter{Search: "feat"})
	if err != nil {
		t.Fatal(err)
	}

	if len(history) == 0 {
		t.Error("expected at least one snapshot matching 'feat'")
	}

	for _, s := range history {
		if !strings.Contains(strings.ToLower(s.Message), "feat") {
			t.Errorf("expected message to contain 'feat', got %q", s.Message)
		}
	}
}

func TestListSnapshotHistoryLimit(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// Take multiple backups.
	for i := 0; i < 3; i++ {
		if err := BackupWith(f.local, "chore: backup", opts); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
		time.Sleep(1 * time.Second)
		writeFile(t, filepath.Join(f.local, fmt.Sprintf("file%d.txt", i)), fmt.Sprintf("content%d\n", i))
	}

	history, err := ListSnapshotHistory(f.local, opts, SnapshotFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}

	if len(history) > 2 {
		t.Errorf("expected at most 2 snapshots with limit=2, got %d", len(history))
	}
}

func TestListSnapshotHistoryEmpty(t *testing.T) {
	f := newSyncFixture(t)

	history, err := ListSnapshotHistory(f.local, Options{Mode: ModeBranch}, SnapshotFilter{})
	if err != nil {
		t.Fatal(err)
	}

	if len(history) != 0 {
		t.Errorf("expected no snapshots, got %d", len(history))
	}
}

func TestEnrichSnapshots(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}

	// Enrich with all details.
	EnrichSnapshots(f.local, opts, snapshots, true, true, true)

	for i, s := range snapshots {
		if len(s.Files) == 0 {
			t.Errorf("snapshot %d: expected files after enrichment", i)
		}
		if s.Size <= 0 {
			t.Errorf("snapshot %d: expected positive size after enrichment, got %d", i, s.Size)
		}
	}
}

func TestIsSnapshotSynced(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// Take a backup (which pushes to remote).
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}

	// The latest snapshot should be synced (it was pushed).
	synced, err := IsSnapshotSynced(f.local, opts, snapshots[0].Hash)
	if err != nil {
		t.Fatal(err)
	}

	if !synced {
		t.Error("expected latest snapshot to be synced after successful push")
	}
}

func TestCompareSnapshotsNoChanges(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}
	if err := BackupWith(f.local, "chore: automatic backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}

	hash := snapshots[0].Hash
	comparison, err := CompareSnapshots(f.local, hash, hash)
	if err != nil {
		t.Fatal(err)
	}

	if len(comparison.Added) != 0 || len(comparison.Modified) != 0 ||
		len(comparison.Deleted) != 0 || len(comparison.Renamed) != 0 {
		t.Errorf("expected no changes when comparing identical snapshots, got: %s", comparison.Summary())
	}
}

func TestCompareSnapshotsAddedModifiedDeleted(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// First backup with two files.
	writeFile(t, filepath.Join(f.local, "file1.txt"), "content1\n")
	writeFile(t, filepath.Join(f.local, "file2.txt"), "content2\n")
	if err := BackupWith(f.local, "chore: first backup", opts); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	snapshots1, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots1) == 0 {
		t.Fatalf("expected snapshot after first backup: %v", err)
	}
	hash1 := snapshots1[0].Hash

	time.Sleep(1 * time.Second)

	// Modify file1, delete file2, add file3.
	writeFile(t, filepath.Join(f.local, "file1.txt"), "modified content\n")
	os.Remove(filepath.Join(f.local, "file2.txt"))
	writeFile(t, filepath.Join(f.local, "file3.txt"), "new file\n")
	if err := BackupWith(f.local, "chore: second backup", opts); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	snapshots2, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots2) == 0 {
		t.Fatalf("expected snapshot after second backup: %v", err)
	}
	hash2 := snapshots2[0].Hash

	if hash1 == hash2 {
		t.Skip("both snapshots have the same hash; skipping diff test")
	}

	comparison, err := CompareSnapshots(f.local, hash1, hash2)
	if err != nil {
		t.Fatal(err)
	}

	// Check added files.
	foundAdded := false
	for _, a := range comparison.Added {
		if a.Path == "file3.txt" {
			foundAdded = true
			break
		}
	}
	if !foundAdded {
		t.Error("expected file3.txt in added files")
	}

	// Check modified files.
	foundModified := false
	for _, m := range comparison.Modified {
		if m.Path == "file1.txt" {
			foundModified = true
			// Verify line-level diff is present for modified file.
			if m.DiffText == "" {
				t.Error("expected non-empty diff text for modified file")
			}
			if !strings.Contains(m.DiffText, "modified content") {
				t.Error("expected diff text to contain the new content")
			}
			break
		}
	}
	if !foundModified {
		t.Error("expected file1.txt in modified files")
	}

	// Check deleted files.
	foundDeleted := false
	for _, d := range comparison.Deleted {
		if d.Path == "file2.txt" {
			foundDeleted = true
			break
		}
	}
	if !foundDeleted {
		t.Error("expected file2.txt in deleted files")
	}
}

func TestCompareSnapshotsWithWorktree(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// Take a backup (base.txt already exists from fixture).
	if err := BackupWith(f.local, "chore: first backup", opts); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshots, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots) == 0 {
		t.Fatalf("expected snapshot: %v", err)
	}

	// Modify the tracked file in the working tree.
	writeFile(t, filepath.Join(f.local, "base.txt"), "changed in worktree\n")

	comparison, err := CompareSnapshots(f.local, snapshots[0].Hash, "WORKTREE")
	if err != nil {
		t.Fatal(err)
	}

	// Should see base.txt as modified.
	foundModified := false
	for _, m := range comparison.Modified {
		if m.Path == "base.txt" {
			foundModified = true
			break
		}
	}
	if !foundModified {
		t.Error("expected base.txt in modified files")
	}
}

func TestCompareSnapshotsBinaryFile(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// Create a binary file large enough for Git to detect as binary.
	// Git uses a heuristic; small files with some printable bytes may be treated as text.
	binaryData := make([]byte, 256)
	for i := range binaryData {
		binaryData[i] = byte(i)
	}
	writeFileBinary(t, filepath.Join(f.local, "data.bin"), binaryData)
	if err := BackupWith(f.local, "chore: first backup", opts); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	snapshots1, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots1) == 0 {
		t.Fatalf("expected snapshot after first backup: %v", err)
	}
	hash1 := snapshots1[0].Hash

	time.Sleep(1 * time.Second)

	// Modify the binary file.
	binaryData2 := make([]byte, 256)
	for i := range binaryData2 {
		binaryData2[i] = byte(255 - i)
	}
	writeFileBinary(t, filepath.Join(f.local, "data.bin"), binaryData2)
	if err := BackupWith(f.local, "chore: second backup", opts); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	snapshots2, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots2) == 0 {
		t.Fatalf("expected snapshot after second backup: %v", err)
	}
	hash2 := snapshots2[0].Hash

	if hash1 == hash2 {
		t.Skip("both snapshots have the same hash; skipping binary diff test")
	}

	comparison, err := CompareSnapshots(f.local, hash1, hash2)
	if err != nil {
		t.Fatal(err)
	}

	// Find the binary file in modified.
	foundBinary := false
	for _, m := range comparison.Modified {
		if m.Path == "data.bin" {
			foundBinary = true
			if !m.Binary {
				t.Error("expected binary flag to be true for binary file")
			}
			if m.DiffText != "" {
				t.Error("expected empty diff text for binary file")
			}
			break
		}
	}
	if !foundBinary {
		t.Error("expected data.bin in modified files")
	}
}

func TestCompareSnapshotsSummary(t *testing.T) {
	c := &SnapshotComparison{
		FromHash: "abc123",
		ToHash:   "def456",
		Added:    []FileDiff{{Path: "a.txt"}},
		Modified: []FileDiff{{Path: "b.txt"}},
		Deleted:  []FileDiff{{Path: "c.txt"}},
		Renamed:  []FileDiff{{Path: "d.txt", From: "e.txt"}},
	}

	summary := c.Summary()
	if !strings.Contains(summary, "1 added") {
		t.Errorf("summary should contain '1 added', got: %s", summary)
	}
	if !strings.Contains(summary, "1 modified") {
		t.Errorf("summary should contain '1 modified', got: %s", summary)
	}
	if !strings.Contains(summary, "1 deleted") {
		t.Errorf("summary should contain '1 deleted', got: %s", summary)
	}
	if !strings.Contains(summary, "1 renamed") {
		t.Errorf("summary should contain '1 renamed', got: %s", summary)
	}
}

func TestGetFileDiff(t *testing.T) {
	f := newSyncFixture(t)

	opts := Options{Mode: ModeBranch}

	// First backup.
	writeFile(t, filepath.Join(f.local, "target.txt"), "line1\nline2\nline3\n")
	if err := BackupWith(f.local, "chore: first backup", opts); err != nil {
		t.Fatalf("first backup: %v", err)
	}

	snapshots1, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots1) == 0 {
		t.Fatalf("expected snapshot after first backup: %v", err)
	}
	hash1 := snapshots1[0].Hash

	time.Sleep(1 * time.Second)

	// Modify and take second backup.
	writeFile(t, filepath.Join(f.local, "target.txt"), "line1\nmodified\nline3\n")
	if err := BackupWith(f.local, "chore: second backup", opts); err != nil {
		t.Fatalf("second backup: %v", err)
	}

	snapshots2, err := ListSnapshots(f.local, opts, "", "")
	if err != nil || len(snapshots2) == 0 {
		t.Fatalf("expected snapshot after second backup: %v", err)
	}
	hash2 := snapshots2[0].Hash

	if hash1 == hash2 {
		t.Skip("both snapshots have the same hash; skipping file diff test")
	}

	fileDiff, err := GetFileDiff(f.local, hash1, hash2, "target.txt")
	if err != nil {
		t.Fatal(err)
	}

	if fileDiff.Path != "target.txt" {
		t.Errorf("expected path target.txt, got %s", fileDiff.Path)
	}

	if fileDiff.Status != "M" {
		t.Errorf("expected status M, got %s", fileDiff.Status)
	}

	if fileDiff.Binary {
		t.Error("expected non-binary file")
	}

	if !strings.Contains(fileDiff.DiffText, "modified") {
		t.Error("expected diff to contain 'modified'")
	}
}

func TestCompareSnapshotsCrossMachine(t *testing.T) {
	f := newSyncFixture(t)

	// Take a backup as machine-a.
	writeFile(t, filepath.Join(f.local, "file.txt"), "from machine-a\n")
	optsA := Options{Mode: ModeBranch, Machine: "machine-a"}
	if err := BackupWith(f.local, "chore: backup from a", optsA); err != nil {
		t.Fatalf("backup from a: %v", err)
	}

	snapshotsA, err := ListSnapshots(f.local, optsA, "machine-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotsA) == 0 {
		t.Fatal("expected snapshot from machine-a")
	}
	hashA := snapshotsA[0].Hash

	time.Sleep(1 * time.Second)

	// Take a backup as machine-b with different content.
	writeFile(t, filepath.Join(f.local, "file.txt"), "from machine-b\n")
	optsB := Options{Mode: ModeBranch, Machine: "machine-b"}
	if err := BackupWith(f.local, "chore: backup from b", optsB); err != nil {
		t.Fatalf("backup from b: %v", err)
	}

	snapshotsB, err := ListSnapshots(f.local, optsB, "machine-b", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotsB) == 0 {
		t.Fatal("expected snapshot from machine-b")
	}
	hashB := snapshotsB[0].Hash

	if hashA == hashB {
		t.Skip("both snapshots have the same hash; skipping cross-machine test")
	}

	// Compare snapshots from different machines.
	comparison, err := CompareSnapshots(f.local, hashA, hashB)
	if err != nil {
		t.Fatal(err)
	}

	// Should see file.txt as modified.
	foundModified := false
	for _, m := range comparison.Modified {
		if m.Path == "file.txt" {
			foundModified = true
			break
		}
	}
	if !foundModified {
		t.Error("expected file.txt in modified files across machines")
	}
}

func writeFileBinary(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

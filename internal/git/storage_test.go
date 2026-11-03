package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupTestRepo initializes a Git repository with identity configured.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}

	if err := exec.Command("git", "-C", repoPath, "init").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repoPath, "config", "user.name", "Test").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repoPath, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatal(err)
	}

	return repoPath
}

// createFile creates a file with the given content in the repo.
func createFile(t *testing.T, repoPath, name string, content string) {
	t.Helper()
	path := filepath.Join(repoPath, name)
	dir := filepath.Dir(path)
	if dir != repoPath {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// commitAll stages and commits all changes in the repo.
func commitAll(t *testing.T, repoPath string, message string) {
	t.Helper()
	if err := exec.Command("git", "-C", repoPath, "add", ".").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repoPath, "commit", "-m", message).Run(); err != nil {
		t.Fatal(err)
	}
}

// createBackupBranch creates a backup branch with the given name and commits.
// The branchName should be the full ref path, e.g., "refs/autogit/machine1/branch1".
func createBackupBranch(t *testing.T, repoPath string, branchName string, numCommits int) {
	t.Helper()
	// Ensure there's at least one commit so the ref can be created.
	createFile(t, repoPath, "initial.txt", "initial content")
	commitAll(t, repoPath, "Initial commit")

	// Add commits to the main branch first.
	for i := 0; i < numCommits; i++ {
		fileName := fmt.Sprintf("backup_file_%d.txt", i)
		createFile(t, repoPath, fileName, strings.Repeat("x", 100+i*50))
		commitAll(t, repoPath, fmt.Sprintf("Backup commit %d", i))
	}

	// Create the backup ref pointing to the latest commit.
	if err := exec.Command("git", "-C", repoPath, "update-ref", branchName, "HEAD").Run(); err != nil {
		t.Fatal(err)
	}
}

func TestGetGitDirSize(t *testing.T) {
	repoPath := setupTestRepo(t)

	// Before any commits, .git should still have some size.
	size, err := GetGitDirSize(repoPath)
	if err != nil {
		t.Fatalf("GetGitDirSize failed: %v", err)
	}
	if size <= 0 {
		t.Errorf("expected positive .git size, got %d", size)
	}

	// Add a file and commit to increase .git size.
	createFile(t, repoPath, "test.txt", strings.Repeat("hello world\n", 100))
	commitAll(t, repoPath, "Add test file")

	size2, err := GetGitDirSize(repoPath)
	if err != nil {
		t.Fatalf("GetGitDirSize after commit failed: %v", err)
	}
	if size2 <= size {
		t.Errorf("expected .git size to increase after commit: before=%d, after=%d", size, size2)
	}
}

func TestGetGitDirSizeNonExistent(t *testing.T) {
	_, err := GetGitDirSize("/nonexistent/path")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestGetTopFilesInSnapshot(t *testing.T) {
	repoPath := setupTestRepo(t)

	// Create files with different sizes.
	createFile(t, repoPath, "small.txt", "a")
	createFile(t, repoPath, "medium.txt", strings.Repeat("b", 1000))
	createFile(t, repoPath, "large.txt", strings.Repeat("c", 10000))
	commitAll(t, repoPath, "Add files with different sizes")

	// Get the latest commit hash.
	output, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(output))

	// Get top 3 files.
	files, err := GetTopFilesInSnapshot(repoPath, hash, 3)
	if err != nil {
		t.Fatalf("GetTopFilesInSnapshot failed: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}

	// Verify sorted by size descending.
	if files[0].Path != "large.txt" {
		t.Errorf("expected first file to be large.txt, got %s", files[0].Path)
	}
	if files[1].Path != "medium.txt" {
		t.Errorf("expected second file to be medium.txt, got %s", files[1].Path)
	}
	if files[2].Path != "small.txt" {
		t.Errorf("expected third file to be small.txt, got %s", files[2].Path)
	}

	// Verify sizes are in descending order.
	for i := 1; i < len(files); i++ {
		if files[i].Size > files[i-1].Size {
			t.Errorf("files not sorted by size: %s (%d) > %s (%d)",
				files[i-1].Path, files[i-1].Size, files[i].Path, files[i].Size)
		}
	}
}

func TestGetTopFilesInSnapshotLimit(t *testing.T) {
	repoPath := setupTestRepo(t)

	// Create 5 files with different sizes.
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("file_%d.txt", i)
		content := strings.Repeat(string(rune('a'+i)), i*1000)
		createFile(t, repoPath, name, content)
	}
	commitAll(t, repoPath, "Add 5 files")

	output, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(output))

	// Request only top 2.
	files, err := GetTopFilesInSnapshot(repoPath, hash, 2)
	if err != nil {
		t.Fatalf("GetTopFilesInSnapshot failed: %v", err)
	}

	if len(files) != 2 {
		t.Errorf("expected 2 files with limit, got %d", len(files))
	}
}

func TestGetBackupGrowthRate(t *testing.T) {
	repoPath := setupTestRepo(t)

	// Create a backup branch with commits.
	prefix := DefaultBranchPrefix
	branchName := prefix + "/machine1/branch1"
	createBackupBranch(t, repoPath, "refs/"+branchName, 3)

	opts := Options{Prefix: prefix}
	rate, err := GetBackupGrowthRate(repoPath, opts)
	if err != nil {
		t.Fatalf("GetBackupGrowthRate failed: %v", err)
	}

	if rate.TotalCommits == 0 {
		t.Error("expected at least 1 commit in growth rate")
	}

	// All commits are in the same second, so commitsPerDay may be 0 or very high.
	// Just verify the dates are set.
	if rate.FirstBackup.IsZero() {
		t.Error("expected FirstBackup to be set")
	}
	if rate.LastBackup.IsZero() {
		t.Error("expected LastBackup to be set")
	}
}

func TestGetBackupGrowthRateNoBackups(t *testing.T) {
	repoPath := setupTestRepo(t)

	opts := Options{}
	rate, err := GetBackupGrowthRate(repoPath, opts)
	if err != nil {
		t.Fatalf("GetBackupGrowthRate failed: %v", err)
	}

	if rate.TotalCommits != 0 {
		t.Errorf("expected 0 commits, got %d", rate.TotalCommits)
	}
}

func TestGetBackupObjectSize(t *testing.T) {
	repoPath := setupTestRepo(t)

	// Create a backup branch with some content.
	prefix := DefaultBranchPrefix
	branchName := prefix + "/machine1/branch1"
	createBackupBranch(t, repoPath, "refs/"+branchName, 2)

	opts := Options{Prefix: prefix}
	size, err := GetBackupObjectSize(repoPath, opts)
	if err != nil {
		t.Fatalf("GetBackupObjectSize failed: %v", err)
	}

	if size <= 0 {
		t.Errorf("expected positive backup object size, got %d", size)
	}
}

func TestGetBackupObjectSizeNoBackups(t *testing.T) {
	repoPath := setupTestRepo(t)

	opts := Options{}
	size, err := GetBackupObjectSize(repoPath, opts)
	if err != nil {
		t.Fatalf("GetBackupObjectSize failed: %v", err)
	}

	if size != 0 {
		t.Errorf("expected 0 backup object size, got %d", size)
	}
}

func TestCheckStorage(t *testing.T) {
	repoPath := setupTestRepo(t)

	// Create a backup branch.
	prefix := DefaultBranchPrefix
	branchName := prefix + "/machine1/branch1"
	createBackupBranch(t, repoPath, "refs/"+branchName, 2)

	opts := Options{Prefix: prefix}
	report := CheckStorage(repoPath, opts, 5)

	if report.Path != repoPath {
		t.Errorf("expected path %s, got %s", repoPath, report.Path)
	}
	if report.GitDirSize <= 0 {
		t.Errorf("expected positive .git size, got %d", report.GitDirSize)
	}
	if report.BackupCommits == 0 {
		t.Error("expected at least 1 backup commit")
	}
	if report.Error != "" {
		t.Errorf("unexpected error: %s", report.Error)
	}
}

func TestCheckStorageNonExistent(t *testing.T) {
	opts := Options{}
	report := CheckStorage("/nonexistent/path", opts, 5)

	if report.Error == "" {
		t.Error("expected error for non-existent path")
	}
}

func TestCheckAllStorage(t *testing.T) {
	tempDir := t.TempDir()

	// Create two repos.
	repo1 := filepath.Join(tempDir, "repo1")
	repo2 := filepath.Join(tempDir, "repo2")
	for _, path := range []string{repo1, repo2} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		exec.Command("git", "-C", path, "init").Run()
		exec.Command("git", "-C", path, "config", "user.name", "Test").Run()
		exec.Command("git", "-C", path, "config", "user.email", "test@example.com").Run()
	}

	// Add a backup branch to repo1.
	prefix := DefaultBranchPrefix
	branchName := prefix + "/machine1/main"
	createBackupBranch(t, repo1, "refs/"+branchName, 1)

	opts := Options{Prefix: prefix}
	summary := CheckAllStorage([]string{repo1, repo2}, opts, 5)

	if len(summary.Reports) != 2 {
		t.Errorf("expected 2 reports, got %d", len(summary.Reports))
	}

	// Total .git size should be positive.
	if summary.TotalGitDirSize <= 0 {
		t.Errorf("expected positive total .git size, got %d", summary.TotalGitDirSize)
	}

	// repo1 should have backup commits, repo2 should not.
	for _, r := range summary.Reports {
		if strings.Contains(r.Path, "repo1") && r.BackupCommits == 0 {
			t.Error("expected repo1 to have backup commits")
		}
	}
}

func TestStorageSummaryRecommendations(t *testing.T) {
	// Test high commit count recommendation.
	reports := []StorageReport{
		{
			Path:          "/test/repo",
			BackupCommits: 150,
		},
	}
	recs := generateRecommendations(reports)
	if len(recs) == 0 {
		t.Error("expected recommendation for high commit count")
	}
	found := false
	for _, rec := range recs {
		if strings.Contains(rec, "backup commits") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected recommendation mentioning backup commits")
	}

	// Test large file recommendation.
	reports = []StorageReport{
		{
			Path: "/test/repo",
			TopFiles: []LargeFile{
				{Path: "data.bin", Size: 20 * 1024 * 1024}, // 20 MB
			},
		},
	}
	recs = generateRecommendations(reports)
	found = false
	for _, rec := range recs {
		if strings.Contains(rec, "large file") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected recommendation for large file")
	}

	// Test rapid growth recommendation.
	reports = []StorageReport{
		{
			Path: "/test/repo",
			GrowthRate: GrowthRate{
				CommitsPerDay: 10.0,
				TotalCommits:  100,
				FirstBackup:   time.Now().AddDate(0, 0, -10),
				LastBackup:    time.Now(),
			},
		},
	}
	recs = generateRecommendations(reports)
	found = false
	for _, rec := range recs {
		if strings.Contains(rec, "growing rapidly") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected recommendation for rapid growth")
	}

	// Test large .git directory recommendation.
	reports = []StorageReport{
		{
			Path:       "/test/repo",
			GitDirSize: 600 * 1024 * 1024, // 600 MB
		},
	}
	recs = generateRecommendations(reports)
	found = false
	for _, rec := range recs {
		if strings.Contains(rec, "large .git directory") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected recommendation for large .git directory")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1536, "1.5 KB"},
		{1572864, "1.5 MB"},
	}

	for _, tc := range tests {
		result := formatBytes(tc.input)
		if result != tc.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestCheckAllStorageEmpty(t *testing.T) {
	opts := Options{}
	summary := CheckAllStorage([]string{}, opts, 5)

	if len(summary.Reports) != 0 {
		t.Errorf("expected 0 reports, got %d", len(summary.Reports))
	}
	if summary.TotalGitDirSize != 0 {
		t.Errorf("expected 0 total .git size, got %d", summary.TotalGitDirSize)
	}
}

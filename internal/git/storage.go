package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LargeFile describes a file found in a snapshot, sorted by size.
type LargeFile struct {
	// Path is the relative path within the repository.
	Path string
	// Size is the file size in bytes.
	Size int64
}

// GrowthRate describes the rate of backup growth for a repository.
type GrowthRate struct {
	// CommitsPerDay is the average number of backup commits per day.
	CommitsPerDay float64
	// FirstBackup is the date of the oldest backup commit.
	FirstBackup time.Time
	// LastBackup is the date of the newest backup commit.
	LastBackup time.Time
	// TotalCommits is the total number of backup commits in the date range.
	TotalCommits int
}

// StorageReport holds the storage usage data for a single repository.
type StorageReport struct {
	// Path is the repository path.
	Path string
	// GitDirSize is the size of the .git directory in bytes.
	GitDirSize int64
	// BackupCommits is the number of backup branch commits.
	BackupCommits int
	// BackupObjectSize is the estimated object store size for backup refs in bytes.
	BackupObjectSize int64
	// LatestSnapshotSize is the tree size of the latest snapshot in bytes.
	LatestSnapshotSize int64
	// TopFiles lists the largest files in the latest snapshot.
	TopFiles []LargeFile
	// GrowthRate describes the backup growth rate.
	GrowthRate GrowthRate
	// Error is set if a measurement failed for this repository.
	Error string
}

// StorageSummary aggregates storage data across all repositories.
type StorageSummary struct {
	// Reports is the per-repository storage data.
	Reports []StorageReport
	// TotalGitDirSize is the sum of all .git directory sizes in bytes.
	TotalGitDirSize int64
	// TotalBackupCommits is the sum of all backup commit counts.
	TotalBackupCommits int
	// TotalBackupObjectSize is the sum of all backup object sizes in bytes.
	TotalBackupObjectSize int64
	// Recommendations lists suggested cleanup actions.
	Recommendations []string
}

// GetGitDirSize walks the .git directory and returns the total size in bytes.
// It uses os.Lstat to avoid following symlinks and skips non-regular files.
func GetGitDirSize(path string) (int64, error) {
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return 0, fmt.Errorf("reading .git directory: %w", err)
	}

	var total int64
	err := filepath.Walk(gitDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Use Lstat to avoid following symlinks.
		linfo, lerr := os.Lstat(p)
		if lerr != nil {
			return nil // skip files we can't stat
		}
		if !linfo.Mode().IsRegular() {
			return nil
		}
		total += linfo.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walking .git directory: %w", err)
	}

	return total, nil
}

// GetBackupObjectSize estimates the object store size used by backup refs.
// It resolves all backup refs, walks reachable objects via git rev-list --objects,
// and sums their sizes via git cat-file --batch-check.
func GetBackupObjectSize(path string, opts Options) (int64, error) {
	prefix := strings.Trim(opts.Prefix, "/")
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}

	refs, err := discoverBackupBranches(path, prefix)
	if err != nil {
		return 0, fmt.Errorf("discovering backup branches: %w", err)
	}

	if len(refs) == 0 {
		return 0, nil
	}

	// Collect unique commit hashes from all refs.
	hashSet := make(map[string]bool)
	for _, ref := range refs {
		hash, err := revParseCommit(path, ref)
		if err != nil {
			continue
		}
		if hash != "" {
			hashSet[hash] = true
		}
	}

	if len(hashSet) == 0 {
		return 0, nil
	}

	// For each unique commit, list all reachable objects.
	// Use rev-list --objects to get all objects reachable from each commit.
	objLines := make(map[string]bool) // sha -> true to deduplicate
	for commitHash := range hashSet {
		output, err := run(shortTimeout, path, "rev-list", "--objects", commitHash)
		if err != nil {
			continue // skip commits that time out
		}
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Format: "<sha> <path>" or just "<sha>" for trees/commits
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				objLines[parts[0]] = true
			}
		}
	}

	if len(objLines) == 0 {
		return 0, nil
	}

	// Sum object sizes using cat-file --batch-check.
	// Build input: one sha per line.
	shas := make([]string, 0, len(objLines))
	for sha := range objLines {
		shas = append(shas, sha)
	}
	input := strings.Join(shas, "\n")

	// Run cat-file --batch-check with stdin input.
	ctx, cancel := newContextTimeout(shortTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", path, "cat-file", "--batch-check")
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	cmd.WaitDelay = waitDelay
	cmd.Stdin = strings.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, fmt.Errorf("git cat-file %w after %s", errTimedOut, shortTimeout)
		}
		return 0, fmt.Errorf("git cat-file failed: %w: %s", err, stderr.String())
	}

	var total int64
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<sha> <type> <size>"
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			size, err := strconv.ParseInt(parts[2], 10, 64)
			if err == nil {
				total += size
			}
		}
	}

	return total, nil
}

// GetTopFilesInSnapshot returns the N largest files in a snapshot, sorted by size descending.
// It uses git ls-tree -r --long to list all files with their sizes.
func GetTopFilesInSnapshot(path, hash string, n int) ([]LargeFile, error) {
	output, err := run(shortTimeout, path, "ls-tree", "-r", "--long", hash)
	if err != nil {
		return nil, fmt.Errorf("listing tree for snapshot %s: %w", hash[:7], err)
	}

	var files []LargeFile
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Format: "100644 blob <hash>     <size>\t<path>"
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		meta := strings.Fields(parts[0])
		if len(meta) < 4 {
			continue
		}

		sizeStr := meta[len(meta)-1]
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			continue
		}

		filePath := parts[1]
		files = append(files, LargeFile{Path: filePath, Size: size})
	}

	// Sort by size descending.
	sort.Slice(files, func(i, j int) bool {
		return files[i].Size > files[j].Size
	})

	if n > 0 && len(files) > n {
		files = files[:n]
	}

	return files, nil
}

// GetBackupGrowthRate calculates the rate of backup growth for a repository.
// It returns commits per day based on the date range of backup commits.
func GetBackupGrowthRate(path string, opts Options) (GrowthRate, error) {
	prefix := strings.Trim(opts.Prefix, "/")
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}

	refs, err := discoverBackupBranches(path, prefix)
	if err != nil {
		return GrowthRate{}, fmt.Errorf("discovering backup branches: %w", err)
	}

	if len(refs) == 0 {
		return GrowthRate{}, nil
	}

	// Collect unique commit hashes.
	hashSet := make(map[string]bool)
	for _, ref := range refs {
		hash, err := revParseCommit(path, ref)
		if err != nil {
			continue
		}
		if hash != "" {
			hashSet[hash] = true
		}
	}

	if len(hashSet) == 0 {
		return GrowthRate{}, nil
	}

	// Get commit dates using log --format=%ai for all unique commits.
	// Use rev-list to get all commits, then log to get dates.
	var allCommits []string
	for commitHash := range hashSet {
		output, err := run(shortTimeout, path, "rev-list", commitHash)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				allCommits = append(allCommits, line)
			}
		}
	}

	if len(allCommits) == 0 {
		return GrowthRate{}, nil
	}

	// Deduplicate commits.
	commitSet := make(map[string]bool)
	for _, c := range allCommits {
		commitSet[c] = true
	}

	if len(commitSet) == 0 {
		return GrowthRate{}, nil
	}

	// Get dates for all unique commits.
	commitsList := make([]string, 0, len(commitSet))
	for c := range commitSet {
		commitsList = append(commitsList, c)
	}

	// Use log with multiple refs to get dates.
	args := []string{"log", "--format=%ai", "--topo-order"}
	args = append(args, commitsList...)
	output, err := run(shortTimeout, path, args...)
	if err != nil {
		return GrowthRate{}, fmt.Errorf("reading commit dates: %w", err)
	}

	var dates []time.Time
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		t, err := time.Parse("2006-01-02 15:04:05 -0700", line)
		if err == nil {
			dates = append(dates, t)
		}
	}

	if len(dates) == 0 {
		return GrowthRate{}, nil
	}

	// Find first and last dates.
	sort.Slice(dates, func(i, j int) bool {
		return dates[i].Before(dates[j])
	})
	first := dates[0]
	last := dates[len(dates)-1]

	totalCommits := len(dates)
	duration := last.Sub(first)
	commitsPerDay := 0.0
	if duration > 0 {
		commitsPerDay = float64(totalCommits) / duration.Hours() / 24
	}

	return GrowthRate{
		CommitsPerDay: commitsPerDay,
		FirstBackup:   first,
		LastBackup:    last,
		TotalCommits:  totalCommits,
	}, nil
}

// CheckStorage performs a full storage analysis for a single repository.
func CheckStorage(path string, opts Options, topFiles int) StorageReport {
	report := StorageReport{Path: path}

	// 1. .git directory size.
	gitDirSize, err := GetGitDirSize(path)
	if err != nil {
		report.Error = fmt.Sprintf("git dir size: %v", err)
		return report
	}
	report.GitDirSize = gitDirSize

	// 2. Backup commit count and object size.
	prefix := strings.Trim(opts.Prefix, "/")
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}
	refs, err := discoverBackupBranches(path, prefix)
	if err == nil {
		hashSet := make(map[string]bool)
		for _, ref := range refs {
			hash, err := revParseCommit(path, ref)
			if err != nil {
				continue
			}
			if hash != "" {
				hashSet[hash] = true
			}
		}
		report.BackupCommits = len(hashSet)
	}

	objSize, err := GetBackupObjectSize(path, opts)
	if err != nil {
		report.Error = fmt.Sprintf("backup object size: %v", err)
		return report
	}
	report.BackupObjectSize = objSize

	// 3. Latest snapshot size and top files.
	if len(refs) > 0 {
		// Find the latest commit across all refs.
		var latestHash string
		var latestDate time.Time
		for _, ref := range refs {
			hash, err := revParseCommit(path, ref)
			if err != nil {
				continue
			}
			dateOutput, err := run(shortTimeout, path, "log", "-1", "--format=%ai", hash)
			if err != nil {
				continue
			}
			date, err := time.Parse("2006-01-02 15:04:05 -0700", strings.TrimSpace(dateOutput))
			if err != nil {
				continue
			}
			if latestHash == "" || date.After(latestDate) {
				latestHash = hash
				latestDate = date
			}
		}

		if latestHash != "" {
			snapSize, err := GetSnapshotSize(path, latestHash)
			if err == nil {
				report.LatestSnapshotSize = snapSize
			}

			if topFiles > 0 {
				top, err := GetTopFilesInSnapshot(path, latestHash, topFiles)
				if err == nil {
					report.TopFiles = top
				}
			}
		}
	}

	// 4. Growth rate.
	growth, err := GetBackupGrowthRate(path, opts)
	if err == nil {
		report.GrowthRate = growth
	}

	return report
}

// CheckAllStorage aggregates storage data across all repositories and generates
// cleanup recommendations.
func CheckAllStorage(paths []string, opts Options, topFiles int) StorageSummary {
	summary := StorageSummary{}

	for _, path := range paths {
		report := CheckStorage(path, opts, topFiles)
		summary.Reports = append(summary.Reports, report)
		summary.TotalGitDirSize += report.GitDirSize
		summary.TotalBackupCommits += report.BackupCommits
		summary.TotalBackupObjectSize += report.BackupObjectSize
	}

	// Generate recommendations.
	summary.Recommendations = generateRecommendations(summary.Reports)

	return summary
}

// generateRecommendations analyzes storage reports and suggests cleanup actions.
func generateRecommendations(reports []StorageReport) []string {
	var recs []string

	for _, r := range reports {
		if r.Error != "" {
			continue
		}

		// High backup commit count — suggest tightening retention.
		if r.BackupCommits > 100 {
			recs = append(recs, fmt.Sprintf(
				"%s has %d backup commits — consider tightening retention policy to reduce storage usage.",
				r.Path, r.BackupCommits))
		}

		// Large files in snapshots — suggest adding to .gitignore or secrets guard.
		for _, f := range r.TopFiles {
			if f.Size > 10*1024*1024 { // 10 MB threshold
				recs = append(recs, fmt.Sprintf(
					"%s contains a large file (%s, %s) — consider adding it to .gitignore or the secrets guard.",
					r.Path, f.Path, formatBytes(f.Size)))
			}
		}

		// Rapidly growing history — suggest retention policies.
		if r.GrowthRate.CommitsPerDay > 5 {
			recs = append(recs, fmt.Sprintf(
				"%s is growing rapidly (%.1f commits/day) — consider adding a retention policy to limit backup history.",
				r.Path, r.GrowthRate.CommitsPerDay))
		}

		// Large .git directory — suggest git gc.
		if r.GitDirSize > 500*1024*1024 { // 500 MB threshold
			recs = append(recs, fmt.Sprintf(
				"%s has a large .git directory (%s) — consider running 'git gc' to optimize the object store.",
				r.Path, formatBytes(r.GitDirSize)))
		}
	}

	return recs
}

// formatBytes returns a human-readable byte size string (e.g., "1.5 MB").
func formatBytes(b int64) string {
	if b == 0 {
		return "0 B"
	}

	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}

	div := float64(b)
	exp := 0
	units := []string{"B", "KB", "MB", "GB", "TB"}
	for n := div; n >= float64(unit) && exp < len(units)-1; n = n / float64(unit) {
		div /= float64(unit)
		exp++
	}

	return fmt.Sprintf("%.1f %s", div, units[exp])
}

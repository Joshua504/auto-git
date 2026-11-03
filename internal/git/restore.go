package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SnapshotInfo holds metadata about a single backup snapshot.
type SnapshotInfo struct {
	Hash      string    // full commit hash
	ShortHash string    // first 7 characters of the hash
	Date      time.Time // author date
	Author    string    // "Name <email>"
	Machine   string    // extracted from backup branch name
	Branch    string    // the working branch that was backed up
	Message   string    // first line of commit message
	Files     []string  // list of files in the snapshot tree
	Size      int64     // estimated size of the snapshot in bytes
	Synced    bool      // whether the snapshot has been synchronized to the remote
}

// FileChange describes a file that differs between two snapshots.
type FileChange struct {
	Path string // relative path in the repository
	From string // old path (for renames); empty otherwise
	Status string // "M" (modified), "A" (added), "D" (deleted), "R" (renamed)
}

// FileDiff holds the detailed diff for a single file between two snapshots.
type FileDiff struct {
	Path     string // relative path in the repository
	From     string // old path (for renames); empty otherwise
	Status   string // "M" (modified), "A" (added), "D" (deleted), "R" (renamed)
	Binary   bool   // true when the file is binary and cannot be shown as text
	DiffText string // unified diff text; empty for binary files or deleted files with no old content
}

// SnapshotComparison holds the full comparison result between two snapshots.
type SnapshotComparison struct {
	FromHash string      // the "from" snapshot hash
	ToHash   string      // the "to" snapshot hash (or "WORKTREE")
	Added    []FileDiff  // files present in "to" but not in "from"
	Modified []FileDiff  // files changed between snapshots
	Deleted  []FileDiff  // files present in "from" but not in "to"
	Renamed  []FileDiff  // files renamed between snapshots
}

// Summary returns a human-readable summary of the comparison.
func (c *SnapshotComparison) Summary() string {
	return fmt.Sprintf(
		"%d added, %d modified, %d deleted, %d renamed",
		len(c.Added), len(c.Modified), len(c.Deleted), len(c.Renamed),
	)
}

// ListSnapshots returns all backup snapshots for a repository, sorted newest-first.
// If machine and branch are both non-empty, it lists only that specific backup
// branch. Otherwise it discovers all autogit/*/* backup branches.
func ListSnapshots(path string, opts Options, machine, branch string) ([]SnapshotInfo, error) {
	if !IsRepository(path) {
		return nil, fmt.Errorf("%s is not a Git repository", path)
	}

	// Fetch from remote to discover snapshots pushed from other machines.
	_ = Fetch(path)

	prefix := strings.Trim(opts.Prefix, "/")
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}

	var refs []string
	if machine != "" && branch != "" {
		// Target a specific backup branch.
		machineLabel := refLabel(strings.ToLower(machine), "machine")
		branchLabel := refLabel(branch, "branch")
		suffix := prefix + "/" + machineLabel + "/" + branchLabel
		refs = append(refs, "refs/"+suffix)
		refs = append(refs, "refs/remotes/origin/"+suffix)
	} else {
		// Discover all backup branches.
		var err error
		refs, err = discoverBackupBranches(path, prefix)
		if err != nil {
			return nil, err
		}
	}

	if len(refs) == 0 {
		return nil, nil
	}

	// Collect unique commit hashes from all refs.
	hashSet := make(map[string]bool)
	for _, ref := range refs {
		hash, err := revParseCommit(path, ref)
		if err != nil {
			continue // ref may not exist locally
		}
		if hash != "" {
			hashSet[hash] = true
		}
	}

	if len(hashSet) == 0 {
		return nil, nil
	}

	// Parse commit metadata for each unique hash.
	var snapshots []SnapshotInfo
	for hash := range hashSet {
		info, err := parseSnapshotInfo(path, hash, prefix)
		if err != nil {
			continue
		}
		snapshots = append(snapshots, info)
	}

	// Sort newest-first.
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Date.After(snapshots[j].Date)
	})

	return snapshots, nil
}

// SnapshotDiff returns the list of files that differ between two snapshots.
// If toHash is "WORKTREE", it compares fromHash against the current working tree.
func SnapshotDiff(path string, fromHash, toHash string) ([]FileChange, error) {
	if !IsRepository(path) {
		return nil, fmt.Errorf("%s is not a Git repository", path)
	}

	var output string
	var err error

	if toHash == "WORKTREE" {
		// Compare snapshot against the current working tree by staging everything
		// temporarily and comparing against the snapshot's tree.
		output, err = run(shortTimeout, path, "diff", "--name-status", fromHash, "--", ".")
	} else {
		// Compare two snapshots.
		output, err = run(shortTimeout, path, "diff", "--name-status", fromHash+".."+toHash)
	}

	if err != nil {
		return nil, fmt.Errorf("comparing snapshots: %w", err)
	}

	return parseDiffOutput(strings.TrimSpace(output)), nil
}

// CompareSnapshots performs a detailed comparison between two snapshots,
// returning file-level changes grouped by status and line-level unified diffs.
// If toHash is "WORKTREE", it compares fromHash against the current working tree.
// This can be used to compare snapshots from different machines or backup branches.
func CompareSnapshots(path string, fromHash, toHash string) (*SnapshotComparison, error) {
	if !IsRepository(path) {
		return nil, fmt.Errorf("%s is not a Git repository", path)
	}

	changes, err := SnapshotDiff(path, fromHash, toHash)
	if err != nil {
		return nil, err
	}

	result := &SnapshotComparison{
		FromHash: fromHash,
		ToHash:   toHash,
	}

	for _, ch := range changes {
		fileDiff := FileDiff{
			Path:   ch.Path,
			From:   ch.From,
			Status: ch.Status,
		}

		// Skip diff content for pure additions where there is no "from" side
		// and for deletions where there is no "to" side — we still want the
		// unified diff which shows the full file being added/removed.
		if ch.Status != "R" {
			// Get the unified diff for this file.
			diffArgs := []string{"diff", fromHash + ".." + toHash, "--", ch.Path}
			if toHash == "WORKTREE" {
				diffArgs = []string{"diff", fromHash, "--", ch.Path}
			}
			diffOutput, err := run(shortTimeout, path, diffArgs...)
			if err == nil {
				diffText := strings.TrimSpace(diffOutput)

				// Check whether the file is binary by examining the diff output.
				// Git diff outputs "Binary files a/X and b/X differ" for binary files.
				if strings.Contains(diffText, "Binary files") {
					fileDiff.Binary = true
				} else {
					fileDiff.DiffText = diffText
				}
			}
		} else {
			// For renames, diff against the new path.
			diffArgs := []string{"diff", fromHash + ".." + toHash, "--", ch.Path}
			diffOutput, err := run(shortTimeout, path, diffArgs...)
			if err == nil {
				diffText := strings.TrimSpace(diffOutput)

				if strings.Contains(diffText, "Binary files") {
					fileDiff.Binary = true
				} else {
					fileDiff.DiffText = diffText
				}
			}
		}

		switch ch.Status {
		case "A":
			result.Added = append(result.Added, fileDiff)
		case "M":
			result.Modified = append(result.Modified, fileDiff)
		case "D":
			result.Deleted = append(result.Deleted, fileDiff)
		case "R":
			result.Renamed = append(result.Renamed, fileDiff)
		}
	}

	return result, nil
}

// GetFileDiff returns the unified diff for a single file between two snapshots.
// If toHash is "WORKTREE", it compares fromHash against the current working tree.
func GetFileDiff(path string, fromHash, toHash, filePath string) (*FileDiff, error) {
	if !IsRepository(path) {
		return nil, fmt.Errorf("%s is not a Git repository", path)
	}

	var diffOutput string
	var err error

	if toHash == "WORKTREE" {
		diffOutput, err = run(shortTimeout, path, "diff", fromHash, "--", filePath)
	} else {
		diffOutput, err = run(shortTimeout, path, "diff", fromHash+".."+toHash, "--", filePath)
	}

	if err != nil {
		return nil, fmt.Errorf("diffing file %s: %w", filePath, err)
	}

	diffText := strings.TrimSpace(diffOutput)
	isBinary := strings.Contains(diffText, "Binary files")
	if isBinary {
		diffText = ""
	}

	// Determine the status of this file.
	changes, err := SnapshotDiff(path, fromHash, toHash)
	if err != nil {
		return nil, err
	}

	status := "M" // default
	var fromPath string
	for _, ch := range changes {
		if ch.Path == filePath || ch.From == filePath {
			status = ch.Status
			fromPath = ch.From
			break
		}
	}

	return &FileDiff{
		Path:     filePath,
		From:     fromPath,
		Status:   status,
		Binary:   isBinary,
		DiffText: diffText,
	}, nil
}

// RestoreFile extracts a single file from a snapshot commit to destPath.
func RestoreFile(path, snapshotHash, filePath, destPath string) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	// Ensure the parent directory of destPath exists.
	if dir := filepath.Dir(destPath); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating destination directory: %w", err)
		}
	}

	// Use git show to extract the file content.
	cmd := exec.Command("git", "-C", path, "show", snapshotHash+":"+filePath)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("extracting file %q from snapshot %s: %w", filePath, snapshotHash[:7], err)
	}

	if err := os.WriteFile(destPath, out, 0644); err != nil {
		return fmt.Errorf("writing restored file to %s: %w", destPath, err)
	}

	return nil
}

// RestoreSnapshotToDir restores the full tree of a snapshot to a separate directory.
func RestoreSnapshotToDir(path string, opts Options, snapshotHash, destDir string) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	// Create the destination directory.
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	// Initialize a temporary git environment in the destination to use read-tree.
	if _, err := run(shortTimeout, destDir, "init"); err != nil {
		return fmt.Errorf("initializing temporary repository: %w", err)
	}

	// Add the source repository as a remote so we can reference its objects.
	if _, err := run(shortTimeout, destDir, "remote", "add", "source", path); err != nil {
		return fmt.Errorf("adding source remote: %w", err)
	}

	// Fetch the specific commit from the source.
	if _, err := run(shortTimeout, destDir, "fetch", "source", snapshotHash); err != nil {
		return fmt.Errorf("fetching snapshot: %w", err)
	}

	// Checkout the tree.
	if _, err := run(shortTimeout, destDir, "checkout", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checking out snapshot tree: %w", err)
	}

	// Clean up git metadata in the destination — users just want the files.
	gitDir := filepath.Join(destDir, ".git")
	if err := os.RemoveAll(gitDir); err != nil {
		return fmt.Errorf("cleaning up temporary git data: %w", err)
	}

	return nil
}

// RestoreSnapshotToWorktree restores a snapshot into the current working tree
// without moving HEAD. It overwrites tracked files in place.
func RestoreSnapshotToWorktree(path, snapshotHash string) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	// Use git checkout to restore files from the snapshot without moving HEAD.
	if _, err := run(shortTimeout, path, "checkout", snapshotHash, "--", "."); err != nil {
		return fmt.Errorf("restoring snapshot to working tree: %w", err)
	}

	return nil
}

// discoverBackupBranches finds all autogit/*/* refs (local and remote tracking).
func discoverBackupBranches(path, prefix string) ([]string, error) {
	prefix = strings.Trim(prefix, "/")

	var refs []string

	// Find local backup refs. for-each-ref uses the argument as a prefix,
	// not a glob, so we pass "refs/autogit" and filter the results.
	localOutput, err := run(shortTimeout, path, "for-each-ref", "--format=%(refname)", "refs/"+prefix)
	if err == nil {
		for _, ref := range strings.Split(strings.TrimSpace(localOutput), "\n") {
			ref = strings.TrimSpace(ref)
			if ref != "" && strings.HasPrefix(ref, "refs/"+prefix+"/") {
				refs = append(refs, ref)
			}
		}
	}

	// Find remote tracking backup refs.
	remoteOutput, err := run(shortTimeout, path, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/"+prefix)
	if err == nil {
		for _, ref := range strings.Split(strings.TrimSpace(remoteOutput), "\n") {
			ref = strings.TrimSpace(ref)
			if ref != "" && strings.HasPrefix(ref, "refs/remotes/origin/"+prefix+"/") {
				refs = append(refs, ref)
			}
		}
	}

	return refs, nil
}

// parseSnapshotInfo extracts metadata from a single commit hash.
func parseSnapshotInfo(path, hash, prefix string) (SnapshotInfo, error) {
	info := SnapshotInfo{
		Hash:      hash,
		ShortHash: hash,
	}
	if len(hash) > 7 {
		info.ShortHash = hash[:7]
	}

	// Parse commit details using a custom format.
	output, err := run(shortTimeout, path, "log", "-1",
		"--format=%H%n%ai%n%an%n%ae%n%s", hash)
	if err != nil {
		return info, fmt.Errorf("reading commit %s: %w", hash, err)
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 5 {
		return info, fmt.Errorf("unexpected log output for %s", hash)
	}

	info.Hash = lines[0]
	if len(info.Hash) > 7 {
		info.ShortHash = info.Hash[:7]
	}

	// Parse author date.
	date, err := time.Parse("2006-01-02 15:04:05 -0700", lines[1])
	if err != nil {
		return info, fmt.Errorf("parsing date for %s: %w", hash, err)
	}
	info.Date = date

	info.Author = lines[2] + " <" + lines[3] + ">"
	info.Message = lines[4]

	// Determine which backup branch this commit belongs to by checking refs.
	// This is expensive but gives accurate machine/branch info.
	branchRefs, err := discoverBackupBranches(path, prefix)
	if err == nil {
		for _, ref := range branchRefs {
			refHash, err := revParseCommit(path, ref)
			if err != nil || refHash != hash {
				continue
			}
			// Extract machine and branch from the ref name.
			// ref format: refs/autogit/<machine>/<branch> or refs/remotes/origin/autogit/<machine>/<branch>
			name := ref
			if strings.HasPrefix(name, "refs/remotes/origin/") {
				name = strings.TrimPrefix(name, "refs/remotes/origin/")
			} else if strings.HasPrefix(name, "refs/") {
				name = strings.TrimPrefix(name, "refs/")
			}
			// name is now "autogit/<machine>/<branch>"
			parts := strings.SplitN(name, "/", 3)
			if len(parts) >= 3 {
				info.Machine = parts[1]
				info.Branch = parts[2]
				// Decode the refLabel encoding: "+" was "/" in the original.
				info.Branch = strings.ReplaceAll(info.Branch, "+", "/")
				break
			}
		}
	}

	return info, nil
}

// parseDiffOutput converts raw git diff --name-status output into FileChange structs.
func parseDiffOutput(output string) []FileChange {
	if output == "" {
		return nil
	}

	var changes []FileChange
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Format: "Status\tPath" or "Status\tOldPath\tNewPath" for renames.
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		status := parts[0]
		change := FileChange{
			Status: status,
		}

		if status == "R" && len(parts) >= 3 {
			change.From = parts[1]
			change.Path = parts[2]
		} else {
			change.Path = parts[1]
		}

		changes = append(changes, change)
	}

	return changes
}

// GetSnapshotFiles returns the list of files in a snapshot's tree.
func GetSnapshotFiles(path, hash string) ([]string, error) {
	if !IsRepository(path) {
		return nil, fmt.Errorf("%s is not a Git repository", path)
	}

	output, err := run(shortTimeout, path, "ls-tree", "-r", "--name-only", hash)
	if err != nil {
		return nil, fmt.Errorf("listing files in snapshot %s: %w", hash[:7], err)
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	var files []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}

	return files, nil
}

// GetSnapshotSize estimates the size of a snapshot's tree in bytes.
func GetSnapshotSize(path, hash string) (int64, error) {
	if !IsRepository(path) {
		return 0, fmt.Errorf("%s is not a Git repository", path)
	}

	output, err := run(shortTimeout, path, "ls-tree", "-r", "--long", hash)
	if err != nil {
		return 0, fmt.Errorf("calculating size for snapshot %s: %w", hash[:7], err)
	}

	var total int64
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Format: "100644 blob <hash>     <size>\t<path>"
		// The size is separated from the path by a tab, and from the hash by spaces.
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		// The first part contains "mode type hash    size" — extract the size
		// as the last space-separated token.
		meta := strings.Fields(parts[0])
		if len(meta) < 4 {
			continue
		}

		sizeStr := meta[len(meta)-1]
		var size int64
		_, err := fmt.Sscanf(sizeStr, "%d", &size)
		if err != nil {
			continue
		}

		total += size
	}

	return total, nil
}

// IsSnapshotSynced checks whether a snapshot commit exists on the remote tracking
// backup branch, meaning it has been successfully synchronized.
func IsSnapshotSynced(path string, opts Options, hash string) (bool, error) {
	if !IsRepository(path) {
		return false, fmt.Errorf("%s is not a Git repository", path)
	}

	prefix := strings.Trim(opts.Prefix, "/")
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}

	// Find all remote tracking backup refs.
	remoteOutput, err := run(shortTimeout, path, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/"+prefix)
	if err != nil {
		// No remote refs found — assume not synced.
		return false, nil
	}

	// For each remote tracking ref, check if the commit is reachable.
	for _, ref := range strings.Split(strings.TrimSpace(remoteOutput), "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" || !strings.HasPrefix(ref, "refs/remotes/origin/"+prefix+"/") {
			continue
		}

		// Check if the commit is an ancestor of (or is) the remote tip.
		_, err := run(shortTimeout, path, "merge-base", "--is-ancestor", hash, ref)
		if err == nil {
			return true, nil
		}
	}

	return false, nil
}

// SnapshotFilter holds the criteria for filtering snapshot history.
type SnapshotFilter struct {
	// Machine filters by machine name (case-insensitive partial match).
	Machine string
	// Branch filters by branch name (case-insensitive partial match).
	Branch string
	// Search filters by commit message (case-insensitive substring match).
	Search string
	// Limit caps the number of results. 0 means no limit.
	Limit int
	// Chronological returns results oldest-first instead of newest-first.
	Chronological bool
}

// ListSnapshotHistory returns the full history of backup snapshots for a
// repository, walking all commits on all backup branches. It applies the
// provided filter and returns results sorted by date.
func ListSnapshotHistory(path string, opts Options, filter SnapshotFilter) ([]SnapshotInfo, error) {
	if !IsRepository(path) {
		return nil, fmt.Errorf("%s is not a Git repository", path)
	}

	prefix := strings.Trim(opts.Prefix, "/")
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}

	// Discover all backup branches (local and remote).
	refs, err := discoverBackupBranches(path, prefix)
	if err != nil {
		return nil, err
	}

	if len(refs) == 0 {
		return nil, nil
	}

	// Build a map of ref name -> (machine, branch) for quick lookup.
	refInfo := make(map[string]struct{ machine, branch string })
	for _, ref := range refs {
		name := ref
		if strings.HasPrefix(name, "refs/remotes/origin/") {
			name = strings.TrimPrefix(name, "refs/remotes/origin/")
		} else if strings.HasPrefix(name, "refs/") {
			name = strings.TrimPrefix(name, "refs/")
		}
		// name is now "autogit/<machine>/<branch>"
		parts := strings.SplitN(name, "/", 3)
		if len(parts) >= 3 {
			machine := parts[1]
			branch := strings.ReplaceAll(parts[2], "+", "/")
			refInfo[ref] = struct{ machine, branch string }{machine: machine, branch: branch}
		}
	}

	// Collect all unique commit hashes from all backup branch histories.
	hashToRef := make(map[string]string) // hash -> ref it was found on
	hashToInfo := make(map[string]struct{ machine, branch string })

	for _, ref := range refs {
		// Walk the full history of this ref.
		output, err := run(shortTimeout, path, "log", "--format=%H", ref)
		if err != nil {
			continue // ref may not exist locally
		}

		info, ok := refInfo[ref]
		if !ok {
			continue
		}

		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			hash := strings.TrimSpace(line)
			if hash == "" {
				continue
			}
			// Only record the first ref we find for each hash.
			if _, exists := hashToRef[hash]; !exists {
				hashToRef[hash] = ref
				hashToInfo[hash] = info
			}
		}
	}

	if len(hashToRef) == 0 {
		return nil, nil
	}

	// Parse commit metadata for each unique hash.
	var snapshots []SnapshotInfo
	for hash := range hashToRef {
		info, err := parseSnapshotInfo(path, hash, prefix)
		if err != nil {
			continue
		}

		// Use the machine/branch from the ref we found the hash on.
		if mi, ok := hashToInfo[hash]; ok {
			info.Machine = mi.machine
			info.Branch = mi.branch
		}

		// Apply filters.
		if filter.Machine != "" && !strings.Contains(strings.ToLower(info.Machine), strings.ToLower(filter.Machine)) {
			continue
		}
		if filter.Branch != "" && !strings.Contains(strings.ToLower(info.Branch), strings.ToLower(filter.Branch)) {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(info.Message), strings.ToLower(filter.Search)) {
			continue
		}

		snapshots = append(snapshots, info)
	}

	// Sort by date.
	if filter.Chronological {
		sort.Slice(snapshots, func(i, j int) bool {
			return snapshots[i].Date.Before(snapshots[j].Date)
		})
	} else {
		sort.Slice(snapshots, func(i, j int) bool {
			return snapshots[i].Date.After(snapshots[j].Date)
		})
	}

	// Apply limit.
	if filter.Limit > 0 && len(snapshots) > filter.Limit {
		snapshots = snapshots[:filter.Limit]
	}

	return snapshots, nil
}

// EnrichSnapshots adds file lists, size estimates, and sync status to a slice
// of SnapshotInfo. It is a separate function so callers can control which
// enrichment steps are performed.
func EnrichSnapshots(path string, opts Options, snapshots []SnapshotInfo, withFiles, withSize, withSync bool) {
	for i := range snapshots {
		if withFiles {
			files, err := GetSnapshotFiles(path, snapshots[i].Hash)
			if err == nil {
				snapshots[i].Files = files
			}
		}
		if withSize {
			size, err := GetSnapshotSize(path, snapshots[i].Hash)
			if err == nil {
				snapshots[i].Size = size
			}
		}
		if withSync {
			synced, err := IsSnapshotSynced(path, opts, snapshots[i].Hash)
			if err == nil {
				snapshots[i].Synced = synced
			}
		}
	}
}

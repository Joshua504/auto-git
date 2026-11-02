package git

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultBranchPrefix is the namespace of backup branches unless configured otherwise.
const DefaultBranchPrefix = "autogit"

// reservedRefNamespaces are the first path segment of refs Git itself uses.
// A backup prefix there would mix snapshots in with real branches or tags.
var reservedRefNamespaces = map[string]bool{
	"heads": true, "tags": true, "remotes": true, "stash": true,
	"notes": true, "bisect": true, "replace": true,
}

var unsafeRefChars = regexp.MustCompile(`[^A-Za-z0-9_+-]+`)

// refLabel turns arbitrary text, such as a branch or host name, into one safe
// component of a Git ref name. Slashes become "+" so that "feature/login" and
// the branch "feature" can never collide in the ref hierarchy.
func refLabel(text, fallback string) string {
	label := unsafeRefChars.ReplaceAllString(strings.ReplaceAll(text, "/", "+"), "-")
	label = strings.Trim(label, "-")

	if label == "" {
		return fallback
	}

	return label
}

// backupNames are the refs used for one branch of one repository on one machine.
type backupNames struct {
	branch   string // label of the current branch, for example "main" or "detached"
	local    string // hidden local ref holding the newest snapshot: refs/autogit/<machine>/<branch>
	remote   string // branch on the remote: refs/heads/autogit/<machine>/<branch>
	tracking string // local copy of that remote branch: refs/remotes/origin/autogit/<machine>/<branch>
}

func newBackupNames(path string, opts Options) (backupNames, error) {
	prefix := strings.Trim(opts.Prefix, "/")
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}

	if reservedRefNamespaces[strings.Split(prefix, "/")[0]] {
		return backupNames{}, fmt.Errorf("backup prefix %q would mix snapshots with Git's own refs", prefix)
	}

	machine := opts.Machine
	if machine == "" {
		machine, _ = os.Hostname()
	}

	machine = refLabel(strings.ToLower(machine), "machine")

	branch, err := currentBranchLabel(path)
	if err != nil {
		return backupNames{}, err
	}

	suffix := prefix + "/" + machine + "/" + branch

	names := backupNames{
		branch:   branch,
		local:    "refs/" + suffix,
		remote:   "refs/heads/" + suffix,
		tracking: "refs/remotes/origin/" + suffix,
	}

	if _, err := run(shortTimeout, path, "check-ref-format", names.remote); err != nil {
		return backupNames{}, fmt.Errorf("%q is not a valid backup branch name; check backup.prefix and backup.machine", suffix)
	}

	return names, nil
}

// rawBranchName is the checked-out branch's name, or "detached" for a detached HEAD.
func rawBranchName(path string) (string, error) {
	output, err := run(shortTimeout, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		return strings.TrimSpace(output), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "detached", nil
	}

	return "", fmt.Errorf("checking the current branch: %w", err)
}

// currentBranchLabel is the branch name made safe for use inside a ref name.
// Branch mode backs up a detached HEAD too, under the name "detached".
func currentBranchLabel(path string) (string, error) {
	name, err := rawBranchName(path)
	if err != nil {
		return "", err
	}

	return refLabel(name, "branch"), nil
}

// backupBranch keeps a snapshot of the working tree on a backup branch.
//
// It never changes your branch, your index or your files. It copies your index
// to a scratch file, stages everything into the copy (what "git add -A" would
// do), turns the result into a commit, and pushes that commit to
// autogit/<machine>/<branch>. The snapshot's history includes your own commits,
// so unpushed work is protected too.
func backupBranch(path, message string, opts Options) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	names, err := newBackupNames(path, opts)
	if err != nil {
		return err
	}

	// Fetch first so the snapshot builds on what the remote already has. If the
	// network is down, still take the snapshot and report the failure afterwards:
	// nothing is lost while offline, and the next cycle pushes everything.
	fetchErr := Fetch(path)

	tree, blocked, err := snapshotTree(path)
	if err != nil {
		return err
	}

	reportBlocked(path, blocked)

	tip, err := snapshotCommit(path, names, tree, message, opts)
	if err != nil {
		return err
	}

	if tip == "" {
		// An empty repository: nothing to back up.
		return skipIfLocked(fetchErr)
	}

	if _, err := run(shortTimeout, path, "update-ref", names.local, tip); err != nil {
		return skipIfLocked(err)
	}

	if fetchErr != nil {
		return skipIfLocked(fetchErr)
	}

	remoteTip, err := revParseCommit(path, names.tracking)
	if err != nil {
		return err
	}

	if remoteTip == tip {
		// Nothing new to push, but still prune old snapshots.
		_, pruneErr := PruneBackupBranch(path, names, opts.Retention)
		return pruneErr
	}

	pushErr := retry("push backup branch", isRetryableGitError, func() error {
		_, err := run(longTimeout, path, "push", "origin", tip+":"+names.remote)

		return err
	})

	// After a successful push, prune old snapshots.
	if pushErr == nil {
		pruned, pruneErr := PruneBackupBranch(path, names, opts.Retention)
		if pruneErr != nil {
			log.Printf("pruning backup branch %s: %v", names.local, pruneErr)
		} else if pruned > 0 {
			log.Printf("pruned %d old snapshots from %s", pruned, names.local)
		}
	}

	return pushErr
}

// snapshotTree writes a tree of everything "git add -A" would stage, built in a
// scratch copy of the index so the real index is never touched, and returns it
// together with the files the secrets guard kept out.
func snapshotTree(path string) (string, []blockedFile, error) {
	output, err := run(shortTimeout, path, "rev-parse", "--git-path", "autogit-index", "--git-path", "index")
	if err != nil {
		return "", nil, fmt.Errorf("locating the index: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		return "", nil, fmt.Errorf("unexpected output locating the index: %q", output)
	}

	// Git runs in the repository directory, so a relative index path would
	// point somewhere else for it than for us.
	resolve := func(p string) (string, error) {
		if !filepath.IsAbs(p) {
			p = filepath.Join(path, p)
		}

		return filepath.Abs(p)
	}

	scratch, err := resolve(lines[0])
	if err != nil {
		return "", nil, err
	}

	realIndex, err := resolve(lines[1])
	if err != nil {
		return "", nil, err
	}

	// Remove leftovers of a run that was interrupted. Only AutoGit uses this
	// file, so a stale lock on it can never belong to anyone else.
	_ = os.Remove(scratch)
	_ = os.Remove(scratch + ".lock")

	defer func() { _ = os.Remove(scratch) }()

	env := []string{"GIT_INDEX_FILE=" + scratch}
	opts := runOptions{timeout: longTimeout, path: path, env: env}

	// Starting from a copy of the real index keeps Git's file-change cache, so
	// each cycle only has to read the files that actually changed.
	data, err := os.ReadFile(realIndex)

	switch {
	case err == nil:
		if err := os.WriteFile(scratch, data, 0o600); err != nil {
			return "", nil, fmt.Errorf("copying the index: %w", err)
		}
	case errors.Is(err, fs.ErrNotExist):
		head, headErr := revParseCommit(path, "HEAD")
		if headErr != nil {
			return "", nil, headErr
		}

		if head != "" {
			if _, err := runWith(opts, "read-tree", "HEAD"); err != nil {
				return "", nil, err
			}
		}
	default:
		return "", nil, fmt.Errorf("reading the index: %w", err)
	}

	if _, err := runWith(opts, "add", "-A", "."); err != nil {
		return "", nil, err
	}

	blocked, err := guardStaged(path, env)
	if err != nil {
		return "", nil, fmt.Errorf("checking files for secrets: %w", err)
	}

	tree, err := runWith(opts, "write-tree")
	if err != nil {
		return "", nil, err
	}

	return strings.TrimSpace(tree), blocked, nil
}

// snapshotCommit returns the commit that represents tree on the backup branch,
// creating it if needed. It returns "" when there is nothing to back up.
func snapshotCommit(path string, names backupNames, tree, message string, opts Options) (string, error) {
	head, err := revParseCommit(path, "HEAD")
	if err != nil {
		return "", err
	}

	localTip, err := revParseCommit(path, names.local)
	if err != nil {
		return "", err
	}

	remoteTip, err := revParseCommit(path, names.tracking)
	if err != nil {
		return "", err
	}

	// Build on the snapshots that already exist. Both can differ after a
	// reinstall or a second process; if neither contains the other, the new
	// snapshot has both as parents, which is always a fast-forward on the remote.
	parents, err := dropAncestors(path, uniqueNonEmpty(localTip, remoteTip))
	if err != nil {
		return "", err
	}

	// Make the user's own commits part of the history, so unpushed work is
	// safe on the remote even if it was never pushed to the real branch.
	if head != "" {
		covered := false

		for _, parent := range parents {
			if parent == head {
				covered = true

				break
			}

			isAncestorOfParent, err := isAncestor(path, head, parent)
			if err != nil {
				return "", err
			}

			if isAncestorOfParent {
				covered = true

				break
			}
		}

		if !covered {
			parents = append(parents, head)
		}
	}

	emptyTree, err := emptyTreeID(path)
	if err != nil {
		return "", err
	}

	// An empty repository has nothing worth a backup branch.
	if len(parents) == 0 && tree == emptyTree {
		return "", nil
	}

	// Nothing changed since the only parent: reuse it rather than make an empty commit.
	if len(parents) == 1 {
		parentTree, err := run(shortTimeout, path, "rev-parse", parents[0]+"^{tree}")
		if err != nil {
			return "", err
		}

		if strings.TrimSpace(parentTree) == tree {
			return parents[0], nil
		}
	}

	if err := checkIdentity(path); err != nil {
		return "", err
	}

	// Describe what changed since the snapshot this one follows, or since the
	// start of your history for the very first one.
	text := message

	if hasPlaceholders(message) {
		before := emptyTree

		if len(parents) > 0 {
			output, err := run(shortTimeout, path, "rev-parse", parents[0]+"^{tree}")
			if err != nil {
				return "", err
			}

			before = strings.TrimSpace(output)
		}

		changes, err := treeChanges(path, before, tree)
		if err != nil {
			return "", err
		}

		text, err = renderCommitMessage(path, message, opts, changes)
		if err != nil {
			return "", err
		}
	}

	args := []string{"commit-tree", tree}

	for _, parent := range parents {
		args = append(args, "-p", parent)
	}

	args = append(args, "-m", text, "-m", describeSnapshot(names.branch, head))

	commit, err := run(longTimeout, path, args...)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(commit), nil
}

func describeSnapshot(branch, head string) string {
	if head == "" {
		return fmt.Sprintf("AutoGit snapshot of the working tree on %s (no commits yet).", branch)
	}

	short := head
	if len(short) > 7 {
		short = short[:7]
	}

	return fmt.Sprintf("AutoGit snapshot of the working tree on %s, based on %s.", branch, short)
}

// revParseCommit resolves rev to a commit ID, or "" if it does not exist.
func revParseCommit(path, rev string) (string, error) {
	output, err := run(shortTimeout, path, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err == nil {
		return strings.TrimSpace(output), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil
	}

	return "", err
}

// isAncestor reports whether ancestor is reachable from descendant.
func isAncestor(path, ancestor, descendant string) (bool, error) {
	_, err := run(shortTimeout, path, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	return false, err
}

// dropAncestors removes every commit that another commit in the list contains.
func dropAncestors(path string, commits []string) ([]string, error) {
	var kept []string

	for i, commit := range commits {
		redundant := false

		for j, other := range commits {
			if i == j {
				continue
			}

			contained, err := isAncestor(path, commit, other)
			if err != nil {
				return nil, err
			}

			if contained {
				redundant = true

				break
			}
		}

		if !redundant {
			kept = append(kept, commit)
		}
	}

	return kept, nil
}

func uniqueNonEmpty(values ...string) []string {
	seen := map[string]bool{}

	var result []string

	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}

	return result
}

// commitInfo holds the hash and author date of a single commit.
type commitInfo struct {
	hash string
	date int64
}

// PruneBackupBranch removes old snapshots from a backup branch according to
// the retention policy. It rewrites the branch history so that only the most
// recent snapshots remain, then force-pushes the result to the remote.
//
// It returns the number of snapshots pruned.
func PruneBackupBranch(path string, names backupNames, retention RetentionCfg) (int, error) {
	if !retention.ShouldPrune() {
		return 0, nil
	}

	// Get the current tip of the local backup ref.
	localTip, err := revParseCommit(path, names.local)
	if err != nil {
		return 0, err
	}

	if localTip == "" {
		return 0, nil
	}

	// List all commits on the backup branch, newest first.
	// Format: commit hash and author date (unix timestamp), separated by tab.
	output, err := run(shortTimeout, path, "log", "--format=%H%x09%at", localTip)
	if err != nil {
		return 0, fmt.Errorf("listing backup branch commits: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return 0, nil
	}

	var commits []commitInfo
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			continue
		}

		var date int64
		_, err := fmt.Sscanf(parts[1], "%d", &date)
		if err != nil {
			continue
		}

		commits = append(commits, commitInfo{hash: parts[0], date: date})
	}

	if len(commits) == 0 {
		return 0, nil
	}

	// Determine which commits to keep.
	now := time.Now().Unix()
	var cutoff int64
	if retention.MaxAge > 0 {
		cutoff = now - (int64(retention.MaxAge) * 86400)
	}

	// Find the keep boundary: the first commit that should be removed.
	// Commits are ordered newest-first, so we keep from index 0 up to keepCount-1.
	keepCount := 0
	for i, c := range commits {
		ageOK := retention.MaxAge == 0 || c.date >= cutoff
		countOK := retention.MaxCommits == 0 || (i+1) <= retention.MaxCommits

		if ageOK && countOK {
			keepCount = i + 1
		} else {
			break
		}
	}

	// Always keep at least one commit so the branch isn't empty.
	if keepCount == 0 {
		keepCount = 1
	}

	pruned := len(commits) - keepCount
	if pruned <= 0 {
		return 0, nil
	}

	// The oldest commit we're keeping.
	oldestKept := commits[keepCount-1].hash

	// Build a set of pruned commit hashes.
	prunedHashes := make(map[string]bool)
	for i := keepCount; i < len(commits); i++ {
		prunedHashes[commits[i].hash] = true
	}

	// Get the parents of the oldest kept commit.
	parentsOutput, err := run(shortTimeout, path, "rev-list", "--parents", "-1", oldestKept)
	if err != nil {
		return 0, err
	}
	parentsLine := strings.Fields(parentsOutput)

	// If the oldest kept commit has no parents, it's already a root.
	if len(parentsLine) <= 1 {
		if _, err := run(shortTimeout, path, "update-ref", names.local, oldestKept); err != nil {
			return 0, err
		}
		if err := forcePushBackupBranch(path, names, oldestKept); err != nil {
			return 0, err
		}
		return pruned, nil
	}

	// Check whether any parent is pruned.
	hasPrunedParent := false
	for _, p := range parentsLine[1:] {
		if prunedHashes[p] {
			hasPrunedParent = true
			break
		}
	}

	if !hasPrunedParent {
		// All parents are non-pruned (e.g., they are user commits on the real branch).
		// Just move the ref to the oldest kept commit.
		if _, err := run(shortTimeout, path, "update-ref", names.local, oldestKept); err != nil {
			return 0, err
		}
		if err := forcePushBackupBranch(path, names, oldestKept); err != nil {
			return 0, err
		}
		return pruned, nil
	}

	// At least one parent is pruned. We need to recreate the oldest kept commit
	// without the pruned parents, then rebuild the chain of kept commits on top.
	return rebuildKeptChain(path, names, commits, keepCount, oldestKept, prunedHashes, pruned)
}

// rebuildKeptChain recreates the kept commits so they form a clean chain
// without pruned ancestors, then updates the ref and force-pushes.
func rebuildKeptChain(path string, names backupNames, commits []commitInfo, keepCount int, oldestKept string, prunedHashes map[string]bool, pruned int) (int, error) {
	// Map from original hash to new hash for rebuilt commits.
	replacementMap := make(map[string]string)

	// Rebuild from oldest kept to newest kept (index: keepCount-1 down to 0).
	for i := keepCount - 1; i >= 0; i-- {
		c := commits[i]

		// Get the tree of this commit.
		treeOutput, err := run(shortTimeout, path, "rev-parse", c.hash+"^{tree}")
		if err != nil {
			return 0, err
		}
		tree := strings.TrimSpace(treeOutput)

		// Get the commit message.
		msgOutput, err := run(shortTimeout, path, "log", "-1", "--format=%B", c.hash)
		if err != nil {
			return 0, err
		}

		// Get the author info.
		authorOutput, err := run(shortTimeout, path, "log", "-1", "--format=%an <%ae>%n%aI", c.hash)
		if err != nil {
			return 0, err
		}
		authorLines := strings.Split(strings.TrimSpace(authorOutput), "\n")
		if len(authorLines) < 2 {
			return 0, fmt.Errorf("parsing author info for %s", c.hash)
		}

		emailStart := strings.Index(authorLines[0], "<")
		emailEnd := strings.Index(authorLines[0], ">")
		if emailStart < 0 || emailEnd <= emailStart {
			return 0, fmt.Errorf("parsing author email for %s", c.hash)
		}

		env := []string{
			"GIT_AUTHOR_NAME=" + strings.TrimSpace(authorLines[0][:emailStart]),
			"GIT_AUTHOR_EMAIL=" + authorLines[0][emailStart+1:emailEnd],
			"GIT_AUTHOR_DATE=" + authorLines[1],
		}

		// Get the parents of this commit.
		parentsOutput, err := run(shortTimeout, path, "rev-list", "--parents", "-1", c.hash)
		if err != nil {
			return 0, err
		}
		parentsLine := strings.Fields(parentsOutput)

		var newParents []string
		for _, p := range parentsLine[1:] {
			// If this parent was rebuilt, use the replacement.
			if repl, ok := replacementMap[p]; ok {
				newParents = append(newParents, repl)
			} else if !prunedHashes[p] {
				// Non-pruned parent (e.g., a user commit on the real branch).
				newParents = append(newParents, p)
			}
			// Pruned parents are silently dropped.
		}

		args := []string{"commit-tree", tree}
		for _, p := range newParents {
			args = append(args, "-p", p)
		}
		args = append(args, "-m", strings.TrimSpace(msgOutput))

		newCommit, err := runWith(runOptions{timeout: longTimeout, path: path, env: env}, args...)
		if err != nil {
			return 0, err
		}
		replacementMap[c.hash] = strings.TrimSpace(newCommit)
	}

	// The tip of the new chain is the replacement for the original tip (commits[0]).
	tipReplacement := replacementMap[commits[0].hash]
	if tipReplacement == "" {
		return 0, fmt.Errorf("failed to rebuild tip commit")
	}

	if _, err := run(shortTimeout, path, "update-ref", names.local, tipReplacement); err != nil {
		return 0, err
	}

	if err := forcePushBackupBranch(path, names, tipReplacement); err != nil {
		return 0, err
	}

	return pruned, nil
}

// forcePushBackupBranch force-pushes the backup branch to the remote.
func forcePushBackupBranch(path string, names backupNames, newTip string) error {
	_, err := run(longTimeout, path, "push", "--force", "origin", newTip+":"+names.remote)
	return err
}

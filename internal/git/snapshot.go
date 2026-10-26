package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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

// currentBranchLabel names the checked-out branch, or "detached" for a
// detached HEAD. Branch mode backs up detached work too, under that name.
func currentBranchLabel(path string) (string, error) {
	output, err := run(shortTimeout, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		return refLabel(strings.TrimSpace(output), "branch"), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "detached", nil
	}

	return "", fmt.Errorf("checking the current branch: %w", err)
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

	tip, err := snapshotCommit(path, names, tree, message)
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
		return nil
	}

	return retry("push backup branch", isRetryableGitError, func() error {
		_, err := run(longTimeout, path, "push", "origin", tip+":"+names.remote)

		return err
	})
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
func snapshotCommit(path string, names backupNames, tree, message string) (string, error) {
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

	if len(parents) == 0 {
		// An empty repository has nothing worth a backup branch.
		emptyTree, err := runWith(runOptions{timeout: shortTimeout, path: path, stdin: []byte{}},
			"hash-object", "-t", "tree", "--stdin")
		if err != nil {
			return "", err
		}

		if tree == strings.TrimSpace(emptyTree) {
			return "", nil
		}
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

	args := []string{"commit-tree", tree}

	for _, parent := range parents {
		args = append(args, "-p", parent)
	}

	args = append(args, "-m", message, "-m", describeSnapshot(names.branch, head))

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

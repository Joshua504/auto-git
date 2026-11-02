package git

import (
	"fmt"
	"strings"
)

// OpenSyncOutcome says what SyncOnOpen did.
type OpenSyncOutcome string

const (
	// OpenSyncUpToDate means the upstream had nothing new.
	OpenSyncUpToDate OpenSyncOutcome = "up to date"

	// OpenSyncFastForwarded means new commits were brought in.
	OpenSyncFastForwarded OpenSyncOutcome = "fast-forwarded"

	// OpenSyncDiverged means the branch and its upstream each have commits the
	// other lacks. Nothing was changed.
	OpenSyncDiverged OpenSyncOutcome = "diverged"

	// OpenSyncBlocked means uncommitted changes touch files the new commits
	// change. Nothing was changed.
	OpenSyncBlocked OpenSyncOutcome = "blocked by local changes"
)

// OpenSyncResult describes the outcome of SyncOnOpen.
type OpenSyncResult struct {
	Outcome OpenSyncOutcome

	// Commits is how many commits the upstream had that the branch lacked.
	Commits int

	// Conflicts lists the files that blocked the pull, for OpenSyncBlocked.
	Conflicts []string
}

// SyncOnOpen brings new remote commits into the current branch. AutoGit runs it
// when an editor opens, so work starts from the latest code instead of piling
// changes onto a stale branch and running into conflicts or a rejected push.
//
// It only ever fast-forwards: no merge commit is created, no history is
// rewritten, and nothing is stashed. If a fast-forward would need more than
// that, because the branch has diverged or the incoming files overlap with your
// uncommitted edits, it changes nothing and reports why.
func SyncOnOpen(path string) (OpenSyncResult, error) {
	if !IsRepository(path) {
		return OpenSyncResult{}, fmt.Errorf("%s is not a Git repository", path)
	}

	lock, skipped := acquireLock(path)
	if skipped != nil {
		return OpenSyncResult{}, skipped
	}
	defer releaseLock(lock)

	// Never move a branch the user is in the middle of working on.
	reason, err := busyReason(path)
	if err != nil {
		return OpenSyncResult{}, err
	}

	if reason != "" {
		return OpenSyncResult{}, &SkippedError{Reason: reason}
	}

	if err := Fetch(path); err != nil {
		return OpenSyncResult{}, skipIfLocked(err)
	}

	behind, err := commitsBehindUpstream(path)
	if err != nil {
		return OpenSyncResult{}, err
	}

	if behind == 0 {
		return OpenSyncResult{Outcome: OpenSyncUpToDate}, nil
	}

	// A fast-forward is only possible if everything on the branch is already
	// in the upstream.
	canFastForward, err := isAncestor(path, "HEAD", "@{upstream}")
	if err != nil {
		return OpenSyncResult{}, err
	}

	if !canFastForward {
		return OpenSyncResult{Outcome: OpenSyncDiverged, Commits: behind}, nil
	}

	conflicts, err := overlappingChanges(path)
	if err != nil {
		return OpenSyncResult{}, err
	}

	if len(conflicts) > 0 {
		return OpenSyncResult{Outcome: OpenSyncBlocked, Commits: behind, Conflicts: conflicts}, nil
	}

	if _, err := run(longTimeout, path, "merge", "--ff-only", "@{upstream}"); err != nil {
		return OpenSyncResult{}, skipIfLocked(err)
	}

	return OpenSyncResult{Outcome: OpenSyncFastForwarded, Commits: behind}, nil
}

// overlappingChanges lists files that have uncommitted changes locally and are
// also changed by the incoming commits. Git would refuse to overwrite them, so
// AutoGit checks first and reports them by name.
func overlappingChanges(path string) ([]string, error) {
	local := map[string]bool{}

	// Modified and staged files, compared with the current commit.
	tracked, err := run(shortTimeout, path, "diff", "--name-only", "-z", "--no-renames", "HEAD")
	if err != nil {
		return nil, err
	}

	// New files Git has not been told to ignore. Incoming commits that add the
	// same path would overwrite them.
	untracked, err := run(shortTimeout, path, "ls-files", "--others", "--exclude-standard", "-z", "--full-name", "--", ":/")
	if err != nil {
		return nil, err
	}

	for _, list := range []string{tracked, untracked} {
		for _, name := range strings.Split(list, "\x00") {
			if name != "" {
				local[name] = true
			}
		}
	}

	incoming, err := run(shortTimeout, path, "diff", "--name-only", "-z", "--no-renames", "HEAD", "@{upstream}")
	if err != nil {
		return nil, err
	}

	var conflicts []string

	for _, name := range strings.Split(incoming, "\x00") {
		if name != "" && local[name] {
			conflicts = append(conflicts, name)
		}
	}

	return conflicts, nil
}

package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SkippedError reports that a backup was deliberately not attempted because
// the repository is busy or in a state where committing automatically would
// be wrong. It is not a failure: the next cycle simply tries again.
type SkippedError struct {
	Reason string
}

func (e *SkippedError) Error() string {
	return "backup skipped: " + e.Reason
}

// lockFileName is the file Git creates while it changes the index.
const lockFileName = "index.lock"

// operationMarkers are files Git keeps while a multi-step operation is
// unfinished. While one exists, the user is in the middle of something.
var operationMarkers = []struct {
	name   string
	reason string
}{
	{"rebase-merge", "a rebase is in progress"},
	{"rebase-apply", "a rebase or patch application is in progress"},
	{"MERGE_HEAD", "a merge is in progress"},
	{"CHERRY_PICK_HEAD", "a cherry-pick is in progress"},
	{"REVERT_HEAD", "a revert is in progress"},
	{"BISECT_LOG", "a bisect is in progress"},
}

// busyReason inspects the repository before AutoGit changes anything. It
// returns why a backup must not run right now, or "" when it is safe.
//
// Committing in these states would do real damage: `git add .` marks files with
// conflict markers as resolved, and commits made on a detached HEAD belong to
// no branch and can never be pushed.
func busyReason(repo string) (string, error) {
	args := []string{"rev-parse"}

	for _, marker := range operationMarkers {
		args = append(args, "--git-path", marker.name)
	}

	args = append(args, "--git-path", lockFileName)

	output, err := run(shortTimeout, repo, args...)
	if err != nil {
		return "", fmt.Errorf("locating git state files: %w", err)
	}

	paths := strings.Split(strings.TrimSpace(output), "\n")
	if len(paths) != len(operationMarkers)+1 {
		return "", fmt.Errorf("unexpected output locating git state files: %q", output)
	}

	// Git prints these paths relative to the directory it was run in.
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}

		return filepath.Join(repo, p)
	}

	for i, marker := range operationMarkers {
		if _, err := os.Stat(resolve(paths[i])); err == nil {
			return marker.reason, nil
		}
	}

	if info, err := os.Stat(resolve(paths[len(paths)-1])); err == nil {
		age := time.Since(info.ModTime()).Round(time.Second)

		return fmt.Sprintf(
			"another git process is using the repository (%s exists and is %s old; if no git command is running it is stale and can be deleted)",
			lockFileName, age), nil
	}

	// A conflict can leave unmerged files without any marker, for example
	// after "git stash pop". The pathspec ":/" covers the whole repository
	// even when AutoGit was pointed at a subdirectory.
	unmerged, err := run(shortTimeout, repo, "ls-files", "--unmerged", "--", ":/")
	if err != nil {
		return "", fmt.Errorf("checking for unresolved conflicts: %w", err)
	}

	if strings.TrimSpace(unmerged) != "" {
		return "the repository has unresolved merge conflicts", nil
	}

	// symbolic-ref exits with 1 when HEAD is not a branch.
	if _, err := run(shortTimeout, repo, "symbolic-ref", "--quiet", "HEAD"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "HEAD is detached (not on a branch)", nil
		}

		return "", fmt.Errorf("checking the current branch: %w", err)
	}

	return "", nil
}

// checkIdentity fails with an actionable message when Git has no author or
// committer identity, instead of letting `git commit` fail with a generic error.
func checkIdentity(repo string) error {
	for _, identity := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		if _, err := run(shortTimeout, repo, "var", identity); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return fmt.Errorf(
					"git cannot commit in %s because user.name and user.email are not set; "+
						`run: git config user.name "Your Name" and git config user.email "you@example.com"`,
					repo)
			}

			return fmt.Errorf("checking git identity: %w", err)
		}
	}

	return nil
}

var lockPathPattern = regexp.MustCompile(`Unable to create '([^']+\.lock)'`)

// skipIfLocked turns "another git process holds a lock" failures into a skip.
// busyReason catches locks that exist before AutoGit starts; this covers a
// lock taken by someone else between that check and AutoGit's own git call.
func skipIfLocked(err error) error {
	if err == nil {
		return nil
	}

	message := err.Error()

	if !strings.Contains(message, ".lock': File exists") &&
		!strings.Contains(message, "Another git process seems to be running") {
		return err
	}

	reason := "another git process is using the repository"

	if match := lockPathPattern.FindStringSubmatch(message); match != nil {
		reason += fmt.Sprintf(" (%s exists; if no git command is running it is stale and can be deleted)", match[1])
	}

	return &SkippedError{Reason: reason}
}

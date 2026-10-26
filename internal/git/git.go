package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// errTimedOut marks a command that was killed for exceeding its timeout.
// Timeouts are not retried: the timeout has already waited long enough, and
// the next scheduled backup cycle is the natural retry.
var errTimedOut = errors.New("timed out")

// These are variables rather than constants so tests can shorten them.
var (
	// shortTimeout bounds quick, local, read-only commands.
	shortTimeout = 30 * time.Second

	// longTimeout bounds commands that touch the network, run hooks, or
	// may take a while on large repositories (fetch, push, add, commit, rebase).
	longTimeout = 2 * time.Minute

	// waitDelay is how long to wait for a killed command's output pipes to
	// close. Git spawns helpers (ssh, git-remote-https) that can keep the pipes
	// open after git itself is killed; without this, a timeout could still hang.
	waitDelay = 5 * time.Second
)

// runOptions configures a single git invocation.
type runOptions struct {
	timeout time.Duration
	path    string
	stdin   []byte   // optional input for the command
	env     []string // extra KEY=VALUE entries added to the environment
}

// run executes git in the given repository path and returns its stdout.
// See runWith for the details.
func run(timeout time.Duration, path string, args ...string) (string, error) {
	return runWith(runOptions{timeout: timeout, path: path}, args...)
}

// runWith executes git in opts.path and returns its stdout.
//
// The command is killed if it exceeds opts.timeout, and interactive credential
// prompts are disabled so a missing credential fails instead of hanging.
// On failure the returned error contains git's own output and wraps the
// underlying error, so callers can still use errors.As with *exec.ExitError.
func runWith(opts runOptions, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", opts.path}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), opts.env...)
	cmd.WaitDelay = waitDelay

	if opts.stdin != nil {
		cmd.Stdin = bytes.NewReader(opts.stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.String(), fmt.Errorf("git %s %w after %s", args[0], errTimedOut, opts.timeout)
		}

		output := strings.TrimSpace(stderr.String() + stdout.String())

		return stdout.String(), fmt.Errorf("git %s failed: %w: %s", args[0], err, output)
	}

	return stdout.String(), nil
}

func IsRepository(path string) bool {
	_, err := run(shortTimeout, path, "rev-parse", "--is-inside-work-tree")

	return err == nil
}

func HasChanges(path string) (bool, error) {
	output, err := run(shortTimeout, path, "status", "--porcelain")
	if err != nil {
		return false, err
	}

	return len(output) > 0, nil
}

func Add(path string) error {
	_, err := run(longTimeout, path, "add", ".")

	return err
}

func Commit(path, message string) error {
	_, err := run(longTimeout, path, "commit", "-m", message)

	return err
}

func Push(path string) error {
	return retry("push to origin", isRetryableGitError, func() error {
		_, err := run(longTimeout, path, "push", "-u", "origin", "HEAD")

		return err
	})
}

func Fetch(path string) error {
	return retry("fetch from origin", isRetryableGitError, func() error {
		_, err := run(longTimeout, path, "fetch", "origin")

		return err
	})
}

// hasStagedChanges reports whether the index differs from HEAD.
func hasStagedChanges(path string) (bool, error) {
	_, err := run(shortTimeout, path, "diff", "--cached", "--quiet")
	if err == nil {
		return false, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}

	return false, err
}

func IsBehindRemote(path string) (bool, error) {
	// Identify the current branch.
	branchOutput, err := run(shortTimeout, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return false, fmt.Errorf("checking current branch failed: %w", err)
	}

	// The command's output ends with a newline. Left in place, it would end up
	// inside the config key below and make Git reject the key.
	branch := strings.TrimSpace(branchOutput)

	// Check whether this branch has an upstream configured.
	remoteOutput, err := run(shortTimeout, path, "config", "--get", "branch."+branch+".remote")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// No upstream is configured. Backup can attempt the initial push.
			return false, nil
		}

		return false, fmt.Errorf("checking branch remote failed: %w", err)
	}

	if strings.TrimSpace(remoteOutput) == "" {
		return false, nil
	}

	// An upstream is configured, so failure to resolve it must be reported.
	if _, err := run(shortTimeout, path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		return false, fmt.Errorf("resolving configured upstream failed: %w", err)
	}

	// Count commits present on the upstream but missing locally.
	countOutput, err := run(shortTimeout, path, "rev-list", "--count", "HEAD..@{upstream}")
	if err != nil {
		return false, fmt.Errorf("checking remote commits failed: %w", err)
	}

	count, err := strconv.Atoi(strings.TrimSpace(countOutput))
	if err != nil {
		return false, fmt.Errorf("parsing remote commit count %q: %w", countOutput, err)
	}

	return count > 0, nil
}

// Pull rebases local commits onto the upstream. It uses --autostash because
// files the guard kept out of the backup stay modified in the working tree,
// and Git refuses to rebase over uncommitted changes.
func Pull(path string) error {
	if _, err := run(longTimeout, path, "rebase", "--autostash", "@{upstream}"); err != nil {
		// A failed rebase may leave Git in an unfinished state.
		// Abort it so the repository returns to its pre-rebase state.
		if _, abortErr := run(shortTimeout, path, "rebase", "--abort"); abortErr != nil {
			return fmt.Errorf("%w; additionally, aborting the rebase failed: %v", err, abortErr)
		}

		return fmt.Errorf("rebase aborted and local changes restored: %w", err)
	}

	return nil
}

func Backup(path, message string) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	// Never touch a repository the user is in the middle of working on.
	reason, err := busyReason(path)
	if err != nil {
		return err
	}

	if reason != "" {
		return &SkippedError{Reason: reason}
	}

	// Save local changes before synchronizing with the remote.
	hasChanges, err := HasChanges(path)
	if err != nil {
		return fmt.Errorf("checking repository changes: %w", err)
	}

	if hasChanges {
		// Fail early with a clear message instead of after staging everything.
		if err := checkIdentity(path); err != nil {
			return err
		}

		if err := Add(path); err != nil {
			return skipIfLocked(err)
		}

		// Keep secrets, key files and oversized files out of the commit.
		blocked, err := guardStaged(path)
		if err != nil {
			return skipIfLocked(fmt.Errorf("checking staged files for secrets: %w", err))
		}

		reportBlocked(path, blocked)

		// Everything staged may have been blocked, leaving nothing to commit.
		staged, err := hasStagedChanges(path)
		if err != nil {
			return err
		}

		if staged {
			if err := Commit(path, message); err != nil {
				return skipIfLocked(err)
			}
		}
	} else {
		reportBlocked(path, nil)
	}

	// Get the latest remote history.
	if err := Fetch(path); err != nil {
		return skipIfLocked(err)
	}

	// Rebase local commits if the remote has commits we don't have.
	behind, err := IsBehindRemote(path)
	if err != nil {
		return err
	}

	if behind {
		if err := Pull(path); err != nil {
			return skipIfLocked(err)
		}
	}

	// Push only after synchronization succeeds.
	return Push(path)
}

package git

import (
	"errors"
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

const serviceTemplate = `[Unit]
Description=AutoGit - Automatic Git backup service
Documentation=https://github.com/Joshua504/auto-git
After=network-online.target
Wants=network-online.target


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

// IsBehindRemote reports whether the branch's upstream has commits the branch
// does not have.
func IsBehindRemote(path string) (bool, error) {
	count, err := commitsBehindUpstream(path)

	return count > 0, err
}

// commitsBehindUpstream counts the commits the upstream has that the current
// branch does not. A branch with no upstream has none.
func commitsBehindUpstream(path string) (int, error) {
	// Identify the current branch.
	branchOutput, err := run(shortTimeout, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return 0, fmt.Errorf("checking current branch failed: %w", err)
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
			return 0, nil
		}

		return 0, fmt.Errorf("checking branch remote failed: %w", err)
	}

	if strings.TrimSpace(remoteOutput) == "" {
		return 0, nil
	}

	// An upstream is configured, so failure to resolve it must be reported.
	if _, err := run(shortTimeout, path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		return 0, fmt.Errorf("resolving configured upstream failed: %w", err)
	}

	// Count commits present on the upstream but missing locally.
	countOutput, err := run(shortTimeout, path, "rev-list", "--count", "HEAD..@{upstream}")
	if err != nil {
		return 0, fmt.Errorf("checking remote commits failed: %w", err)
	}

	count, err := strconv.Atoi(strings.TrimSpace(countOutput))
	if err != nil {
		return 0, fmt.Errorf("parsing remote commit count %q: %w", countOutput, err)
	}

	return count, nil
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

// Backup modes, selected with Options.Mode.
const (
	// ModeCommit commits to the branch you are working on. This is the default.
	ModeCommit = "commit"

	// ModeBranch leaves your branch, index and working tree alone and keeps
	// snapshots of your work on a separate backup branch instead.
	ModeBranch = "branch"
)

// RetentionCfg controls how old backup snapshots are cleaned up.
type RetentionCfg struct {
	// MaxAge is the maximum age of backup snapshots, in days.
	// Snapshots older than this are pruned. Zero means no age limit.
	MaxAge int

	// MaxCommits is the maximum number of snapshots to keep per backup branch.
	// Oldest snapshots are removed when the count exceeds this limit.
	// Zero means no count limit.
	MaxCommits int
}

// ShouldPrune reports whether retention is configured.
func (r RetentionCfg) ShouldPrune() bool {
	return r.MaxAge > 0 || r.MaxCommits > 0
}

// Options chooses how a repository is backed up.
type Options struct {
	// Mode is ModeCommit (the default when empty) or ModeBranch.
	Mode string

	// Prefix is the namespace of backup branches in ModeBranch. The default is
	// "autogit", which gives branches like autogit/<machine>/<branch>.
	Prefix string

	// Machine names this computer in the backup branch name, so two computers
	// never write to the same branch. The default is the host name.
	Machine string

	// Retention controls how old backup branch snapshots are cleaned up.
	// Only applies when Mode is ModeBranch.
	Retention RetentionCfg
}

// Backup backs up a repository in the default mode, ModeCommit.
func Backup(path, message string) error {
	return BackupWith(path, message, Options{})
}

// BackupWith backs up a repository using the given options.
//
// It acquires an exclusive lock on the repository so that two AutoGit instances
// never operate on the same repository at the same time. If the lock is already
// held, the repository is skipped and will be retried on the next cycle.
func BackupWith(path, message string, opts Options) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	lock, skipped := acquireLock(path)
	if skipped != nil {
		return skipped
	}
	defer releaseLock(lock)

	switch opts.Mode {
	case "", ModeCommit:
		return backupCommit(path, message, opts)
	case ModeBranch:
		return backupBranch(path, message, opts)
	default:
		return fmt.Errorf("unknown backup mode %q (use %q or %q)", opts.Mode, ModeCommit, ModeBranch)
	}
}

// backupCommit commits changes on the current branch, brings in remote
// changes, and pushes.
func backupCommit(path, message string, opts Options) error {
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
		blocked, err := guardStaged(path, nil)
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
			// Describe only what is actually being committed: this runs after
			// the secrets guard, so files it kept out are never named.
			text := message

			if hasPlaceholders(message) {
				changes, err := stagedChanges(path, nil)
				if err != nil {
					return skipIfLocked(err)
				}

				if text, err = renderCommitMessage(path, message, opts, changes); err != nil {
					return err
				}
			}

			if err := Commit(path, text); err != nil {
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

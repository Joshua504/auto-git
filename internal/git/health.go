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

// HealthStatus represents the overall health of a repository.
type HealthStatus int

const (
	// Healthy means the repository is fully operational.
	Healthy HealthStatus = iota
	// Degraded means the repository has issues but may still be partially functional.
	Degraded
	// Unhealthy means the repository has critical issues and cannot be backed up.
	Unhealthy
)

func (s HealthStatus) String() string {
	switch s {
	case Healthy:
		return "healthy"
	case Degraded:
		return "degraded"
	case Unhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// HealthResult holds the health check result for a single repository.
type HealthResult struct {
	// Path is the repository path that was checked.
	Path string

	// Status is the overall health status.
	Status HealthStatus

	// Exists is true when the path exists on the filesystem.
	Exists bool

	// IsGitRepo is true when the path is a valid Git repository.
	IsGitRepo bool

	// HasRemote is true when the repository has at least one remote configured.
	HasRemote bool

	// RemoteName is the name of the primary remote (usually "origin").
	RemoteName string

	// RemoteURL is the URL of the primary remote.
	RemoteURL string

	// RemoteReachable is true when the remote could be contacted via fetch.
	RemoteReachable bool

	// AuthOK is true when authentication to the remote succeeded (or is not needed).
	AuthOK bool

	// Busy is true when a Git operation is in progress.
	Busy bool

	// BusyReason describes why the repository is busy, if applicable.
	BusyReason string

	// BehindCount is the number of commits the local branch is behind its upstream.
	BehindCount int

	// AheadCount is the number of commits the local branch is ahead of its upstream.
	AheadCount int

	// Branch is the current branch name.
	Branch string

	// Issues lists human-readable descriptions of problems found.
	Issues []string

	// Warnings lists non-critical observations.
	Warnings []string
}

// CheckHealth performs a comprehensive health check on a single repository.
// It evaluates repository existence, Git validity, remote configuration,
// remote reachability, authentication, operation state, and sync status.
func CheckHealth(path string) HealthResult {
	result := HealthResult{Path: path}

	// 1. Check if the path exists on the filesystem.
	if _, err := os.Stat(path); err != nil {
		result.Exists = false
		result.Status = Unhealthy
		result.Issues = append(result.Issues, fmt.Sprintf("path does not exist: %v", err))
		return result
	}
	result.Exists = true

	// 2. Check if it's a valid Git repository.
	if !IsRepository(path) {
		result.IsGitRepo = false
		result.Status = Unhealthy
		result.Issues = append(result.Issues, "not a valid Git repository")
		return result
	}
	result.IsGitRepo = true

	// 3. Check current branch.
	branchOutput, err := run(shortTimeout, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		result.Warnings = append(result.Warnings, "HEAD may be detached (not on a branch)")
	} else {
		result.Branch = strings.TrimSpace(branchOutput)
	}

	// 4. Check remote configuration.
	remoteName, remoteURL, hasRemote := getRemoteInfo(path)
	result.HasRemote = hasRemote
	result.RemoteName = remoteName
	result.RemoteURL = remoteURL

	if !hasRemote {
		result.Status = Degraded
		result.Issues = append(result.Issues, "no remote configured; cannot push backups")
		return result
	}

	// 5. Check remote reachability and authentication.
	reachable, authOK, authIssue := checkRemoteReachability(path, remoteName)
	result.RemoteReachable = reachable
	result.AuthOK = authOK

	if !reachable {
		result.Status = Degraded
		if authIssue != "" {
			result.Issues = append(result.Issues, authIssue)
		} else {
			result.Issues = append(result.Issues, "remote is unreachable")
		}
	} else if !authOK {
		result.Status = Degraded
		if authIssue != "" {
			result.Issues = append(result.Issues, authIssue)
		}
	}

	// 6. Check for unfinished Git operations.
	busy, busyReason := checkBusyState(path)
	result.Busy = busy
	result.BusyReason = busyReason

	if busy {
		result.Warnings = append(result.Warnings, fmt.Sprintf("repository is busy: %s", busyReason))
	}

	// 7. Check sync status (behind/ahead of remote).
	// Only meaningful if the remote is reachable. Fetch first so @{upstream}
	// reflects the latest remote state.
	if reachable {
		// Fetch the latest refs so we can compare against the current upstream.
		_ = Fetch(path)

		behind, err := commitsBehindUpstream(path)
		if err == nil {
			result.BehindCount = behind
			if behind > 0 {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%d commit(s) behind remote", behind))
			}
		}

		ahead, err := commitsAheadUpstream(path)
		if err == nil {
			result.AheadCount = ahead
			if ahead > 0 {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%d commit(s) ahead of remote", ahead))
			}
		}
	}

	// 8. Determine overall status.
	if result.Status == Degraded {
		// Already degraded from remote issues
	} else if len(result.Issues) > 0 {
		result.Status = Degraded
	} else {
		result.Status = Healthy
	}

	return result
}

// CheckAllHealth performs health checks on all given repository paths.
func CheckAllHealth(paths []string) []HealthResult {
	results := make([]HealthResult, 0, len(paths))
	for _, path := range paths {
		results = append(results, CheckHealth(path))
	}
	return results
}

// getRemoteInfo returns the name and URL of the primary remote.
func getRemoteInfo(path string) (name, url string, hasRemote bool) {
	// Try "origin" first, then fall back to the first configured remote.
	for _, candidate := range []string{"origin", ""} {
		var remoteName string

		if candidate == "" {
			// List all remotes and pick the first one.
			output, err := run(shortTimeout, path, "remote")
			if err != nil {
				return "", "", false
			}
			remotes := strings.Fields(strings.TrimSpace(output))
			if len(remotes) == 0 {
				return "", "", false
			}
			remoteName = remotes[0]
		} else {
			remoteName = candidate
		}

		urlOutput, err := run(shortTimeout, path, "remote", "get-url", remoteName)
		if err != nil {
			if candidate == "origin" {
				continue
			}
			return "", "", false
		}

		return remoteName, strings.TrimSpace(urlOutput), true
	}

	return "", "", false
}

// checkRemoteReachability tests whether the remote can be contacted.
// It returns whether the remote is reachable, whether authentication succeeded,
// and an optional issue description.
func checkRemoteReachability(path, remoteName string) (reachable, authOK bool, issue string) {
	// Use a shorter timeout for health checks to avoid blocking.
	healthCheckTimeout := 15 * time.Second

	ctx, cancel := newContextTimeout(healthCheckTimeout)
	defer cancel()

	// Run a lightweight ls-remote to test connectivity without fetching everything.
	output, err := runWithContext(ctx, path, "ls-remote", remoteName)

	if err != nil {
		errMsg := strings.ToLower(err.Error())

		// Check for authentication failures.
		if strings.Contains(errMsg, "authentication failed") ||
			strings.Contains(errMsg, "could not read username") ||
			strings.Contains(errMsg, "permission denied") ||
			strings.Contains(errMsg, "access denied") ||
			strings.Contains(errMsg, "403") ||
			strings.Contains(errMsg, "401") {
			return false, false, fmt.Sprintf("authentication failed: %v", err)
		}

		// Check for repository not found.
		if strings.Contains(errMsg, "repository not found") ||
			strings.Contains(errMsg, "404") ||
			strings.Contains(errMsg, "does not appear to be a git repository") {
			return false, true, fmt.Sprintf("remote repository not found: %v", err)
		}

		// Network issues.
		if strings.Contains(errMsg, "could not resolve") ||
			strings.Contains(errMsg, "connection timed out") ||
			strings.Contains(errMsg, "network is unreachable") ||
			strings.Contains(errMsg, "connection refused") ||
			strings.Contains(errMsg, "unable to access") {
			return false, true, fmt.Sprintf("network error: %v", err)
		}

		// Timeout.
		if strings.Contains(errMsg, "timed out") {
			return false, true, fmt.Sprintf("connection timed out after %s", healthCheckTimeout)
		}

		return false, true, fmt.Sprintf("remote check failed: %v", err)
	}

	// If ls-remote succeeded, the remote is reachable and auth works.
	_ = output
	return true, true, ""
}

// checkBusyState checks if the repository has an unfinished Git operation.
func checkBusyState(path string) (busy bool, reason string) {
	reason, err := busyReason(path)
	if err != nil {
		return false, ""
	}
	if reason != "" {
		return true, reason
	}
	return false, ""
}

// commitsAheadUpstream counts the commits the current branch has that the
// upstream does not. A branch with no upstream has none.
func commitsAheadUpstream(path string) (int, error) {
	branchOutput, err := run(shortTimeout, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return 0, fmt.Errorf("checking current branch: %w", err)
	}

	branch := strings.TrimSpace(branchOutput)

	remoteOutput, err := run(shortTimeout, path, "config", "--get", "branch."+branch+".remote")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return 0, nil
		}
		return 0, fmt.Errorf("checking branch remote: %w", err)
	}

	if strings.TrimSpace(remoteOutput) == "" {
		return 0, nil
	}

	if _, err := run(shortTimeout, path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		return 0, fmt.Errorf("resolving configured upstream: %w", err)
	}

	countOutput, err := run(shortTimeout, path, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return 0, fmt.Errorf("checking ahead commits: %w", err)
	}

	count, err := strconv.Atoi(strings.TrimSpace(countOutput))
	if err != nil {
		return 0, fmt.Errorf("parsing ahead commit count %q: %w", countOutput, err)
	}

	return count, nil
}

// newContextTimeout creates a context with a timeout for health check operations.
func newContextTimeout(timeout time.Duration) (ctx context.Context, cancel func()) {
	return context.WithTimeout(context.Background(), timeout)
}

// runWithContext executes git with a pre-configured context.
func runWithContext(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = waitDelay

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.String(), fmt.Errorf("git %s %w after context timeout", args[0], errTimedOut)
		}

		output := strings.TrimSpace(stderr.String() + stdout.String())
		return stdout.String(), fmt.Errorf("git %s failed: %w: %s", args[0], err, output)
	}

	return stdout.String(), nil
}

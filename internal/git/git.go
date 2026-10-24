package git

import (
	"fmt"
	"os/exec"
)

func IsRepository(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
	err := cmd.Run()

	return err == nil
}

func HasChanges(path string) (bool, error) {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}

	return len(output) > 0, nil
}

func Add(path string) error {
	cmd := exec.Command("git", "-C", path, "add", ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add failed: %w: %s", err, output)
	}

	return nil
}

func Commit(path, message string) error {
	cmd := exec.Command("git", "-C", path, "commit", "-m", message)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w: %s", err, output)
	}

	return nil
}

func Push(path string) error {
	return retry("git push", isRetryableGitError, func() error {
		cmd := exec.Command("git", "-C", path, "push", "-u", "origin", "HEAD")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git push failed: %w: %s", err, output)
		}

		return nil
	})
}

func Fetch(path string) error {
	return retry("git fetch", isRetryableGitError, func() error {
		cmd := exec.Command("git", "-C", path, "fetch", "origin")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git fetch failed: %w: %s", err, output)
		}

		return nil
	})
}

func IsBehindRemote(path string) (bool, error) {
	// Identify the current branch.
	branchCmd := exec.Command(
		"git", "-C", path,
		"symbolic-ref", "--quiet", "--short", "HEAD",
	)

	branchOutput, err := branchCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf(
			"checking current branch failed: %w: %s",
			err,
			branchOutput,
		)
	}

	branch := string(branchOutput)

	// Check whether this branch has an upstream configured.
	remoteCmd := exec.Command(
		"git", "-C", path,
		"config", "--get", "branch."+branch+".remote",
	)

	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// No upstream is configured. Backup can attempt the initial push.
			return false, nil
		}

		return false, fmt.Errorf("checking branch remote failed: %w", err)
	}

	if len(remoteOutput) == 0 {
		return false, nil
	}

	// An upstream is configured, so failure to resolve it must be reported.
	upstreamCmd := exec.Command(
		"git", "-C", path,
		"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}",
	)

	upstreamOutput, err := upstreamCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf(
			"resolving configured upstream failed: %w: %s",
			err,
			upstreamOutput,
		)
	}

	// Count commits present on the upstream but missing locally.
	cmd := exec.Command(
		"git", "-C", path,
		"rev-list", "--count", "HEAD..@{upstream}",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf(
			"checking remote commits failed: %w: %s",
			err,
			output,
		)
	}

	return string(output) != "0\n", nil
}

func Pull(path string) error {
	cmd := exec.Command("git", "-C", path, "rebase", "@{upstream}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// A failed rebase may leave Git in an unfinished state.
		// Abort it so the repository returns to its pre-rebase state.
		abortCmd := exec.Command("git", "-C", path, "rebase", "--abort")
		abortOutput, abortErr := abortCmd.CombinedOutput()

		if abortErr != nil {
			return fmt.Errorf(
				"git rebase failed: %w: %s; additionally, aborting the rebase failed: %v: %s",
				err,
				output,
				abortErr,
				abortOutput,
			)
		}

		return fmt.Errorf("git rebase failed; rebase aborted and local changes restored: %w: %s", err, output)
	}

	return nil
}

func Backup(path, message string) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	// Save local changes before synchronizing with the remote.
	hasChanges, err := HasChanges(path)
	if err != nil {
		return fmt.Errorf("checking repository changes: %w", err)
	}

	if hasChanges {
		if err := Add(path); err != nil {
			return err
		}

		if err := Commit(path, message); err != nil {
			return err
		}
	}

	// Get the latest remote history.
	if err := Fetch(path); err != nil {
		return err
	}

	// Rebase local commits if the remote has commits we don't have.
	behind, err := IsBehindRemote(path)
	if err != nil {
		return err
	}

	if behind {
		if err := Pull(path); err != nil {
			return err
		}
	}

	// Push only after synchronization succeeds.
	if err := Push(path); err != nil {
		return err
	}

	return nil
}

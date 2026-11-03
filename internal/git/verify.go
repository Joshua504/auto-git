package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// VerifyResult holds the outcome of verifying a backup for one repository.
type VerifyResult struct {
	// Path is the repository path that was verified.
	Path string

	// OK is true when all checks passed.
	OK bool

	// RemoteReachable is true when the remote could be contacted.
	RemoteReachable bool

	// RefExists is true when the expected backup reference exists on the remote.
	RefExists bool

	// HashMatches is true when the remote commit hash matches the expected one.
	HashMatches bool

	// Fetchable is true when the snapshot can be fetched.
	Fetchable bool

	// Issues lists human-readable descriptions of any problems found.
	Issues []string
}

// HasIssues reports whether any verification issues were found.
func (r VerifyResult) HasIssues() bool {
	return len(r.Issues) > 0
}

// VerifyBackup verifies that a backup for the given repository was successfully
// uploaded and is recoverable. It performs the following checks:
//
//  1. Fetches the latest refs from the remote.
//  2. Verifies the expected backup reference exists on the remote.
//  3. Compares the expected commit hash with the remote backup commit.
//  4. Verifies the snapshot can be fetched (tree objects are accessible).
//  5. Detects missing or corrupted backup references.
//
// The expectedHash parameter is optional: when empty, only the reference
// existence and fetchability are checked.
func VerifyBackup(path string, opts Options, expectedHash string) VerifyResult {
	result := VerifyResult{Path: path}

	// Step 1: Fetch the latest refs from the remote.
	if err := Fetch(path); err != nil {
		result.RemoteReachable = false
		result.Issues = append(result.Issues, fmt.Sprintf("cannot reach remote: %v", err))
		return result
	}
	result.RemoteReachable = true

	// Step 2: Determine which ref to check based on backup mode.
	ref, err := resolveBackupRef(path, opts)
	if err != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("cannot resolve backup reference: %v", err))
		return result
	}

	// Step 3: Check if the remote backup reference exists.
	remoteCommit, err := revParseCommit(path, ref)
	if err != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("checking remote reference failed: %v", err))
		return result
	}

	if remoteCommit == "" {
		result.RefExists = false
		result.Issues = append(result.Issues, "backup reference does not exist on the remote")
		return result
	}
	result.RefExists = true

	// Step 4: Compare commit hash if an expected hash was provided.
	hashMismatch := false
	if expectedHash != "" {
		if remoteCommit == expectedHash {
			result.HashMatches = true
		} else {
			result.HashMatches = false
			hashMismatch = true
			result.Issues = append(result.Issues,
				fmt.Sprintf("commit hash mismatch: expected %s, got %s", expectedHash, remoteCommit))
		}
	} else {
		// No expected hash to compare; consider it a match.
		result.HashMatches = true
	}

	// Step 5: Verify the snapshot can be fetched (tree objects are accessible).
	if err := verifyFetchable(path, remoteCommit); err != nil {
		result.Fetchable = false
		result.Issues = append(result.Issues, fmt.Sprintf("snapshot cannot be fetched: %v", err))
		return result
	}
	result.Fetchable = true

	if !hashMismatch {
		result.OK = true
	}

	return result
}

// VerifyAllBackups verifies all configured repositories and returns a slice of
// results, one per repository.
func VerifyAllBackups(paths []string, opts Options) []VerifyResult {
	results := make([]VerifyResult, 0, len(paths))
	for _, path := range paths {
		results = append(results, VerifyBackup(path, opts, ""))
	}
	return results
}

// resolveBackupRef returns the remote tracking ref to check, depending on the
// backup mode. In commit mode it resolves to the current branch's upstream.
// In branch mode it resolves to the backup branch's remote tracking ref.
func resolveBackupRef(path string, opts Options) (string, error) {
	switch opts.Mode {
	case "", ModeCommit:
		// In commit mode, check the current branch's upstream.
		branchOutput, err := run(shortTimeout, path, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil {
			return "", fmt.Errorf("checking current branch: %w", err)
		}
		branch := strings.TrimSpace(branchOutput)

		remoteOutput, err := run(shortTimeout, path, "config", "--get", "branch."+branch+".remote")
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
				return "", fmt.Errorf("branch %q has no remote configured", branch)
			}
			return "", fmt.Errorf("checking branch remote: %w", err)
		}
		remote := strings.TrimSpace(remoteOutput)
		if remote == "" {
			return "", fmt.Errorf("branch %q has no remote configured", branch)
		}

		// The remote tracking ref for the current branch.
		return "refs/remotes/" + remote + "/" + branch, nil

	case ModeBranch:
		names, err := newBackupNames(path, opts)
		if err != nil {
			return "", err
		}
		return names.tracking, nil

	default:
		return "", fmt.Errorf("unknown backup mode %q", opts.Mode)
	}
}

// verifyFetchable checks that the commit's tree objects are accessible locally
// after a fetch. It verifies the commit, its tree, and a sample of the tree's
// contents.
func verifyFetchable(path, commit string) error {
	// Verify the commit object exists and is valid.
	if _, err := run(shortTimeout, path, "cat-file", "-t", commit); err != nil {
		return fmt.Errorf("commit object %s is missing or corrupted", commit[:7])
	}

	// Verify the tree object is accessible.
	tree, err := run(shortTimeout, path, "rev-parse", commit+"^{tree}")
	if err != nil {
		return fmt.Errorf("cannot resolve tree for commit %s: %w", commit[:7], err)
	}
	tree = strings.TrimSpace(tree)

	if _, err := run(shortTimeout, path, "cat-file", "-t", tree); err != nil {
		return fmt.Errorf("tree object %s is missing or corrupted", tree[:7])
	}

	// Verify a few entries in the tree are accessible (spot check).
	lsTree, err := run(shortTimeout, path, "ls-tree", "-r", "--name-only", tree)
	if err != nil {
		return fmt.Errorf("cannot list tree contents: %w", err)
	}

	entries := strings.Split(strings.TrimSpace(lsTree), "\n")
	if len(entries) == 0 || (len(entries) == 1 && entries[0] == "") {
		// Empty tree is valid (empty repository).
		return nil
	}

	// Spot-check up to 3 random entries to ensure blob objects are accessible.
	checkCount := 3
	if len(entries) < checkCount {
		checkCount = len(entries)
	}

	for i := 0; i < checkCount; i++ {
		entry := strings.TrimSpace(entries[i])
		if entry == "" {
			continue
		}
		if _, err := run(shortTimeout, path, "cat-file", "-e", commit+":"+entry); err != nil {
			return fmt.Errorf("blob for %q is missing or corrupted", entry)
		}
	}

	return nil
}

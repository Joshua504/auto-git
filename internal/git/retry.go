package git

import (
	"fmt"
	"strings"
	"time"
)

const (
	maxRetryAttempts = 3
	retryDelay       = 2 * time.Second
)

func retry(operation string, shouldRetry func(error) bool, action func() error) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		lastErr = action()
		if lastErr == nil {
			return nil
		}

		if attempt == maxRetryAttempts || !shouldRetry(lastErr) {
			break
		}

		time.Sleep(retryDelay)
	}

	return fmt.Errorf("%s failed: %w", operation, lastErr)
}

func isRetryableGitError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())

	// Permanent failures should not be retried.
	permanentErrors := []string{
		"authentication failed",
		"permission denied",
		"could not read username",
		"repository not found",
		"does not appear to be a git repository",
		"no configured push destination",
		"no such remote",
		"non-fast-forward",
		"rejected",
	}

	for _, permanentError := range permanentErrors {
		if strings.Contains(message, permanentError) {
			return false
		}
	}

	// Retry errors commonly associated with temporary network failures.
	retryableErrors := []string{
		"could not resolve host",
		"could not resolve hostname",
		"failed to connect",
		"connection timed out",
		"connection reset",
		"network is unreachable",
		"temporary failure in name resolution",
		"remote end hung up unexpectedly",
		"the remote end hung up unexpectedly",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"timed out",
		"unable to access",
	}

	for _, retryableError := range retryableErrors {
		if strings.Contains(message, retryableError) {
			return true
		}
	}

	return false
}

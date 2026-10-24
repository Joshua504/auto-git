package git

import (
	"errors"
	"strings"
	"testing"
)

func TestRetrySucceedsImmediately(t *testing.T) {
	attempts := 0

	err := retry("test operation", func(error) bool {
		return true
	}, func() error {
		attempts++
		return nil
	})

	if err != nil {
		t.Fatalf("retry() returned an unexpected error: %v", err)
	}

	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestRetrySucceedsAfterFailure(t *testing.T) {
	attempts := 0

	err := retry("test operation", func(error) bool {
		return true
	}, func() error {
		attempts++

		if attempts < 2 {
			return errors.New("temporary failure")
		}

		return nil
	})

	if err != nil {
		t.Fatalf("retry() returned an unexpected error: %v", err)
	}

	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestRetryStopsOnNonRetryableError(t *testing.T) {
	attempts := 0
	expectedErr := errors.New("authentication failed")

	err := retry("test operation", func(error) bool {
		return false
	}, func() error {
		attempts++
		return expectedErr
	})

	if err == nil {
		t.Fatal("expected retry() to return an error")
	}

	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}

	if !strings.Contains(err.Error(), "test operation") {
		t.Errorf("expected operation name in error, got: %v", err)
	}

	if !errors.Is(err, expectedErr) {
		t.Error("expected returned error to wrap the original error")
	}
}

func TestRetryReturnsErrorAfterAllAttempts(t *testing.T) {
	attempts := 0
	expectedErr := errors.New("temporary failure")

	err := retry("test operation", func(error) bool {
		return true
	}, func() error {
		attempts++
		return expectedErr
	})

	if err == nil {
		t.Fatal("expected retry() to return an error")
	}

	if attempts != maxRetryAttempts {
		t.Fatalf(
			"expected %d attempts, got %d",
			maxRetryAttempts,
			attempts,
		)
	}

	if !strings.Contains(err.Error(), "test operation") {
		t.Errorf("expected operation name in error, got: %v", err)
	}

	if !errors.Is(err, expectedErr) {
		t.Error("expected returned error to wrap the original error")
	}
}

func TestIsRetryableGitError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "temporary DNS failure",
			err:       errors.New("git fetch failed: Could not resolve host: github.com"),
			retryable: true,
		},
		{
			name:      "connection timeout",
			err:       errors.New("git push failed: connection timed out"),
			retryable: true,
		},
		{
			name:      "temporary server failure",
			err:       errors.New("remote service unavailable"),
			retryable: true,
		},
		{
			name:      "authentication failure",
			err:       errors.New("Authentication failed"),
			retryable: false,
		},
		{
			name:      "permission denied",
			err:       errors.New("Permission denied (publickey)"),
			retryable: false,
		},
		{
			name:      "non-fast-forward rejection",
			err:       errors.New("push rejected: non-fast-forward"),
			retryable: false,
		},
		{
			name:      "unknown error",
			err:       errors.New("some unexpected Git error"),
			retryable: false,
		},
		{
			name:      "nil error",
			err:       nil,
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableGitError(tt.err)

			if got != tt.retryable {
				t.Errorf(
					"isRetryableGitError() = %v, want %v",
					got,
					tt.retryable,
				)
			}
		})
	}
}

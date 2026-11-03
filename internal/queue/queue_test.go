package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestQueue creates a queue backed by a temporary file that is cleaned up
// when the test finishes.
func newTestQueue(t *testing.T) *Queue {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "queue.db")
	q, err := New(dbPath)
	if err != nil {
		t.Fatalf("New queue: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

func TestNewQueue(t *testing.T) {
	q := newTestQueue(t)
	if q == nil {
		t.Fatal("expected non-nil queue")
	}
}

func TestQueue_PersistAcrossReopens(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "queue.db")

	// First instance: add an entry.
	q1, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, _, err := q1.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	q1.Close()

	// Second instance: should see the entry.
	q2, err := New(dbPath)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	defer q2.Close()

	entry, err := q2.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.RepoPath != "/repo" {
		t.Errorf("repo path = %q; want %q", entry.RepoPath, "/repo")
	}
	if entry.CommitHash != "abc123" {
		t.Errorf("commit hash = %q; want %q", entry.CommitHash, "abc123")
	}
	if entry.Status != StatusPending {
		t.Errorf("status = %q; want %q", entry.Status, StatusPending)
	}
}

func TestQueue_AddAndGet(t *testing.T) {
	q := newTestQueue(t)

	id, isDup, err := q.Add("/repo", "abc123", "https://github.com/user/repo.git")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if isDup {
		t.Error("first add should not be a duplicate")
	}
	if id == 0 {
		t.Error("expected non-zero ID")
	}

	entry, err := q.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if entry.RepoPath != "/repo" {
		t.Errorf("repo path = %q; want %q", entry.RepoPath, "/repo")
	}
	if entry.CommitHash != "abc123" {
		t.Errorf("commit hash = %q; want %q", entry.CommitHash, "abc123")
	}
	if entry.RemoteURL != "https://github.com/user/repo.git" {
		t.Errorf("remote URL = %q; want %q", entry.RemoteURL, "https://github.com/user/repo.git")
	}
	if entry.Status != StatusPending {
		t.Errorf("status = %q; want %q", entry.Status, StatusPending)
	}
	if entry.RetryCount != 0 {
		t.Errorf("retry count = %d; want 0", entry.RetryCount)
	}
	if entry.CreatedAt.IsZero() {
		t.Error("created at should not be zero")
	}
}

func TestQueue_Add_DuplicatePrevention(t *testing.T) {
	q := newTestQueue(t)

	id1, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	id2, isDup, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	if !isDup {
		t.Error("expected duplicate for same repo+commit")
	}
	if id1 != id2 {
		t.Errorf("duplicate ID = %d; want %d", id2, id1)
	}

	// Different commit should not be a duplicate.
	id3, isDup, err := q.Add("/repo", "def456", "origin")
	if err != nil {
		t.Fatalf("Add 3: %v", err)
	}
	if isDup {
		t.Error("different commit should not be a duplicate")
	}
	if id3 == id1 {
		t.Error("different commit should have a different ID")
	}

	// Different repo should not be a duplicate.
	id4, isDup, err := q.Add("/other", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add 4: %v", err)
	}
	if isDup {
		t.Error("different repo should not be a duplicate")
	}
	if id4 == id1 {
		t.Error("different repo should have a different ID")
	}
}

func TestQueue_Add_DuplicateAfterCompletion(t *testing.T) {
	q := newTestQueue(t)

	id1, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	// Complete the entry.
	if err := q.MarkCompleted(id1); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	// Adding the same commit again should NOT be a duplicate because the
	// original is completed.
	id2, isDup, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	if isDup {
		t.Error("completed entry should allow re-queueing")
	}
	if id2 == id1 {
		t.Error("re-queued entry should have a new ID")
	}
}

func TestQueue_Pending(t *testing.T) {
	q := newTestQueue(t)

	// Add three entries.
	for i, hash := range []string{"aaa", "bbb", "ccc"} {
		_, _, err := q.Add(fmt.Sprintf("/repo%d", i), hash, "origin")
		if err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	pending, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("pending count = %d; want 3", len(pending))
	}

	// Complete the first one.
	if err := q.MarkCompleted(pending[0].ID); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	pending, err = q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending count = %d; want 2", len(pending))
	}
}

func TestQueue_Completed(t *testing.T) {
	q := newTestQueue(t)

	id, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := q.MarkCompleted(id); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}

	completed, err := q.Completed()
	if err != nil {
		t.Fatalf("Completed: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("completed count = %d; want 1", len(completed))
	}
	if completed[0].Status != StatusCompleted {
		t.Errorf("status = %q; want %q", completed[0].Status, StatusCompleted)
	}
	if completed[0].CompletedAt == nil {
		t.Error("completed at should not be nil")
	}
}

func TestQueue_MarkFailed_IncrementRetry(t *testing.T) {
	q := newTestQueue(t)

	id, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Fail 4 times (maxRetries is 5, so it should stay pending).
	for i := 0; i < 4; i++ {
		status, err := q.MarkFailed(id, fmt.Sprintf("error %d", i))
		if err != nil {
			t.Fatalf("MarkFailed %d: %v", i, err)
		}
		if status != StatusPending {
			t.Errorf("attempt %d: status = %q; want %q", i, status, StatusPending)
		}
	}

	entry, err := q.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.RetryCount != 4 {
		t.Errorf("retry count = %d; want 4", entry.RetryCount)
	}

	// The 5th failure should mark it as failed.
	status, err := q.MarkFailed(id, "final error")
	if err != nil {
		t.Fatalf("MarkFailed 5: %v", err)
	}
	if status != StatusFailed {
		t.Errorf("final status = %q; want %q", status, StatusFailed)
	}

	entry, err = q.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.RetryCount != 5 {
		t.Errorf("retry count = %d; want 5", entry.RetryCount)
	}
	if entry.LastError != "final error" {
		t.Errorf("last error = %q; want %q", entry.LastError, "final error")
	}
}

func TestQueue_Failed(t *testing.T) {
	q := newTestQueue(t)

	id, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Exhaust retries.
	for i := 0; i < 5; i++ {
		_, _ = q.MarkFailed(id, "error")
	}

	failed, err := q.Failed()
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("failed count = %d; want 1", len(failed))
	}
	if failed[0].Status != StatusFailed {
		t.Errorf("status = %q; want %q", failed[0].Status, StatusFailed)
	}
}

func TestQueue_MarkRetrying(t *testing.T) {
	q := newTestQueue(t)

	id, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := q.MarkRetrying(id); err != nil {
		t.Fatalf("MarkRetrying: %v", err)
	}

	entry, err := q.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Status != StatusRetrying {
		t.Errorf("status = %q; want %q", entry.Status, StatusRetrying)
	}
}

func TestQueue_CountPending(t *testing.T) {
	q := newTestQueue(t)

	count, err := q.CountPending()
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d; want 0", count)
	}

	_, _, err = q.Add("/repo1", "a", "origin")
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	_, _, err = q.Add("/repo2", "b", "origin")
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	count, err = q.CountPending()
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d; want 2", count)
	}
}

func TestQueue_ClearCompleted(t *testing.T) {
	q := newTestQueue(t)

	// Add and complete two entries.
	for _, hash := range []string{"a", "b"} {
		id, _, err := q.Add("/repo", hash, "origin")
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if err := q.MarkCompleted(id); err != nil {
			t.Fatalf("MarkCompleted: %v", err)
		}
	}

	// Add one pending entry.
	_, _, err := q.Add("/repo", "c", "origin")
	if err != nil {
		t.Fatalf("Add pending: %v", err)
	}

	removed, err := q.ClearCompleted()
	if err != nil {
		t.Fatalf("ClearCompleted: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d; want 2", removed)
	}

	all, err := q.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("remaining entries = %d; want 1", len(all))
	}
	if all[0].CommitHash != "c" {
		t.Errorf("remaining commit = %q; want %q", all[0].CommitHash, "c")
	}
}

func TestQueue_All(t *testing.T) {
	q := newTestQueue(t)

	// Add entries.
	id1, _, err := q.Add("/repo", "a", "origin")
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	id2, _, err := q.Add("/repo", "b", "origin")
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	// Complete one, fail the other.
	if err := q.MarkCompleted(id1); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	for i := 0; i < 5; i++ {
		_, _ = q.MarkFailed(id2, "error")
	}

	all, err := q.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all count = %d; want 2", len(all))
	}
}

func TestQueue_Get_NotFound(t *testing.T) {
	q := newTestQueue(t)

	_, err := q.Get(999)
	if err == nil {
		t.Error("expected error for non-existent entry")
	}
}

func TestQueue_Close_Idempotent(t *testing.T) {
	q := newTestQueue(t)

	if err := q.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestQueue_EmptyPending(t *testing.T) {
	q := newTestQueue(t)

	pending, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending count = %d; want 0", len(pending))
	}
}

func TestQueue_Ordering_OldestFirst(t *testing.T) {
	q := newTestQueue(t)

	// Add entries with small delays to ensure ordering.
	ids := make([]int64, 3)
	for i := 0; i < 3; i++ {
		id, _, err := q.Add("/repo", fmt.Sprintf("hash%d", i), "origin")
		if err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
		ids[i] = id
		time.Sleep(10 * time.Millisecond)
	}

	pending, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("pending count = %d; want 3", len(pending))
	}

	// Should be ordered oldest first.
	if pending[0].ID != ids[0] {
		t.Errorf("first pending ID = %d; want %d", pending[0].ID, ids[0])
	}
	if pending[2].ID != ids[2] {
		t.Errorf("last pending ID = %d; want %d", pending[2].ID, ids[2])
	}
}

func TestProcessor_ProcessOnce(t *testing.T) {
	q := newTestQueue(t)

	pushed := make(map[string]bool)
	pushFunc := func(repoPath string) error {
		pushed[repoPath] = true
		return nil
	}

	// Add entries.
	for i, path := range []string{"/repo1", "/repo2"} {
		_, _, err := q.Add(path, fmt.Sprintf("hash%d", i), "origin")
		if err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	processor := NewProcessor(q, pushFunc, ProcessorConfig{RetryDelay: time.Millisecond})
	processor.ProcessOnce()

	// Give the processor a moment to finish.
	time.Sleep(100 * time.Millisecond)

	if len(pushed) != 2 {
		t.Errorf("pushed count = %d; want 2", len(pushed))
	}

	completed, err := q.Completed()
	if err != nil {
		t.Fatalf("Completed: %v", err)
	}
	if len(completed) != 2 {
		t.Errorf("completed count = %d; want 2", len(completed))
	}
}

func TestProcessor_ProcessOnce_PartialFailure(t *testing.T) {
	q := newTestQueue(t)

	pushFunc := func(repoPath string) error {
		if repoPath == "/repo2" {
			return fmt.Errorf("network error")
		}
		return nil
	}

	// Add two entries.
	id1, _, err := q.Add("/repo1", "hash1", "origin")
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	id2, _, err := q.Add("/repo2", "hash2", "origin")
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	processor := NewProcessor(q, pushFunc, ProcessorConfig{RetryDelay: time.Millisecond})
	processor.ProcessOnce()
	time.Sleep(100 * time.Millisecond)

	// repo1 should be completed.
	e1, err := q.Get(id1)
	if err != nil {
		t.Fatalf("Get 1: %v", err)
	}
	if e1.Status != StatusCompleted {
		t.Errorf("repo1 status = %q; want %q", e1.Status, StatusCompleted)
	}

	// repo2 should have incremented retry count.
	e2, err := q.Get(id2)
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	if e2.RetryCount != 1 {
		t.Errorf("repo2 retry count = %d; want 1", e2.RetryCount)
	}
	if e2.LastError != "network error" {
		t.Errorf("repo2 last error = %q; want %q", e2.LastError, "network error")
	}
}

func TestProcessor_StartStop(t *testing.T) {
	q := newTestQueue(t)

	pushCount := 0
	pushFunc := func(repoPath string) error {
		pushCount++
		return nil
	}

	_, _, err := q.Add("/repo", "hash1", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	processor := NewProcessor(q, pushFunc, ProcessorConfig{RetryDelay: 50 * time.Millisecond})

	stopCh := make(chan struct{})
	go func() {
		processor.Start()
		close(stopCh)
	}()

	// Let it process.
	time.Sleep(200 * time.Millisecond)
	processor.Stop()
	<-stopCh

	if pushCount < 1 {
		t.Errorf("push count = %d; want >= 1", pushCount)
	}
}

func TestQueue_BadPath(t *testing.T) {
	// Try to open a path inside a non-existent directory.
	_, err := New("/nonexistent/dir/that/does/not/exist/queue.db")
	if err == nil {
		t.Error("expected error for bad path")
	}
}

func TestQueue_ConcurrentAccess(t *testing.T) {
	q := newTestQueue(t)

	done := make(chan bool, 10)

	// Concurrent adds.
	for i := 0; i < 10; i++ {
		go func(i int) {
			_, _, err := q.Add("/repo", fmt.Sprintf("hash%d", i), "origin")
			if err != nil {
				t.Errorf("Add %d: %v", i, err)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines.
	for i := 0; i < 10; i++ {
		<-done
	}

	count, err := q.CountPending()
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if count != 10 {
		t.Errorf("pending count = %d; want 10", count)
	}
}

func TestQueue_DefaultMaxRetries(t *testing.T) {
	q := newTestQueue(t)

	_, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	entry, err := q.Get(1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.MaxRetries != 5 {
		t.Errorf("max retries = %d; want 5", entry.MaxRetries)
	}
}

func TestQueue_EntryFields(t *testing.T) {
	q := newTestQueue(t)

	id, _, err := q.Add("/my/repo", "deadbeef", "https://github.com/user/repo.git")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	entry, err := q.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if entry.ID != id {
		t.Errorf("ID = %d; want %d", entry.ID, id)
	}
	if entry.RepoPath != "/my/repo" {
		t.Errorf("repo path = %q; want %q", entry.RepoPath, "/my/repo")
	}
	if entry.CommitHash != "deadbeef" {
		t.Errorf("commit hash = %q; want %q", entry.CommitHash, "deadbeef")
	}
	if entry.RemoteURL != "https://github.com/user/repo.git" {
		t.Errorf("remote URL = %q; want %q", entry.RemoteURL, "https://github.com/user/repo.git")
	}
	if entry.Status != StatusPending {
		t.Errorf("status = %q; want %q", entry.Status, StatusPending)
	}
	if entry.RetryCount != 0 {
		t.Errorf("retry count = %d; want 0", entry.RetryCount)
	}
	if entry.MaxRetries != 5 {
		t.Errorf("max retries = %d; want 5", entry.MaxRetries)
	}
	if entry.CreatedAt.IsZero() {
		t.Error("created at should not be zero")
	}
	if entry.UpdatedAt.IsZero() {
		t.Error("updated at should not be zero")
	}
	if entry.CompletedAt != nil {
		t.Error("completed at should be nil for pending entry")
	}
}

func TestQueue_MarkCompleted_CompletedAt(t *testing.T) {
	q := newTestQueue(t)

	id, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	before := time.Now().UTC().Truncate(time.Second)
	if err := q.MarkCompleted(id); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	after := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	time.Sleep(50 * time.Millisecond)

	entry, err := q.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if entry.CompletedAt == nil {
		t.Fatal("completed at should not be nil")
	}
	// Truncate to second precision since SQLite stores timestamps as strings
	completedTruncated := entry.CompletedAt.Truncate(time.Second)
	if completedTruncated.Before(before) || completedTruncated.After(after) {
		t.Errorf("completed at = %v; should be between %v and %v",
			entry.CompletedAt, before, after)
	}
}

func TestQueue_Retrying(t *testing.T) {
	q := newTestQueue(t)

	id, _, err := q.Add("/repo", "abc123", "origin")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := q.MarkRetrying(id); err != nil {
		t.Fatalf("MarkRetrying: %v", err)
	}

	retrying, err := q.Retrying()
	if err != nil {
		t.Fatalf("Retrying: %v", err)
	}
	if len(retrying) != 1 {
		t.Fatalf("retrying count = %d; want 1", len(retrying))
	}
	if retrying[0].Status != StatusRetrying {
		t.Errorf("status = %q; want %q", retrying[0].Status, StatusRetrying)
	}
}

func TestQueue_NewInvalidPath(t *testing.T) {
	// Use a file path that's actually a file, not a directory.
	tmpFile := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := New(tmpFile + "/subdir/queue.db")
	if err == nil {
		t.Error("expected error when db path goes through a file")
	}
}

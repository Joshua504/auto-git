package queue

import (
	"database/sql"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// EntryStatus tracks the lifecycle of a queued backup operation.
type EntryStatus string

const (
	StatusPending   EntryStatus = "pending"
	StatusRetrying  EntryStatus = "retrying"
	StatusCompleted EntryStatus = "completed"
	StatusFailed    EntryStatus = "failed"
)

// Entry represents a single queued backup operation.
type Entry struct {
	ID            int64
	RepoPath      string
	CommitHash    string
	RemoteURL     string
	Status        EntryStatus
	RetryCount    int
	MaxRetries    int
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   *time.Time
}

// Queue persists pending backup operations in a SQLite database so that
// operations survive application restarts and can be retried when network
// connectivity returns.
type Queue struct {
	db     *sql.DB
	mu     sync.Mutex
	closed bool
}

// New opens (or creates) the SQLite database at the given path and returns
// a ready-to-use queue. The path is typically inside the repository directory
// or a shared data directory.
func New(dbPath string) (*Queue, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening queue database: %w", err)
	}

	q := &Queue{db: db}
	if err := q.init(); err != nil {
		db.Close()
		return nil, err
	}

	return q, nil
}

// Close releases the database connection.
func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return nil
	}
	q.closed = true
	return q.db.Close()
}

// init creates the queue table if it does not already exist.
func (q *Queue) init() error {
	query := `
	CREATE TABLE IF NOT EXISTS backup_queue (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		repo_path TEXT NOT NULL,
		commit_hash TEXT NOT NULL,
		remote_url TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		retry_count INTEGER NOT NULL DEFAULT 0,
		max_retries INTEGER NOT NULL DEFAULT 5,
		last_error TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		completed_at TEXT
	);`

	if _, err := q.db.Exec(query); err != nil {
		return fmt.Errorf("creating queue table: %w", err)
	}

	// Create an index for fast lookups by repo + commit (deduplication).
	idxQuery := `
	CREATE INDEX IF NOT EXISTS idx_queue_repo_commit
	ON backup_queue(repo_path, commit_hash);`

	if _, err := q.db.Exec(idxQuery); err != nil {
		return fmt.Errorf("creating queue index: %w", err)
	}

	return nil
}

// Add inserts a new backup operation into the queue. If an entry with the
// same repo path and commit hash already exists and is still pending or
// retrying, it is a duplicate and the existing ID is returned with
// isDuplicate set to true.
func (q *Queue) Add(repoPath, commitHash, remoteURL string) (id int64, isDuplicate bool, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Check for duplicates first.
	var existingID int64
	err = q.db.QueryRow(`
		SELECT id FROM backup_queue
		WHERE repo_path = ? AND commit_hash = ? AND status IN ('pending', 'retrying')
		LIMIT 1`,
		repoPath, commitHash,
	).Scan(&existingID)

	if err == nil {
		return existingID, true, nil
	} else if err != sql.ErrNoRows {
		return 0, false, fmt.Errorf("checking for duplicate: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := q.db.Exec(`
		INSERT INTO backup_queue (repo_path, commit_hash, remote_url, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		repoPath, commitHash, remoteURL, now, now,
	)
	if err != nil {
		return 0, false, fmt.Errorf("inserting queue entry: %w", err)
	}

	id, _ = result.LastInsertId()
	return id, false, nil
}

// Pending returns all entries that are waiting to be processed, ordered by
// creation time (oldest first).
func (q *Queue) Pending() ([]Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.queryEntries(`
		SELECT id, repo_path, commit_hash, remote_url, status,
		       retry_count, max_retries, last_error,
		       created_at, updated_at, completed_at
		FROM backup_queue
		WHERE status = 'pending'
		ORDER BY created_at ASC`)
}

// Retrying returns entries currently in the retrying state.
func (q *Queue) Retrying() ([]Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.queryEntries(`
		SELECT id, repo_path, commit_hash, remote_url, status,
		       retry_count, max_retries, last_error,
		       created_at, updated_at, completed_at
		FROM backup_queue
		WHERE status = 'retrying'
		ORDER BY updated_at ASC`)
}

// Completed returns entries that have been successfully processed.
func (q *Queue) Completed() ([]Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.queryEntries(`
		SELECT id, repo_path, commit_hash, remote_url, status,
		       retry_count, max_retries, last_error,
		       created_at, updated_at, completed_at
		FROM backup_queue
		WHERE status = 'completed'
		ORDER BY completed_at DESC`)
}

// Failed returns entries that have exceeded their retry limit.
func (q *Queue) Failed() ([]Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.queryEntries(`
		SELECT id, repo_path, commit_hash, remote_url, status,
		       retry_count, max_retries, last_error,
		       created_at, updated_at, completed_at
		FROM backup_queue
		WHERE status = 'failed'
		ORDER BY updated_at DESC`)
}

// All returns all entries regardless of status.
func (q *Queue) All() ([]Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.queryEntries(`
		SELECT id, repo_path, commit_hash, remote_url, status,
		       retry_count, max_retries, last_error,
		       created_at, updated_at, completed_at
		FROM backup_queue
		ORDER BY created_at DESC`)
}

// MarkCompleted marks an entry as successfully completed.
func (q *Queue) MarkCompleted(id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(`
		UPDATE backup_queue
		SET status = 'completed', updated_at = ?, completed_at = ?
		WHERE id = ?`,
		now, now, id,
	)
	if err != nil {
		return fmt.Errorf("marking entry completed: %w", err)
	}
	return nil
}

// MarkFailed increments the retry count and updates the error message.
// If the retry count exceeds maxRetries, the status is set to "failed".
// Otherwise, the status is set back to "pending" for the next retry cycle.
func (q *Queue) MarkFailed(id int64, errorMessage string) (EntryStatus, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(`
		UPDATE backup_queue
		SET retry_count = retry_count + 1,
		    last_error = ?,
		    updated_at = ?,
		    status = CASE
		        WHEN retry_count + 1 >= max_retries THEN 'failed'
		        ELSE 'pending'
		    END
		WHERE id = ?`,
		errorMessage, now, id,
	)
	if err != nil {
		return "", fmt.Errorf("marking entry failed: %w", err)
	}

	// Return the new status.
	var newStatus EntryStatus
	err = q.db.QueryRow(`SELECT status FROM backup_queue WHERE id = ?`, id).Scan(&newStatus)
	if err != nil {
		return "", fmt.Errorf("reading updated status: %w", err)
	}
	return newStatus, nil
}

// MarkRetrying sets an entry to the retrying state while it is being processed.
func (q *Queue) MarkRetrying(id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(`
		UPDATE backup_queue
		SET status = 'retrying', updated_at = ?
		WHERE id = ?`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("marking entry retrying: %w", err)
	}
	return nil
}

// Get retrieves a single entry by ID.
func (q *Queue) Get(id int64) (Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	entries, err := q.queryEntries(`
		SELECT id, repo_path, commit_hash, remote_url, status,
		       retry_count, max_retries, last_error,
		       created_at, updated_at, completed_at
		FROM backup_queue
		WHERE id = ?`, id)
	if err != nil {
		return Entry{}, err
	}

	if len(entries) == 0 {
		return Entry{}, fmt.Errorf("entry %d not found", id)
	}
	return entries[0], nil
}

// CountPending returns the number of pending entries.
func (q *Queue) CountPending() (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var count int
	err := q.db.QueryRow(`SELECT COUNT(*) FROM backup_queue WHERE status = 'pending'`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting pending entries: %w", err)
	}
	return count, nil
}

// ClearCompleted removes all completed entries from the queue.
func (q *Queue) ClearCompleted() (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	result, err := q.db.Exec(`DELETE FROM backup_queue WHERE status = 'completed'`)
	if err != nil {
		return 0, fmt.Errorf("clearing completed entries: %w", err)
	}

	return result.RowsAffected()
}

// queryEntries is a helper that runs a SELECT query and scans the results
// into Entry structs. The caller must hold q.mu.
func (q *Queue) queryEntries(query string, args ...interface{}) ([]Entry, error) {
	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying queue: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var createdAt, updatedAt, completedAt sql.NullString
		var lastError sql.NullString

		err := rows.Scan(
			&e.ID, &e.RepoPath, &e.CommitHash, &e.RemoteURL,
			&e.Status, &e.RetryCount, &e.MaxRetries, &lastError,
			&createdAt, &updatedAt, &completedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}

		if lastError.Valid {
			e.LastError = lastError.String
		}
		if createdAt.Valid {
			e.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
		}
		if updatedAt.Valid {
			e.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt.String)
		}
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339, completedAt.String)
			e.CompletedAt = &t
		}

		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return entries, nil
}

// IsNetworkAvailable performs a lightweight connectivity check by attempting
// to resolve a well-known DNS name. It returns true if the DNS lookup
// succeeds within a short timeout.
func IsNetworkAvailable() bool {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Processor handles the retry loop for pending queue entries.
type Processor struct {
	queue       *Queue
	pushFunc    func(repoPath string) error
	retryDelay  time.Duration
	stopCh      chan struct{}
	stopped     chan struct{}

	// onCompleted is called after a batch of pending entries has been
	// processed. It receives the number of entries that were successfully
	// pushed in that batch. It may be nil.
	onCompleted func(completed int)
}

// ProcessorConfig holds configuration for the Processor.
type ProcessorConfig struct {
	// RetryDelay is how long to wait between retry cycles.
	RetryDelay time.Duration

	// OnCompleted is called after a batch of pending entries has been
	// processed. It receives the number of entries that were successfully
	// pushed in that batch.
	OnCompleted func(completed int)
}

// DefaultProcessorConfig returns sensible defaults.
func DefaultProcessorConfig() ProcessorConfig {
	return ProcessorConfig{
		RetryDelay: 30 * time.Second,
	}
}

// NewProcessor creates a processor that will retry pending entries.
// pushFunc is called for each pending entry to attempt the push operation.
func NewProcessor(queue *Queue, pushFunc func(repoPath string) error, cfg ProcessorConfig) *Processor {
	if cfg.RetryDelay == 0 {
		cfg = DefaultProcessorConfig()
	}
	return &Processor{
		queue:       queue,
		pushFunc:    pushFunc,
		retryDelay:  cfg.RetryDelay,
		stopCh:      make(chan struct{}),
		stopped:     make(chan struct{}),
		onCompleted: cfg.OnCompleted,
	}
}

// Start begins the retry loop in the background. It blocks until Start
// returns or Stop is called.
func (p *Processor) Start() {
	defer close(p.stopped)

	// Initial check: if network is already available, process immediately.
	if IsNetworkAvailable() {
		p.processPending()
	}

	ticker := time.NewTicker(p.retryDelay)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			if IsNetworkAvailable() {
				p.processPending()
			}
		}
	}
}

// Stop signals the processor to exit and waits for it to finish.
func (p *Processor) Stop() {
	close(p.stopCh)
	<-p.stopped
}

// ProcessOnce runs a single pass over pending entries. It is useful for
// manual triggering or testing.
func (p *Processor) ProcessOnce() {
	if IsNetworkAvailable() {
		p.processPending()
	}
}

// processPending retrieves all pending entries and attempts to push them.
func (p *Processor) processPending() {
	pending, err := p.queue.Pending()
	if err != nil {
		log.Printf("queue: could not read pending entries: %v", err)
		return
	}

	if len(pending) == 0 {
		return
	}

	log.Printf("queue: processing %d pending backup(s)", len(pending))

	completed := 0
	for _, entry := range pending {
		if err := p.queue.MarkRetrying(entry.ID); err != nil {
			log.Printf("queue: could not mark entry %d as retrying: %v", entry.ID, err)
			continue
		}

		log.Printf("queue: retrying push for %s (commit %s, attempt %d)",
			entry.RepoPath, entry.CommitHash, entry.RetryCount+1)

		if err := p.pushFunc(entry.RepoPath); err != nil {
			newStatus, markErr := p.queue.MarkFailed(entry.ID, err.Error())
			if markErr != nil {
				log.Printf("queue: could not update entry %d after failure: %v", entry.ID, markErr)
			}
			if newStatus == StatusFailed {
				log.Printf("queue: giving up on %s (commit %s) after %d retries",
					entry.RepoPath, entry.CommitHash, entry.RetryCount+1)
			}
			continue
		}

		if err := p.queue.MarkCompleted(entry.ID); err != nil {
			log.Printf("queue: could not mark entry %d as completed: %v", entry.ID, err)
			continue
		}

		completed++
		log.Printf("queue: successfully pushed %s (commit %s)", entry.RepoPath, entry.CommitHash)
	}

	if completed > 0 && p.onCompleted != nil {
		p.onCompleted(completed)
	}
}

package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Joshua504/auto-git/internal/scheduler"
)

// recorder stands in for a backup pass. It counts calls and notices when two
// passes run at the same time, which must never happen.
type recorder struct {
	mu         sync.Mutex
	calls      int
	inFlight   int
	overlapped bool
	delay      time.Duration
}

func (r *recorder) backup() {
	r.mu.Lock()
	r.calls++
	r.inFlight++

	if r.inFlight > 1 {
		r.overlapped = true
	}

	r.mu.Unlock()

	time.Sleep(r.delay)

	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls
}

func (r *recorder) hadOverlap() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.overlapped
}

// harness runs a watcher against a fake editor.
type harness struct {
	t          *testing.T
	editorOpen atomic.Bool
	rec        *recorder
	cancel     context.CancelFunc
	done       chan struct{}
}

// newHarness starts a watcher. A long schedulerInterval means scheduled ticks
// never fire, so every backup counted is an open-triggered or close-triggered one.
func newHarness(t *testing.T, schedulerInterval, backupDelay time.Duration) *harness {
	t.Helper()

	h := &harness{
		t:    t,
		rec:  &recorder{delay: backupDelay},
		done: make(chan struct{}),
	}

	s, err := scheduler.New(schedulerInterval, h.rec.backup)
	if err != nil {
		t.Fatal(err)
	}

	w := &watcher{
		isRunning: h.editorOpen.Load,
		pollEvery: 5 * time.Millisecond,
		scheduler: s,
		backup:    h.rec.backup,
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel

	go func() {
		defer close(h.done)
		w.run(ctx)
	}()

	t.Cleanup(h.stop)

	return h
}

// stop shuts the watcher down and waits for it. It is safe to call twice.
func (h *harness) stop() {
	h.cancel()

	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		h.t.Error("watcher did not stop after its context was cancelled")
	}
}

func (h *harness) waitForCalls(want int) {
	h.t.Helper()

	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) {
		if h.rec.count() >= want {
			return
		}

		time.Sleep(2 * time.Millisecond)
	}

	h.t.Fatalf("expected at least %d backup(s), got %d", want, h.rec.count())
}

// settle gives the watcher time to do anything it is going to do, so a test
// can assert that nothing more happened.
func (h *harness) settle() { time.Sleep(80 * time.Millisecond) }

func TestBacksUpImmediatelyWhenEditorOpens(t *testing.T) {
	h := newHarness(t, time.Hour, 0)

	h.editorOpen.Store(true)

	// The scheduler interval is an hour, so only an immediate backup can arrive.
	h.waitForCalls(1)
	h.settle()

	if got := h.rec.count(); got != 1 {
		t.Fatalf("expected exactly one backup on open, got %d", got)
	}
}

func TestRunsFinalBackupWhenEditorCloses(t *testing.T) {
	h := newHarness(t, time.Hour, 0)

	h.editorOpen.Store(true)
	h.waitForCalls(1)

	h.editorOpen.Store(false)
	h.waitForCalls(2)
	h.settle()

	if got := h.rec.count(); got != 2 {
		t.Fatalf("expected one backup on open and one on close, got %d", got)
	}
}

func TestNoBackupsWhileNoEditorIsOpen(t *testing.T) {
	h := newHarness(t, time.Hour, 0)

	h.settle()

	if got := h.rec.count(); got != 0 {
		t.Fatalf("expected no backups without an open editor, got %d", got)
	}
}

func TestShutdownSkipsFinalBackup(t *testing.T) {
	h := newHarness(t, time.Hour, 0)

	h.editorOpen.Store(true)
	h.waitForCalls(1)

	h.stop()

	if got := h.rec.count(); got != 1 {
		t.Fatalf("stopping AutoGit must not start another backup, got %d total", got)
	}
}

func TestReopeningEditorBacksUpAgain(t *testing.T) {
	h := newHarness(t, time.Hour, 0)

	for session := 1; session <= 2; session++ {
		h.editorOpen.Store(true)
		h.waitForCalls(session*2 - 1)

		h.editorOpen.Store(false)
		h.waitForCalls(session * 2)
	}

	h.settle()

	if got := h.rec.count(); got != 4 {
		t.Fatalf("expected an open and a close backup per session (4), got %d", got)
	}

	if h.rec.hadOverlap() {
		t.Fatal("two backups ran at the same time")
	}
}

func TestBackupsNeverOverlap(t *testing.T) {
	// Scheduled ticks come faster than a backup finishes, so the scheduler is
	// busy at the moment the editor closes.
	h := newHarness(t, 3*time.Millisecond, 15*time.Millisecond)

	h.editorOpen.Store(true)
	h.waitForCalls(4)

	h.editorOpen.Store(false)
	time.Sleep(200 * time.Millisecond)

	h.stop()

	if h.rec.hadOverlap() {
		t.Fatal("the closing backup overlapped a scheduled backup")
	}
}

func TestEditorClosingDuringOpeningBackup(t *testing.T) {
	h := newHarness(t, time.Hour, 60*time.Millisecond)

	h.editorOpen.Store(true)
	h.waitForCalls(1) // the opening backup is now running

	h.editorOpen.Store(false)
	h.waitForCalls(2)
	time.Sleep(150 * time.Millisecond)

	if got := h.rec.count(); got != 2 {
		t.Fatalf("expected the opening backup and one final backup, got %d", got)
	}

	if h.rec.hadOverlap() {
		t.Fatal("the closing backup started while the opening backup was still running")
	}
}

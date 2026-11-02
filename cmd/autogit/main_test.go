package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Joshua504/auto-git/internal/config"
	"github.com/Joshua504/auto-git/internal/git"
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
	events     []string // "pull" and "backup", in the order they started
}

// enter and leave bracket one pass of work, noticing any overlap.
func (r *recorder) enter(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.inFlight++

	if r.inFlight > 1 {
		r.overlapped = true
	}

	r.mu.Unlock()
}

func (r *recorder) leave() {
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
}

// pull stands in for the check that runs when an editor opens.
func (r *recorder) pull() {
	r.enter("pull")
	time.Sleep(r.delay)
	r.leave()
}

func (r *recorder) history() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.events...)
}

func (r *recorder) backup() {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()

	r.enter("backup")
	time.Sleep(r.delay)
	r.leave()
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

	return startHarness(t, schedulerInterval, backupDelay, false)
}

// newHarnessWithPull is newHarness with the pull-on-open step switched on.
func newHarnessWithPull(t *testing.T, schedulerInterval, backupDelay time.Duration) *harness {
	t.Helper()

	return startHarness(t, schedulerInterval, backupDelay, true)
}

func startHarness(t *testing.T, schedulerInterval, backupDelay time.Duration, withPull bool) *harness {
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

	if withPull {
		w.pull = h.rec.pull
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

func TestPullsBeforeTheOpeningBackup(t *testing.T) {
	h := newHarnessWithPull(t, time.Hour, 0)

	h.editorOpen.Store(true)
	h.waitForCalls(1)
	h.settle()

	if got, want := h.rec.history(), []string{"pull", "backup"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected the pull to run first, got %v", got)
	}
}

func TestPullsEveryTimeAnEditorOpens(t *testing.T) {
	h := newHarnessWithPull(t, time.Hour, 0)

	for session := 1; session <= 2; session++ {
		h.editorOpen.Store(true)
		h.waitForCalls(session*2 - 1)

		h.editorOpen.Store(false)
		h.waitForCalls(session * 2)
	}

	h.settle()

	// Each session: pull and backup when it opens, one final backup when it closes.
	want := []string{"pull", "backup", "backup", "pull", "backup", "backup"}
	if got := h.rec.history(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNoPullWhenItIsSwitchedOff(t *testing.T) {
	h := newHarness(t, time.Hour, 0)

	h.editorOpen.Store(true)
	h.waitForCalls(1)
	h.settle()

	if got, want := h.rec.history(), []string{"backup"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected no pull, got %v", got)
	}
}

func TestPullAndBackupsNeverOverlap(t *testing.T) {
	// Slow passes and fast ticks, so everything is busy at once if it can be.
	h := newHarnessWithPull(t, 3*time.Millisecond, 15*time.Millisecond)

	h.editorOpen.Store(true)
	h.waitForCalls(3)

	h.editorOpen.Store(false)
	time.Sleep(200 * time.Millisecond)

	h.stop()

	if h.rec.hadOverlap() {
		t.Fatal("the pull overlapped a backup")
	}
}

func TestOpenSyncMessage(t *testing.T) {
	const repo = "/work/repo"

	tests := []struct {
		name   string
		mode   string
		result git.OpenSyncResult
		err    error
		want   []string // every fragment must appear
		avoid  []string // none may appear
	}{
		{"up to date", "", git.OpenSyncResult{Outcome: git.OpenSyncUpToDate}, nil,
			[]string{"Nothing to pull in /work/repo"}, nil},
		{"one commit", "", git.OpenSyncResult{Outcome: git.OpenSyncFastForwarded, Commits: 1}, nil,
			[]string{"Pulled 1 new commit into /work/repo"}, []string{"commits"}},
		{"several commits", "branch", git.OpenSyncResult{Outcome: git.OpenSyncFastForwarded, Commits: 3}, nil,
			[]string{"Pulled 3 new commits into /work/repo"}, nil},
		{"diverged in commit mode", "commit", git.OpenSyncResult{Outcome: git.OpenSyncDiverged}, nil,
			[]string{"Not pulling", "AutoGit will try to combine them"}, []string{"yourself"}},
		{"diverged in branch mode", "branch", git.OpenSyncResult{Outcome: git.OpenSyncDiverged}, nil,
			[]string{"Not pulling", "Run git pull yourself"}, []string{"AutoGit will"}},
		{"blocked in commit mode", "", git.OpenSyncResult{Outcome: git.OpenSyncBlocked, Conflicts: []string{"a.go", "b.go"}}, nil,
			[]string{"a.go, b.go", "AutoGit will commit them"}, []string{"stash"}},
		{"blocked in branch mode", "branch", git.OpenSyncResult{Outcome: git.OpenSyncBlocked, Conflicts: []string{"a.go"}}, nil,
			[]string{"(a.go)", "Commit or stash them"}, []string{"AutoGit will"}},
		{"many blocking files are shortened", "", git.OpenSyncResult{Outcome: git.OpenSyncBlocked, Conflicts: []string{"a", "b", "c", "d", "e"}}, nil,
			[]string{"a, b, c and 2 more"}, []string{"d, e"}},
		{"skipped repository", "", git.OpenSyncResult{}, &git.SkippedError{Reason: "a merge is in progress"},
			[]string{"Skipping /work/repo: a merge is in progress"}, nil},
		{"failure", "", git.OpenSyncResult{}, errors.New("network down"),
			[]string{"Could not check /work/repo for new commits: network down"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openSyncMessage(repo, tt.mode, tt.result, tt.err)

			for _, fragment := range tt.want {
				if !strings.Contains(got, fragment) {
					t.Errorf("message %q should contain %q", got, fragment)
				}
			}

			for _, fragment := range tt.avoid {
				if strings.Contains(got, fragment) {
					t.Errorf("message %q should not contain %q", got, fragment)
				}
			}
		})
	}
}

// The config package validates placeholders and the git package fills them in.
// If the two lists ever differ, a valid-looking message would be written literally.
func TestConfigAndGitAgreeOnPlaceholders(t *testing.T) {
	if got, want := config.MessagePlaceholders(), git.MessagePlaceholders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("config accepts %v but git fills in %v", got, want)
	}
}

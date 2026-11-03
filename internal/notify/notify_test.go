package notify

import (
	"sync"
	"testing"
	"time"
)

// mockProvider records calls to Notify for testing.
type mockProvider struct {
	mu      sync.Mutex
	calls   []string
	enabled bool
}

func (m *mockProvider) Notify(title, message string) {
	if !m.enabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, title+": "+message)
}

func (m *mockProvider) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func newTestSender(eventEnabled func(Event) bool) Sender {
	return Sender{
		provider:     &mockProvider{enabled: true},
		eventEnabled: eventEnabled,
		lastSent:     make(map[string]time.Time),
		cooldown:     5 * time.Minute,
		seenCount:    make(map[string]int),
		maxRepeats:   3,
	}
}

func TestSender_ShouldNotify(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		s := NewDisabled()
		if s.ShouldNotify() {
			t.Error("disabled sender should not notify")
		}
	})

	t.Run("enabled", func(t *testing.T) {
		s := New(nil)
		if !s.ShouldNotify() {
			t.Error("enabled sender should notify")
		}
	})
}

// TestSender_NoPanic verifies that calling notification methods on a disabled
// sender does not panic — it should simply return without doing anything.
func TestSender_NoPanic(t *testing.T) {
	s := NewDisabled()

	// These should all be no-ops and never panic.
	s.Success("title", "message")
	s.Warning("title", "message")
	s.Error("title", "message")
	s.QueueSynced("title", "message")
	s.BranchDiverged("title", "message")
}

func TestSender_EventFiltering(t *testing.T) {
	// Only allow BackupSuccess.
	s := newTestSender(func(e Event) bool {
		return e == BackupSuccess
	})

	s.Success("ok", "done")
	s.Error("fail", "error")
	s.QueueSynced("sync", "synced")
	s.BranchDiverged("div", "diverged")

	if got := s.provider.(*mockProvider).count(); got != 1 {
		t.Errorf("expected 1 notification (Success only), got %d", got)
	}
}

func TestSender_AllEventsEnabled(t *testing.T) {
	s := newTestSender(nil) // nil means all events enabled

	s.Success("ok", "done")
	s.Error("fail", "error")
	s.QueueSynced("sync", "synced")
	s.BranchDiverged("div", "diverged")

	if got := s.provider.(*mockProvider).count(); got != 4 {
		t.Errorf("expected 4 notifications, got %d", got)
	}
}

func TestSender_Deduplication_SameErrorSuppressed(t *testing.T) {
	s := newTestSender(nil)
	s.cooldown = 1 * time.Hour // long cooldown so the second call is suppressed

	s.Error("fail", "network unreachable")
	s.Error("fail", "network unreachable") // duplicate — should be suppressed

	if got := s.provider.(*mockProvider).count(); got != 1 {
		t.Errorf("expected 1 notification (duplicate suppressed), got %d", got)
	}
}

func TestSender_Deduplication_DifferentErrorAllowed(t *testing.T) {
	s := newTestSender(nil)
	s.cooldown = 1 * time.Hour

	s.Error("fail", "network unreachable")
	s.Error("fail", "authentication failed") // different message — should be sent

	if got := s.provider.(*mockProvider).count(); got != 2 {
		t.Errorf("expected 2 notifications (different errors), got %d", got)
	}
}

func TestSender_Deduplication_MaxRepeats(t *testing.T) {
	s := newTestSender(nil)
	s.cooldown = 0         // no cooldown, only maxRepeats matters
	s.maxRepeats = 2

	s.Error("fail", "network unreachable")
	s.Error("fail", "network unreachable")
	s.Error("fail", "network unreachable") // exceeds maxRepeats — suppressed

	if got := s.provider.(*mockProvider).count(); got != 2 {
		t.Errorf("expected 2 notifications (maxRepeats=2), got %d", got)
	}
}

func TestSender_Deduplication_SuccessNotDeduplicated(t *testing.T) {
	s := newTestSender(nil)
	s.cooldown = 1 * time.Hour

	s.Success("ok", "backup complete")
	s.Success("ok", "backup complete")
	s.Success("ok", "backup complete")

	if got := s.provider.(*mockProvider).count(); got != 3 {
		t.Errorf("expected 3 notifications (success not deduplicated), got %d", got)
	}
}

func TestSender_Deduplication_WarningIsDeduplicated(t *testing.T) {
	s := newTestSender(nil)
	s.cooldown = 1 * time.Hour

	s.Warning("skip", "locked")
	s.Warning("skip", "locked") // duplicate — should be suppressed

	if got := s.provider.(*mockProvider).count(); got != 1 {
		t.Errorf("expected 1 notification (warning deduplicated), got %d", got)
	}
}

func TestSender_Deduplication_CooldownExpires(t *testing.T) {
	s := newTestSender(nil)
	s.cooldown = 1 * time.Millisecond
	s.maxRepeats = 0 // disable max repeats

	s.Error("fail", "network unreachable")
	time.Sleep(2 * time.Millisecond) // wait for cooldown to expire
	s.Error("fail", "network unreachable") // should be allowed again

	if got := s.provider.(*mockProvider).count(); got != 2 {
		t.Errorf("expected 2 notifications (cooldown expired), got %d", got)
	}
}

func TestSender_NewDisabled_NoNotifications(t *testing.T) {
	s := NewDisabled()
	s.Success("ok", "done")
	s.Error("fail", "error")
	s.QueueSynced("sync", "synced")

	if s.ShouldNotify() {
		t.Error("NewDisabled should return a sender that does not notify")
	}
}

func TestSender_QueueSynced(t *testing.T) {
	s := newTestSender(nil)
	s.QueueSynced("sync", "3 backups synced")

	if got := s.provider.(*mockProvider).count(); got != 1 {
		t.Errorf("expected 1 notification, got %d", got)
	}
}

func TestSender_BranchDiverged(t *testing.T) {
	s := newTestSender(nil)
	s.BranchDiverged("div", "branch has diverged")

	if got := s.provider.(*mockProvider).count(); got != 1 {
		t.Errorf("expected 1 notification, got %d", got)
	}
}

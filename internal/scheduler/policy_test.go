package scheduler

import (
	"testing"
	"time"
)

// --- FixedPolicy tests ---

func TestFixedPolicyShouldBackup(t *testing.T) {
	p := FixedPolicy{}
	if !p.ShouldBackup("/any/path") {
		t.Error("FixedPolicy should always return true")
	}
}

func TestFixedPolicyNoOps(t *testing.T) {
	p := FixedPolicy{}
	// These should not panic.
	p.RecordBackup("/path")
	p.RecordActivity("/path")
}

// --- SmartPolicy tests ---

func TestSmartPolicyInitialBackup(t *testing.T) {
	p := NewSmartPolicy(1*time.Hour, 0)

	// First call should always return true (initial backup).
	if !p.ShouldBackup("/repo") {
		t.Error("expected initial backup to be allowed")
	}
}

func TestSmartPolicySkipsWhenNoActivity(t *testing.T) {
	p := NewSmartPolicy(1*time.Hour, 0)

	// Prime: allow the initial backup.
	if !p.ShouldBackup("/repo") {
		t.Fatal("expected initial backup")
	}
	p.RecordBackup("/repo")

	// Without activity, should skip.
	if p.ShouldBackup("/repo") {
		t.Error("expected skip when no activity recorded")
	}
}

func TestSmartPolicyBacksUpWhenActive(t *testing.T) {
	p := NewSmartPolicy(1*time.Hour, 0)

	// Prime.
	p.RecordBackup("/repo")

	// Record activity.
	p.RecordActivity("/repo")

	if !p.ShouldBackup("/repo") {
		t.Error("expected backup when activity is recorded")
	}
}

func TestSmartPolicyActivityExpires(t *testing.T) {
	p := NewSmartPolicy(100*time.Millisecond, 0)

	p.RecordBackup("/repo")
	p.RecordActivity("/repo")

	// Activity is fresh — should backup.
	if !p.ShouldBackup("/repo") {
		t.Error("expected backup with fresh activity")
	}

	// Wait for activity window to expire.
	time.Sleep(150 * time.Millisecond)

	if p.ShouldBackup("/repo") {
		t.Error("expected skip after activity window expired")
	}
}

func TestSmartPolicySafetyInterval(t *testing.T) {
	p := NewSmartPolicy(1*time.Hour, 100*time.Millisecond)

	// Prime.
	p.RecordBackup("/repo")

	// No activity, but safety interval not yet elapsed.
	if p.ShouldBackup("/repo") {
		t.Error("expected skip before safety interval")
	}

	// Wait for safety interval.
	time.Sleep(150 * time.Millisecond)

	if !p.ShouldBackup("/repo") {
		t.Error("expected backup after safety interval elapsed")
	}
}

func TestSmartPolicySeparateRepos(t *testing.T) {
	p := NewSmartPolicy(1*time.Hour, 0)

	// Prime repo A.
	p.RecordBackup("/repo-a")
	p.RecordActivity("/repo-a")

	// Repo B has never been backed up — should allow initial backup.
	if !p.ShouldBackup("/repo-b") {
		t.Error("expected initial backup for new repo")
	}

	// Repo A should still be allowed (activity recorded).
	if !p.ShouldBackup("/repo-a") {
		t.Error("expected backup for active repo A")
	}
}

// --- HybridPolicy tests ---

func TestHybridPolicyInitialBackup(t *testing.T) {
	p := NewHybridPolicy(1*time.Minute, 1*time.Hour)

	if !p.ShouldBackup("/repo") {
		t.Error("expected initial backup to be allowed")
	}
}

func TestHybridPolicyRespectsMinInterval(t *testing.T) {
	p := NewHybridPolicy(200*time.Millisecond, 1*time.Hour)

	p.RecordBackup("/repo")
	p.RecordActivity("/repo")

	// Min interval not yet elapsed.
	if p.ShouldBackup("/repo") {
		t.Error("expected skip within min interval")
	}

	// Wait for min interval.
	time.Sleep(250 * time.Millisecond)

	if !p.ShouldBackup("/repo") {
		t.Error("expected backup after min interval elapsed")
	}
}

func TestHybridPolicyActivityExpires(t *testing.T) {
	p := NewHybridPolicy(100*time.Millisecond, 100*time.Millisecond)

	p.RecordBackup("/repo")
	p.RecordActivity("/repo")

	// Wait for activity window to expire (and min interval).
	time.Sleep(150 * time.Millisecond)

	if p.ShouldBackup("/repo") {
		t.Error("expected skip after activity window expired")
	}
}

func TestHybridPolicySeparateRepos(t *testing.T) {
	p := NewHybridPolicy(1*time.Minute, 1*time.Hour)

	p.RecordBackup("/repo-a")
	p.RecordActivity("/repo-a")

	// Repo B is new — should allow initial backup.
	if !p.ShouldBackup("/repo-b") {
		t.Error("expected initial backup for new repo")
	}
}

// --- Policy interface ---

func TestPolicyInterfaceFixed(t *testing.T) {
	var p Policy = FixedPolicy{}
	if !p.ShouldBackup("/path") {
		t.Error("expected true")
	}
}

func TestPolicyInterfaceSmart(t *testing.T) {
	var p Policy = NewSmartPolicy(1*time.Hour, 0)
	if !p.ShouldBackup("/path") {
		t.Error("expected initial backup")
	}
}

func TestPolicyInterfaceHybrid(t *testing.T) {
	var p Policy = NewHybridPolicy(1*time.Minute, 1*time.Hour)
	if !p.ShouldBackup("/path") {
		t.Error("expected initial backup")
	}
}

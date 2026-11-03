package scheduler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Joshua504/auto-git/internal/git"
	"github.com/Joshua504/auto-git/internal/power"
)

func TestNew(t *testing.T) {
	interval := time.Hour
	task := func() {}

	s, err := New(interval, task)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if s == nil {
		t.Fatal("expected a scheduler, got nil")
	}

	if s.interval != interval {
		t.Errorf("expected interval %v, got %v", interval, s.interval)
	}

	if s.task == nil {
		t.Fatal("expected task to be set, got nil")
	}
}

func TestNewZeroInterval(t *testing.T) {
	task := func() {}

	s, err := New(0, task)

	if err == nil {
		t.Fatal("expected an error for zero interval, got nil")
	}

	if s != nil {
		t.Fatal("expected a nil scheduler for zero interval")
	}
}

func TestNewNegativeInterval(t *testing.T) {
	task := func() {}

	s, err := New(-time.Hour, task)

	if err == nil {
		t.Fatal("expected an error for negative interval, got nil")
	}

	if s != nil {
		t.Fatal("expected a nil scheduler for negative interval")
	}
}

func TestNewNilTask(t *testing.T) {
	s, err := New(time.Hour, nil)

	if err == nil {
		t.Fatal("expected an error for nil task, got nil")
	}

	if s != nil {
		t.Fatal("expected a nil scheduler for nil task")
	}
}

func TestStartRunsTask(t *testing.T) {
	ran := make(chan struct{}, 1)

	task := func() {
		ran <- struct{}{}
	}

	s, err := New(10*time.Millisecond, task)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		s.Start(stop)
	}()

	select {
	case <-ran:
		close(stop)
		<-done

	case <-time.After(200 * time.Millisecond):
		close(stop)
		<-done
		t.Fatal("expected task to run, but it did not")
	}
}

func TestStartRunsTaskRepeatedly(t *testing.T) {
	ran := make(chan struct{}, 5)

	task := func() {
		ran <- struct{}{}
	}

	s, err := New(10*time.Millisecond, task)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		s.Start(stop)
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-ran:
			// One scheduled execution occurred.

		case <-time.After(200 * time.Millisecond):
			close(stop)
			<-done
			t.Fatalf("expected task execution %d, but it did not occur", i+1)
		}
	}

	close(stop)
	<-done
}

func TestStartStops(t *testing.T) {
	s, err := New(time.Hour, func() {})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		s.Start(stop)
	}()

	close(stop)

	select {
	case <-done:
		// Scheduler stopped successfully.

	case <-time.After(200 * time.Millisecond):
		t.Fatal("scheduler did not stop after stop signal")
	}
}

func TestScheduledBackup(t *testing.T) {
	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote.git")
	localPath := filepath.Join(tempDir, "local")

	// Create a temporary bare repository as the remote.
	cmd := exec.Command("git", "init", "--bare", remotePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create remote: %v\n%s", err, output)
	}

	// Create the local repository.
	if err := os.Mkdir(localPath, 0755); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "-C", localPath, "init")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to initialize local repository: %v\n%s", err, output)
	}

	// Configure Git identity for the test repository.
	for key, value := range map[string]string{
		"user.name":  "AutoGit Test",
		"user.email": "autogit-test@example.com",
	} {
		cmd = exec.Command("git", "-C", localPath, "config", key, value)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to configure Git: %v\n%s", err, output)
		}
	}

	// Connect the local repository to the temporary remote.
	cmd = exec.Command(
		"git", "-C", localPath,
		"remote", "add", "origin", remotePath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to configure remote: %v\n%s", err, output)
	}

	// Create a file that the scheduled backup should commit.
	filePath := filepath.Join(localPath, "test.txt")
	if err := os.WriteFile(filePath, []byte("scheduled backup"), 0644); err != nil {
		t.Fatal(err)
	}

	completed := make(chan struct{}, 1)
	taskErrors := make(chan error, 1)

	task := func() {
		if err := git.Backup(localPath, "scheduled backup test"); err != nil {
			select {
			case taskErrors <- err:
			default:
			}
			return
		}

		select {
		case completed <- struct{}{}:
		default:
		}
	}

	s, err := New(10*time.Millisecond, task)
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		s.Start(stop)
	}()

	select {
	case err := <-taskErrors:
		close(stop)
		<-done
		t.Fatalf("scheduled backup failed: %v", err)

	case <-completed:
		close(stop)
		<-done

	case <-time.After(3 * time.Second):
		close(stop)
		<-done
		t.Fatal("scheduled backup did not complete")
	}

	// Verify that the scheduled backup pushed a branch to the remote.
	cmd = exec.Command("git", "--git-dir", remotePath, "show-ref")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote did not receive the backup: %v\n%s", err, output)
	}

	// Verify the commit message on the remote branch.
	cmd = exec.Command("git", "--git-dir", remotePath, "log", "-1", "--pretty=%s", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to read remote commit: %v", err)
	}

	if string(output) != "scheduled backup test\n" {
		t.Errorf("unexpected remote commit message: %q", output)
	}
}

// --- RepoScheduler tests ---

func TestRepoSchedulerRunsBackup(t *testing.T) {
	called := make(chan string, 4)

	backup := func(path string) (bool, error) {
		called <- path
		return true, nil
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo-a", Interval: 10 * time.Millisecond, Policy: &FixedPolicy{}},
		{Path: "/repo-b", Interval: 10 * time.Millisecond, Policy: &FixedPolicy{}},
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// Wait for both repos to be backed up at least once.
	received := make(map[string]int)
	for len(received) < 2 {
		select {
		case path := <-called:
			received[path]++
		case <-time.After(500 * time.Millisecond):
			close(stop)
			<-done
			t.Fatalf("only received backups for %v", received)
		}
	}

	close(stop)
	<-done

	if _, ok := received["/repo-a"]; !ok {
		t.Error("expected backup for /repo-a")
	}
	if _, ok := received["/repo-b"]; !ok {
		t.Error("expected backup for /repo-b")
	}
}

func TestRepoSchedulerSkipsWhenPolicyDenies(t *testing.T) {
	called := make(chan string, 4)

	backup := func(path string) (bool, error) {
		called <- path
		return true, nil
	}

	// Smart policy with no activity — should skip after initial backup.
	policy := NewSmartPolicy(1*time.Hour, 0)
	// Prime: record a backup so the initial-backup path is closed.
	policy.RecordBackup("/repo")

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 10 * time.Millisecond, Policy: policy},
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// Wait a bit — should not receive any backup calls since policy denies.
	select {
	case <-called:
		close(stop)
		<-done
		t.Error("expected no backup when policy denies")
	case <-time.After(100 * time.Millisecond):
		// Expected: no backup called.
	}

	close(stop)
	<-done
}

func TestRepoSchedulerRecordsActivity(t *testing.T) {
	called := make(chan string, 4)

	backup := func(path string) (bool, error) {
		called <- path
		// Return hasChanges=true so the policy records activity.
		return true, nil
	}

	policy := NewSmartPolicy(1*time.Hour, 0)

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 10 * time.Millisecond, Policy: policy},
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// First tick: initial backup allowed.
	select {
	case <-called:
		// OK
	case <-time.After(500 * time.Millisecond):
		close(stop)
		<-done
		t.Fatal("expected initial backup")
	}

	// Second tick: should be allowed because activity was recorded (hasChanges=true).
	select {
	case <-called:
		// OK — activity was recorded
	case <-time.After(500 * time.Millisecond):
		close(stop)
		<-done
		t.Error("expected second backup after activity recorded")
	}

	close(stop)
	<-done
}

func TestRepoSchedulerTrigger(t *testing.T) {
	called := make(chan string, 4)

	backup := func(path string) (bool, error) {
		called <- path
		return true, nil
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 1 * time.Hour, Policy: &FixedPolicy{}},
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// Give the scheduler a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Trigger a manual backup.
	if !rs.Trigger("/repo") {
		close(stop)
		<-done
		t.Fatal("trigger returned false")
	}

	select {
	case path := <-called:
		if path != "/repo" {
			close(stop)
			<-done
			t.Errorf("expected trigger for /repo, got %s", path)
		}
	case <-time.After(500 * time.Millisecond):
		close(stop)
		<-done
		t.Fatal("trigger did not cause backup")
	}

	close(stop)
	<-done
}

func TestRepoSchedulerTriggerBypassesPolicy(t *testing.T) {
	called := make(chan string, 4)

	backup := func(path string) (bool, error) {
		called <- path
		return true, nil
	}

	// Smart policy with no activity — should skip scheduled ticks.
	policy := NewSmartPolicy(1*time.Hour, 0)
	policy.RecordBackup("/repo")

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 1 * time.Hour, Policy: policy},
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	time.Sleep(50 * time.Millisecond)

	// Trigger should bypass the policy and still run.
	if !rs.Trigger("/repo") {
		close(stop)
		<-done
		t.Fatal("trigger returned false")
	}

	select {
	case <-called:
		// OK — trigger bypassed policy
	case <-time.After(500 * time.Millisecond):
		close(stop)
		<-done
		t.Fatal("trigger did not bypass policy")
	}

	close(stop)
	<-done
}

func TestRepoSchedulerStops(t *testing.T) {
	backup := func(path string) (bool, error) {
		return false, nil
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 1 * time.Hour, Policy: &FixedPolicy{}},
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	close(stop)

	select {
	case <-done:
		// OK
	case <-time.After(500 * time.Millisecond):
		t.Fatal("repo scheduler did not stop")
	}
}

func TestRepoSchedulerNilPolicy(t *testing.T) {
	called := make(chan string, 4)

	backup := func(path string) (bool, error) {
		called <- path
		return true, nil
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 10 * time.Millisecond, Policy: nil},
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	select {
	case <-called:
		// OK — nil policy should allow backup
	case <-time.After(500 * time.Millisecond):
		close(stop)
		<-done
		t.Fatal("expected backup with nil policy")
	}

	close(stop)
	<-done
}

func TestRepoSchedulerErrorSkipsRecording(t *testing.T) {
	callCount := 0

	backup := func(path string) (bool, error) {
		callCount++
		return false, fmt.Errorf("simulated error")
	}

	policy := NewSmartPolicy(1*time.Hour, 0)

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 10 * time.Millisecond, Policy: policy},
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// Let a few ticks pass — they should all fail but not crash.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	<-done

	if callCount < 3 {
		t.Errorf("expected at least 3 backup attempts, got %d", callCount)
	}
}

// mockPowerProvider returns a configurable state for testing.
type mockPowerProvider struct {
	state power.State
}

func (m *mockPowerProvider) GetState() (power.State, error) {
	return m.state, nil
}

func TestRepoSchedulerLowBatterySkipsAutoBackup(t *testing.T) {
	var callCount atomic.Int32

	backup := func(path string) (bool, error) {
		callCount.Add(1)
		return false, nil
	}

	// Provider always reports 10% battery (discharging).
	provider := &mockPowerProvider{
		state: power.State{PowerType: power.PowerTypeDischarging, Battery: 10},
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 10 * time.Millisecond, Policy: nil},
	})
	rs.SetPowerConfig(provider, PowerConfig{
		LowBatteryPercent: 20,
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// Let a few ticks pass — automatic backups should be skipped.
	time.Sleep(80 * time.Millisecond)
	close(stop)
	<-done

	if got := callCount.Load(); got != 0 {
		t.Errorf("expected 0 automatic backups on low battery, got %d", got)
	}
}

func TestRepoSchedulerLowBatteryAllowsManualTrigger(t *testing.T) {
	var callCount atomic.Int32

	backup := func(path string) (bool, error) {
		callCount.Add(1)
		return false, nil
	}

	// Provider always reports 10% battery (discharging).
	provider := &mockPowerProvider{
		state: power.State{PowerType: power.PowerTypeDischarging, Battery: 10},
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 1 * time.Hour, Policy: nil},
	})
	rs.SetPowerConfig(provider, PowerConfig{
		LowBatteryPercent: 20,
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	time.Sleep(50 * time.Millisecond)

	// Manual trigger should bypass the low-battery guard.
	if !rs.Trigger("/repo") {
		close(stop)
		<-done
		t.Fatal("trigger returned false")
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-done

	if got := callCount.Load(); got != 1 {
		t.Errorf("expected 1 manual backup on low battery, got %d", got)
	}
}

func TestRepoSchedulerBatteryInterval(t *testing.T) {
	var callCount atomic.Int32

	backup := func(path string) (bool, error) {
		callCount.Add(1)
		return false, nil
	}

	// Provider always reports discharging (on battery).
	provider := &mockPowerProvider{
		state: power.State{PowerType: power.PowerTypeDischarging, Battery: 50},
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 10 * time.Millisecond, Policy: nil},
	})
	rs.SetPowerConfig(provider, PowerConfig{
		BatteryInterval: 500 * time.Millisecond,
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// With battery interval of 500ms, we should see fewer ticks than with 10ms.
	time.Sleep(600 * time.Millisecond)
	close(stop)
	<-done

	got := callCount.Load()
	// With 500ms battery interval and 600ms run time, expect 1-2 calls.
	// Without battery interval (10ms), we'd see ~60 calls.
	if got < 1 || got > 3 {
		t.Errorf("expected 1-3 backups with battery interval, got %d", got)
	}
}

func TestRepoSchedulerACPowerUsesNormalInterval(t *testing.T) {
	var callCount atomic.Int32

	backup := func(path string) (bool, error) {
		callCount.Add(1)
		return false, nil
	}

	// Provider always reports AC power.
	provider := &mockPowerProvider{
		state: power.State{PowerType: power.PowerTypeAC, Battery: -1},
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 10 * time.Millisecond, Policy: nil},
	})
	rs.SetPowerConfig(provider, PowerConfig{
		BatteryInterval: 500 * time.Millisecond,
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// With AC power, the normal 10ms interval should be used.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	<-done

	got := callCount.Load()
	// With 10ms interval and 100ms run time, expect ~10 calls.
	if got < 5 {
		t.Errorf("expected at least 5 backups on AC power, got %d", got)
	}
}

func TestRepoSchedulerPowerTransition(t *testing.T) {
	var callCount atomic.Int32

	backup := func(path string) (bool, error) {
		callCount.Add(1)
		return false, nil
	}

	// Start on AC, then switch to battery.
	provider := &mockPowerProvider{
		state: power.State{PowerType: power.PowerTypeAC, Battery: -1},
	}

	rs := NewRepoScheduler([]RepoEntry{
		{Path: "/repo", Interval: 10 * time.Millisecond, Policy: nil},
	})
	rs.SetPowerConfig(provider, PowerConfig{
		BatteryInterval: 200 * time.Millisecond,
	})

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		rs.Start(stop, backup)
	}()

	// Let a few AC ticks pass.
	time.Sleep(60 * time.Millisecond)
	acCalls := callCount.Load()

	// Switch to battery — interval should slow down.
	provider.state = power.State{PowerType: power.PowerTypeDischarging, Battery: 50}

	// Wait for more ticks with the slower battery interval.
	time.Sleep(300 * time.Millisecond)
	batteryCalls := callCount.Load() - acCalls

	close(stop)
	<-done

	// On AC (10ms interval), we should have seen several calls.
	if acCalls < 3 {
		t.Errorf("expected at least 3 AC calls, got %d", acCalls)
	}
	// On battery (200ms interval), we should have seen fewer calls.
	if batteryCalls < 1 || batteryCalls > 3 {
		t.Errorf("expected 1-3 battery calls after transition, got %d", batteryCalls)
	}
}

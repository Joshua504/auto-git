package scheduler

import (
	"testing"
	"time"
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

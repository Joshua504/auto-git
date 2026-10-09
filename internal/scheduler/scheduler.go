package scheduler

import (
	"fmt"
	"time"
)

type Scheduler struct {
	interval time.Duration
	task     func()
}

func New(interval time.Duration, task func()) (*Scheduler, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("interval must be greater than zero")
	}

	if task == nil {
		return nil, fmt.Errorf("task cannot be nil")
	}

	return &Scheduler{
		interval: interval,
		task:     task,
	}, nil
}

func (s *Scheduler) Start(stop <-chan struct{}) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.task()

		case <-stop:
			return
		}
	}
}

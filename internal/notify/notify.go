// Package notify sends desktop notifications for AutoGit events.
//
// It wraps the beeep library so that notifications are optional and can be
// toggled via the configuration file. When disabled the calls become no-ops.
package notify

import (
	"github.com/gen2brain/beeep"
)

func init() {
	beeep.AppName = "AutoGit"
}

// Sender controls whether desktop notifications are delivered.
type Sender struct {
	enabled bool
}

// New returns a Sender that delivers notifications when enabled is true.
func New(enabled bool) Sender {
	return Sender{enabled: enabled}
}

// ShouldNotify reports whether the sender will actually post notifications.
func (s Sender) ShouldNotify() bool {
	return s.enabled
}

// Success posts an informational notification and plays an audible alert for a
// completed action.
func (s Sender) Success(title, message string) {
	if !s.enabled {
		return
	}

	_ = beeep.Alert(title, message, "")
}

// Warning posts a notification and plays an audible alert for a non-fatal
// issue that still requires attention (skipped repo, diverged branch, etc.).
func (s Sender) Warning(title, message string) {
	if !s.enabled {
		return
	}

	_ = beeep.Alert(title, message, "")
}

// Error posts a notification and plays an audible alert for a failure.
func (s Sender) Error(title, message string) {
	if !s.enabled {
		return
	}

	_ = beeep.Alert(title, message, "")
}

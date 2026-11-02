package notify

import "testing"

func TestSender_ShouldNotify(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    bool
	}{
		{"disabled", false, false},
		{"enabled", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(tt.enabled)
			if got := s.ShouldNotify(); got != tt.want {
				t.Errorf("ShouldNotify() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSender_NoPanic verifies that calling notification methods on a disabled
// sender does not panic — it should simply return without doing anything.
func TestSender_NoPanic(t *testing.T) {
	s := New(false)

	// These should all be no-ops and never panic.
	s.Success("title", "message")
	s.Warning("title", "message")
	s.Error("title", "message")
}

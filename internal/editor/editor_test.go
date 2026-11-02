package editor

import "testing"

func TestIsRunning(t *testing.T) {
	// This test is useful only when an editor is actually open.
	// In CI or when no editor is running, we skip it.
	if !IsRunning([]string{"goland", "code", "nvim", "vim"}) {
		t.Skip("no editor is running; skipping")
	}
}

func TestIsRunningWithNoMatchingEditor(t *testing.T) {
	if IsRunning([]string{"autogit-no-such-editor"}) {
		t.Fatal("expected no matching editor to be detected")
	}
}

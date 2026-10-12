package editor

import "testing"

func TestIsRunning(t *testing.T) {
	if !IsRunning([]string{"goland"}) {
		t.Fatal("expected IsRunning to detect GoLand")
	}
}

func TestIsRunningWithNoMatchingEditor(t *testing.T) {
	if IsRunning([]string{"autogit-no-such-editor"}) {
		t.Fatal("expected no matching editor to be detected")
	}
}

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsRepository(t *testing.T) {
	tempDir := t.TempDir()

	gitDir := filepath.Join(tempDir, "git-repo")
	nonGitDir := filepath.Join(tempDir, "not-a-repo")

	if err := os.Mkdir(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(nonGitDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "-C", gitDir, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	if !IsRepository(gitDir) {
		t.Error("expected git-repo to be recognized as a Git repository")
	}

	if IsRepository(nonGitDir) {
		t.Error("expected non-git directory to not be recognized as a Git repository")
	}
}

func TestHasChanges(t *testing.T) {
	tempDir := t.TempDir()

	cmd := exec.Command("git", "-C", tempDir, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// A newly initialized repository should be clean.
	if HasChanges(tempDir) {
		t.Error("expected new repository to have no changes")
	}

	// Create an untracked file.
	filePath := filepath.Join(tempDir, "test.txt")

	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// The untracked file should be detected.
	if !HasChanges(tempDir) {
		t.Error("expected repository to have changes after creating a file")
	}
}

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

func TestAdd(t *testing.T) {
	tempDir := t.TempDir()

	cmd := exec.Command("git", "-C", tempDir, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(tempDir, "test.txt")

	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Add(tempDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	cmd = exec.Command("git", "-C", tempDir, "diff", "--cached", "--name-only")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	if string(output) != "test.txt\n" {
		t.Errorf("expected test.txt to be staged, got %q", string(output))
	}
}

func TestCommit(t *testing.T) {
	tempDir := t.TempDir()

	cmd := exec.Command("git", "-C", tempDir, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure Git identity for the temporary repository.
	if err := exec.Command(
		"git", "-C", tempDir,
		"config", "user.name", "AutoGit Test",
	).Run(); err != nil {
		t.Fatal(err)
	}

	if err := exec.Command(
		"git", "-C", tempDir,
		"config", "user.email", "autogit-test@example.com",
	).Run(); err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(tempDir, "test.txt")

	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Add(tempDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	message := "test commit"

	if err := Commit(tempDir, message); err != nil {
		t.Fatalf("Commit() failed: %v", err)
	}

	cmd = exec.Command("git", "-C", tempDir, "log", "-1", "--pretty=%s")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	if string(output) != message+"\n" {
		t.Errorf("expected commit message %q, got %q", message, string(output))
	}
}

func TestPush(t *testing.T) {
	tempDir := t.TempDir()

	remotePath := filepath.Join(tempDir, "remote.git")
	localPath := filepath.Join(tempDir, "local")

	// Create a bare repository to act as the remote.
	cmd := exec.Command("git", "init", "--bare", remotePath)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create the local repository.
	if err := os.Mkdir(localPath, 0755); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "-C", localPath, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure Git identity for the temporary repository.
	if err := exec.Command(
		"git", "-C", localPath,
		"config", "user.name", "AutoGit Test",
	).Run(); err != nil {
		t.Fatal(err)
	}

	if err := exec.Command(
		"git", "-C", localPath,
		"config", "user.email", "autogit-test@example.com",
	).Run(); err != nil {
		t.Fatal(err)
	}

	// Add the local bare repository as the remote.
	cmd = exec.Command(
		"git", "-C", localPath,
		"remote", "add", "origin", remotePath,
	)

	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create a file.
	filePath := filepath.Join(localPath, "test.txt")

	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Stage and commit the file.
	if err := Add(localPath); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	if err := Commit(localPath, "test push"); err != nil {
		t.Fatalf("Commit() failed: %v", err)
	}

	// Push to the local remote.
	if err := Push(localPath); err != nil {
		t.Fatalf("Push() failed: %v", err)
	}

	// Verify that the remote received the branch.
	cmd = exec.Command(
		"git", "--git-dir", remotePath,
		"show-ref",
	)

	if err := cmd.Run(); err != nil {
		t.Fatal("expected remote repository to contain the pushed branch")
	}
}

func TestBackup(t *testing.T) {
	tempDir := t.TempDir()

	remotePath := filepath.Join(tempDir, "remote.git")
	localPath := filepath.Join(tempDir, "local")

	// Create a bare repository to act as the remote.
	cmd := exec.Command("git", "init", "--bare", remotePath)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create the local repository.
	if err := os.Mkdir(localPath, 0755); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "-C", localPath, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure Git identity for the temporary repository.
	if err := exec.Command(
		"git", "-C", localPath,
		"config", "user.name", "AutoGit Test",
	).Run(); err != nil {
		t.Fatal(err)
	}

	if err := exec.Command(
		"git", "-C", localPath,
		"config", "user.email", "autogit-test@example.com",
	).Run(); err != nil {
		t.Fatal(err)
	}

	// Add the remote.
	cmd = exec.Command(
		"git", "-C", localPath,
		"remote", "add", "origin", remotePath,
	)

	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create a file so there are changes to back up.
	filePath := filepath.Join(localPath, "test.txt")

	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Run the complete backup.
	message := "auto backup test"

	if err := Backup(localPath, message); err != nil {
		t.Fatalf("Backup() failed: %v", err)
	}

	// Verify that the commit exists locally.
	cmd = exec.Command(
		"git", "-C", localPath,
		"log", "-1", "--pretty=%s",
	)

	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	if string(output) != message+"\n" {
		t.Errorf("expected commit message %q, got %q", message, string(output))
	}

	// Verify that the remote received the commit.
	cmd = exec.Command(
		"git", "--git-dir", remotePath,
		"show-ref",
	)

	if err := cmd.Run(); err != nil {
		t.Fatal("expected remote repository to contain the pushed commit")
	}
}

func TestBackupNoChanges(t *testing.T) {
	tempDir := t.TempDir()

	remotePath := filepath.Join(tempDir, "remote.git")
	localPath := filepath.Join(tempDir, "local")

	// Create a bare repository to act as the remote.
	cmd := exec.Command("git", "init", "--bare", remotePath)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create the local repository.
	if err := os.Mkdir(localPath, 0755); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "-C", localPath, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure Git identity.
	if err := exec.Command(
		"git", "-C", localPath,
		"config", "user.name", "AutoGit Test",
	).Run(); err != nil {
		t.Fatal(err)
	}

	if err := exec.Command(
		"git", "-C", localPath,
		"config", "user.email", "autogit-test@example.com",
	).Run(); err != nil {
		t.Fatal(err)
	}

	// Add the remote.
	cmd = exec.Command(
		"git", "-C", localPath,
		"remote", "add", "origin", remotePath,
	)

	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create the initial commit.
	filePath := filepath.Join(localPath, "test.txt")

	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Add(localPath); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	if err := Commit(localPath, "initial commit"); err != nil {
		t.Fatalf("Commit() failed: %v", err)
	}

	if err := Push(localPath); err != nil {
		t.Fatalf("Push() failed: %v", err)
	}

	// The repository is now clean.
	if HasChanges(localPath) {
		t.Fatal("expected repository to be clean")
	}

	// Run Backup with no changes.
	if err := Backup(localPath, "should not be committed"); err != nil {
		t.Fatalf("Backup() failed: %v", err)
	}

	// Verify that no new commit was created.
	cmd = exec.Command(
		"git", "-C", localPath,
		"log", "-2", "--pretty=%s",
	)

	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	expected := "initial commit\n"

	if string(output) != expected {
		t.Errorf("expected no new commit, got:\n%s", output)
	}
}

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckHealthNonExistentPath(t *testing.T) {
	result := CheckHealth("/nonexistent/path/that/does/not/exist")

	if result.Exists {
		t.Error("expected Exists to be false for non-existent path")
	}

	if result.Status != Unhealthy {
		t.Errorf("expected status Unhealthy, got %s", result.Status)
	}

	if len(result.Issues) == 0 {
		t.Error("expected at least one issue for non-existent path")
	}
}

func TestCheckHealthNotGitRepository(t *testing.T) {
	tempDir := t.TempDir()

	result := CheckHealth(tempDir)

	if !result.Exists {
		t.Error("expected Exists to be true for existing directory")
	}

	if result.IsGitRepo {
		t.Error("expected IsGitRepo to be false for non-Git directory")
	}

	if result.Status != Unhealthy {
		t.Errorf("expected status Unhealthy, got %s", result.Status)
	}

	if !containsString(result.Issues, "not a valid Git repository") {
		t.Errorf("expected issue about not being a Git repository, got: %v", result.Issues)
	}
}

func TestCheckHealthNoRemote(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize a Git repository.
	cmd := exec.Command("git", "-C", tempDir, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure identity.
	if err := exec.Command("git", "-C", tempDir, "config", "user.name", "Test").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", tempDir, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatal(err)
	}

	result := CheckHealth(tempDir)

	if !result.IsGitRepo {
		t.Error("expected IsGitRepo to be true")
	}

	if result.HasRemote {
		t.Error("expected HasRemote to be false")
	}

	if result.Status != Degraded {
		t.Errorf("expected status Degraded for repo without remote, got %s", result.Status)
	}

	if !containsString(result.Issues, "no remote configured") {
		t.Errorf("expected issue about no remote, got: %v", result.Issues)
	}
}

func TestCheckHealthWithLocalRemote(t *testing.T) {
	tempDir := t.TempDir()

	remotePath := filepath.Join(tempDir, "remote.git")
	localPath := filepath.Join(tempDir, "local")

	// Create a bare remote.
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

	// Configure identity.
	if err := exec.Command("git", "-C", localPath, "config", "user.name", "Test").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", localPath, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatal(err)
	}

	// Add the remote.
	cmd = exec.Command("git", "-C", localPath, "remote", "add", "origin", remotePath)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create an initial commit.
	filePath := filepath.Join(localPath, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Add(localPath); err != nil {
		t.Fatal(err)
	}
	if err := Commit(localPath, "initial commit"); err != nil {
		t.Fatal(err)
	}
	if err := Push(localPath); err != nil {
		t.Fatal(err)
	}

	result := CheckHealth(localPath)

	if !result.IsGitRepo {
		t.Error("expected IsGitRepo to be true")
	}

	if !result.HasRemote {
		t.Error("expected HasRemote to be true")
	}

	if result.RemoteName != "origin" {
		t.Errorf("expected RemoteName to be 'origin', got %q", result.RemoteName)
	}

	if result.RemoteURL != remotePath {
		t.Errorf("expected RemoteURL to be %q, got %q", remotePath, result.RemoteURL)
	}

	if !result.RemoteReachable {
		t.Error("expected RemoteReachable to be true for local remote")
	}

	if !result.AuthOK {
		t.Error("expected AuthOK to be true for local remote")
	}

	if result.Status != Healthy {
		t.Errorf("expected status Healthy, got %s", result.Status)
	}

	if result.Branch != "master" && result.Branch != "main" {
		t.Errorf("expected Branch to be 'master' or 'main', got %q", result.Branch)
	}
}

func TestCheckHealthBehindRemote(t *testing.T) {
	tempDir := t.TempDir()

	remotePath := filepath.Join(tempDir, "remote.git")
	localPath := filepath.Join(tempDir, "local")

	// Create a bare remote.
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

	// Configure identity.
	if err := exec.Command("git", "-C", localPath, "config", "user.name", "Test").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", localPath, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatal(err)
	}

	// Add the remote.
	cmd = exec.Command("git", "-C", localPath, "remote", "add", "origin", remotePath)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create an initial commit and push.
	filePath := filepath.Join(localPath, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Add(localPath); err != nil {
		t.Fatal(err)
	}
	if err := Commit(localPath, "initial commit"); err != nil {
		t.Fatal(err)
	}
	if err := Push(localPath); err != nil {
		t.Fatal(err)
	}

	// Add a commit directly to the remote (simulating another machine pushing).
	clonePath := filepath.Join(tempDir, "clone")
	cmd = exec.Command("git", "clone", remotePath, clonePath)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	if err := exec.Command("git", "-C", clonePath, "config", "user.name", "Test").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", clonePath, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatal(err)
	}

	cloneFile := filepath.Join(clonePath, "remote.txt")
	if err := os.WriteFile(cloneFile, []byte("from remote"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "-C", clonePath, "add", ".")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "-C", clonePath, "commit", "-m", "remote commit")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "-C", clonePath, "push")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Now the local repo is behind.
	result := CheckHealth(localPath)

	if result.BehindCount != 1 {
		t.Errorf("expected BehindCount to be 1, got %d", result.BehindCount)
	}

	// Should have a warning about being behind.
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "behind remote") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about being behind remote, got: %v", result.Warnings)
	}
}

func TestCheckHealthAheadOfRemote(t *testing.T) {
	tempDir := t.TempDir()

	remotePath := filepath.Join(tempDir, "remote.git")
	localPath := filepath.Join(tempDir, "local")

	// Create a bare remote.
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

	// Configure identity.
	if err := exec.Command("git", "-C", localPath, "config", "user.name", "Test").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", localPath, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatal(err)
	}

	// Add the remote.
	cmd = exec.Command("git", "-C", localPath, "remote", "add", "origin", remotePath)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create an initial commit and push.
	filePath := filepath.Join(localPath, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Add(localPath); err != nil {
		t.Fatal(err)
	}
	if err := Commit(localPath, "initial commit"); err != nil {
		t.Fatal(err)
	}
	if err := Push(localPath); err != nil {
		t.Fatal(err)
	}

	// Add a local commit without pushing.
	filePath2 := filepath.Join(localPath, "local.txt")
	if err := os.WriteFile(filePath2, []byte("local only"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Add(localPath); err != nil {
		t.Fatal(err)
	}
	if err := Commit(localPath, "local commit"); err != nil {
		t.Fatal(err)
	}

	result := CheckHealth(localPath)

	if result.AheadCount != 1 {
		t.Errorf("expected AheadCount to be 1, got %d", result.AheadCount)
	}

	// Should have a warning about being ahead.
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "ahead of remote") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about being ahead of remote, got: %v", result.Warnings)
	}
}

func TestCheckAllHealth(t *testing.T) {
	tempDir := t.TempDir()

	// Create a valid repo.
	validPath := filepath.Join(tempDir, "valid")
	if err := os.Mkdir(validPath, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", validPath, "init")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	paths := []string{
		"/nonexistent/path",
		tempDir,          // not a git repo
		validPath,        // valid git repo, no remote
	}

	results := CheckAllHealth(paths)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	if results[0].Status != Unhealthy {
		t.Errorf("expected first result to be Unhealthy, got %s", results[0].Status)
	}

	if results[1].Status != Unhealthy {
		t.Errorf("expected second result to be Unhealthy, got %s", results[1].Status)
	}

	if results[2].Status != Degraded {
		t.Errorf("expected third result to be Degraded (no remote), got %s", results[2].Status)
	}
}

func TestHealthStatusString(t *testing.T) {
	tests := []struct {
		status HealthStatus
		want   string
	}{
		{Healthy, "healthy"},
		{Degraded, "degraded"},
		{Unhealthy, "unhealthy"},
		{HealthStatus(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("HealthStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func containsString(slice []string, substr string) bool {
	for _, s := range slice {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

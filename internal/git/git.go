package git

import (
	"fmt"
	"os/exec"
)

func IsRepository(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
	err := cmd.Run()

	return err == nil
}

func HasChanges(path string) bool {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	return len(output) > 0
}

func Add(path string) error {
	cmd := exec.Command("git", "-C", path, "add", ".")

	return cmd.Run()
}

func Commit(path, message string) error {
	cmd := exec.Command("git", "-C", path, "commit", "-m", message)

	return cmd.Run()
}

func Push(path string) error {
	cmd := exec.Command("git", "-C", path, "push", "-u", "origin", "HEAD")

	return cmd.Run()
}

func Backup(path, message string) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	if !HasChanges(path) {
		return nil
	}

	if err := Add(path); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	if err := Commit(path, message); err != nil {
		return fmt.Errorf("git commit failed: %w", err)
	}

	if err := Push(path); err != nil {
		return fmt.Errorf("git push failed: %w", err)
	}

	return nil
}

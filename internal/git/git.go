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

func HasChanges(path string) (bool, error) {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}

	return len(output) > 0, nil
}

func Add(path string) error {
	cmd := exec.Command("git", "-C", path, "add", ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add failed: %w: %s", err, output)
	}

	return nil
}

func Commit(path, message string) error {
	cmd := exec.Command("git", "-C", path, "commit", "-m", message)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w: %s", err, output)
	}

	return nil
}

func Push(path string) error {
	cmd := exec.Command("git", "-C", path, "push", "-u", "origin", "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %w: %s", err, output)
	}

	return nil
}

func Backup(path, message string) error {
	if !IsRepository(path) {
		return fmt.Errorf("%s is not a Git repository", path)
	}

	hasChanges, err := HasChanges(path)
	if err != nil {
		return fmt.Errorf("checking repository changes: %w", err)
	}

	if hasChanges {
		if err := Add(path); err != nil {
			return err
		}

		if err := Commit(path, message); err != nil {
			return err
		}
	}

	if err := Push(path); err != nil {
		return err
	}

	return nil
}

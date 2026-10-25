package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the name of the configuration file AutoGit looks for.
const FileName = "config.yaml"

// Locate decides which configuration file to load.
//
// An explicit path (the --config flag) always wins and must exist. Otherwise
// AutoGit uses the first of these that exists:
//
//  1. config.yaml in the current directory
//  2. autogit/config.yaml inside the user's configuration directory
//     (~/.config/autogit/config.yaml on Linux)
//
// The second location lets AutoGit start from any directory, for example from
// a desktop autostart entry or a service, where the working directory is
// not the project folder.
func Locate(explicit string) (string, error) {
	candidates := []string{FileName}

	if dir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "autogit", FileName))
	}

	return locate(explicit, candidates)
}

func locate(explicit string, candidates []string) (string, error) {
	if explicit != "" {
		if err := checkFile(explicit); err != nil {
			return "", fmt.Errorf("configuration file %q: %w", explicit, err)
		}

		return explicit, nil
	}

	for _, candidate := range candidates {
		if checkFile(candidate) == nil {
			return candidate, nil
		}
	}

	searched := make([]string, 0, len(candidates))

	for _, candidate := range candidates {
		if abs, err := filepath.Abs(candidate); err == nil {
			candidate = abs
		}

		searched = append(searched, candidate)
	}

	return "", fmt.Errorf(
		"no configuration file found (looked in: %s); copy config.example.yaml to one of those locations or pass --config <path>",
		strings.Join(searched, ", "))
}

// checkFile reports whether path is an existing regular file.
func checkFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return errors.New("is a directory, not a file")
	}

	return nil
}

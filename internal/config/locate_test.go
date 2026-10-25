package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, path string) string {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("interval: \"1m\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	return path
}

// chdir moves the test into dir and moves back afterwards.
func chdir(t *testing.T, dir string) {
	t.Helper()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func TestLocateExplicitPathWins(t *testing.T) {
	dir := t.TempDir()
	explicit := touch(t, filepath.Join(dir, "mine.yaml"))
	other := touch(t, filepath.Join(dir, "other.yaml"))

	got, err := locate(explicit, []string{other})
	if err != nil {
		t.Fatal(err)
	}

	if got != explicit {
		t.Fatalf("expected the explicit path %q, got %q", explicit, got)
	}
}

func TestLocateExplicitPathMustExist(t *testing.T) {
	dir := t.TempDir()
	fallback := touch(t, filepath.Join(dir, "config.yaml"))
	missing := filepath.Join(dir, "missing.yaml")

	// An explicit path that does not exist is an error, not a silent fallback
	// to some other file the user did not ask for.
	_, err := locate(missing, []string{fallback})
	if err == nil {
		t.Fatal("expected an error for a missing explicit path")
	}

	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("expected the error to name the path, got: %v", err)
	}
}

func TestLocateExplicitPathCannotBeADirectory(t *testing.T) {
	_, err := locate(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("expected a directory error, got: %v", err)
	}
}

func TestLocateFirstCandidateWins(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, filepath.Join(dir, "first", "config.yaml"))
	second := touch(t, filepath.Join(dir, "second", "config.yaml"))

	got, err := locate("", []string{first, second})
	if err != nil || got != first {
		t.Fatalf("expected %q, got %q (err %v)", first, got, err)
	}
}

func TestLocateFallsBackToLaterCandidate(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "first", "config.yaml")
	second := touch(t, filepath.Join(dir, "second", "config.yaml"))

	got, err := locate("", []string{missing, second})
	if err != nil || got != second {
		t.Fatalf("expected %q, got %q (err %v)", second, got, err)
	}
}

func TestLocateSkipsDirectoriesNamedLikeTheConfig(t *testing.T) {
	dir := t.TempDir()

	if err := os.Mkdir(filepath.Join(dir, "config.yaml"), 0755); err != nil {
		t.Fatal(err)
	}

	real := touch(t, filepath.Join(dir, "real", "config.yaml"))

	got, err := locate("", []string{filepath.Join(dir, "config.yaml"), real})
	if err != nil || got != real {
		t.Fatalf("expected %q, got %q (err %v)", real, got, err)
	}
}

func TestLocateNothingFoundExplainsWhatToDo(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a", "config.yaml")
	b := filepath.Join(dir, "b", "config.yaml")

	_, err := locate("", []string{a, b})
	if err == nil {
		t.Fatal("expected an error when no configuration file exists")
	}

	for _, want := range []string{a, b, "config.example.yaml", "--config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}
}

func TestLocateChecksWorkingDirectoryThenUserConfigDir(t *testing.T) {
	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config directory on this system: %v", err)
	}

	installed := touch(t, filepath.Join(userConfigDir, "autogit", FileName))

	work := t.TempDir()
	chdir(t, work)

	// Only the installed copy exists.
	got, err := Locate("")
	if err != nil || got != installed {
		t.Fatalf("expected the user config copy %q, got %q (err %v)", installed, got, err)
	}

	// A config.yaml in the working directory takes priority.
	touch(t, filepath.Join(work, FileName))

	got, err = Locate("")
	if err != nil || got != FileName {
		t.Fatalf("expected the working directory copy %q, got %q (err %v)", FileName, got, err)
	}
}

// The example file is what new users copy, so it must always be loadable.
func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml cannot be loaded: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config.example.yaml is not a valid configuration: %v", err)
	}

	for _, repository := range cfg.Repositories {
		if strings.Contains(repository.Path, "/home/") {
			t.Fatalf("the example must not contain a personal path: %q", repository.Path)
		}
	}
}

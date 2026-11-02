package git

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixedClock makes {date}, {time} and {datetime} predictable.
func fixedClock(t *testing.T) {
	t.Helper()

	old := now
	now = func() time.Time { return time.Date(2026, 10, 26, 14, 3, 7, 0, time.Local) }

	t.Cleanup(func() { now = old })
}

func lastSubject(t *testing.T, dir, ref string) string {
	t.Helper()

	return mustGit(t, dir, "log", "-1", "--format=%s", ref)
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

func TestSummarizeChanges(t *testing.T) {
	change := func(status string, paths ...string) []fileChange {
		var changes []fileChange
		for _, p := range paths {
			changes = append(changes, fileChange{Status: status, Path: p})
		}

		return changes
	}

	tests := []struct {
		name    string
		changes []fileChange
		want    string
	}{
		{"one update", change("M", "cmd/main.go"), "update main.go"},
		{"one new file", change("A", "notes.txt"), "add notes.txt"},
		{"one deletion", change("D", "old.txt"), "delete old.txt"},
		{"a type change counts as an update", change("T", "link"), "update link"},
		{"mixed actions are each named", append(change("M", "a.go"), change("A", "b.go")...), "update a.go, add b.go"},
		{"a shared action is said once", change("A", "a.go", "b.go"), "add a.go, b.go"},
		{"mixed actions with extras", append(change("D", "a.go", "b.go"), change("A", "c.go", "d.go")...), "delete a.go, delete b.go, add c.go and 1 more"},
		{"three files", change("M", "a.go", "b.go", "c.go"), "update a.go, b.go, c.go"},
		{"more than three", change("M", "a.go", "b.go", "c.go", "d.go", "e.go"), "update a.go, b.go, c.go and 2 more"},
		{"only the file name is used, not the folder", change("M", "internal/deep/er/file.go"), "update file.go"},
		{"names are dropped when the line would be too long",
			change("M", strings.Repeat("x", 25)+".go", strings.Repeat("y", 25)+".go", strings.Repeat("z", 25)+".go"),
			"update " + strings.Repeat("x", 25) + ".go and 2 more"},
		{"a single huge name falls back to a count", change("M", strings.Repeat("w", 80)+".go"), "1 files changed"},
		{"nothing listed", nil, "update files"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeChanges(tt.changes)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}

			if len(got) > maxSummaryLength && !strings.HasSuffix(got, "files changed") {
				t.Fatalf("summary is %d characters, over the %d limit", len(got), maxSummaryLength)
			}
		})
	}
}

func TestParseNameStatus(t *testing.T) {
	got := parseNameStatus("M\x00main.go\x00A\x00dir/new file.txt\x00D\x00gone.txt\x00")
	want := []fileChange{{"M", "main.go"}, {"A", "dir/new file.txt"}, {"D", "gone.txt"}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if len(parseNameStatus("")) != 0 {
		t.Fatal("empty output must give no changes")
	}
}

func TestRenderMessageFillsEveryPlaceholder(t *testing.T) {
	context := messageContext{
		Changes: []fileChange{{"M", "a.go"}, {"A", "b.go"}},
		Branch:  "feature/login",
		Host:    "my-laptop",
		Repo:    "auto-git",
		At:      time.Date(2026, 10, 26, 14, 3, 7, 0, time.UTC),
	}

	tests := map[string]string{
		"{summary}":  "update a.go, add b.go",
		"{files}":    "2",
		"{branch}":   "feature/login",
		"{host}":     "my-laptop",
		"{repo}":     "auto-git",
		"{date}":     "2026-10-26",
		"{time}":     "14:03:07",
		"{datetime}": "2026-10-26 14:03:07",
	}

	for placeholder, want := range tests {
		if got := renderMessage(placeholder, context); got != want {
			t.Errorf("%s = %q, want %q", placeholder, got, want)
		}
	}

	// Every advertised placeholder is actually handled.
	for _, name := range MessagePlaceholders() {
		if got := renderMessage("{"+name+"}", context); strings.Contains(got, "{") {
			t.Errorf("{%s} is advertised but was not filled in: %q", name, got)
		}
	}
}

func TestRenderMessageNeverExpandsFileNames(t *testing.T) {
	context := messageContext{
		Changes: []fileChange{{"A", "{host}.txt"}},
		Host:    "secret-host",
	}

	got := renderMessage("backup: {summary} on {host}", context)

	if want := "backup: add {host}.txt on secret-host"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMessageWithoutBracesIsLeftAlone(t *testing.T) {
	// No placeholders means no rendering and no Git commands, so the repository
	// path is never even looked at.
	got, err := renderCommitMessage("/does/not/exist", "chore: automatic backup", Options{}, nil)
	if err != nil || got != "chore: automatic backup" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// Commit mode
// ---------------------------------------------------------------------------

func TestCommitMessageDescribesTheChange(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, "base.txt"), "edited\n")
	writeFile(t, filepath.Join(f.local, "new.txt"), "new\n")

	if err := BackupWith(f.local, "backup: {summary}", Options{}); err != nil {
		t.Fatal(err)
	}

	if got := lastSubject(t, f.local, "HEAD"); got != "backup: update base.txt, add new.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestCommitMessagesDifferBetweenBackups(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, "first.txt"), "1\n")

	if err := BackupWith(f.local, "backup: {summary}", Options{}); err != nil {
		t.Fatal(err)
	}

	first := lastSubject(t, f.local, "HEAD")

	writeFile(t, filepath.Join(f.local, "second.txt"), "2\n")

	if err := BackupWith(f.local, "backup: {summary}", Options{}); err != nil {
		t.Fatal(err)
	}

	second := lastSubject(t, f.local, "HEAD")

	if first == second {
		t.Fatalf("both backups got the message %q", first)
	}

	if first != "backup: add first.txt" || second != "backup: add second.txt" {
		t.Fatalf("got %q and %q", first, second)
	}
}

func TestCommitMessagePlaceholders(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)
	fixedClock(t)

	writeFile(t, filepath.Join(f.local, "one.txt"), "1\n")

	template := "{branch}|{host}|{repo}|{files}|{date}|{time}|{datetime}"

	if err := BackupWith(f.local, template, Options{Machine: "testbox"}); err != nil {
		t.Fatal(err)
	}

	want := "main|testbox|local|1|2026-10-26|14:03:07|2026-10-26 14:03:07"
	if got := lastSubject(t, f.local, "HEAD"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCommitMessageNeverNamesKeptOutFiles(t *testing.T) {
	f := newSyncFixture(t)
	logs := captureLogs(t)

	writeFile(t, filepath.Join(f.local, ".env"), "TOKEN=hunter2\n")
	writeFile(t, filepath.Join(f.local, "keep.txt"), "fine\n")

	if err := BackupWith(f.local, "backup: {summary} ({files})", Options{}); err != nil {
		t.Fatal(err)
	}

	message := mustGit(t, f.local, "log", "-1", "--format=%B")

	if message != "backup: add keep.txt (1)" {
		t.Fatalf("got %q", message)
	}

	if strings.Contains(message, ".env") {
		t.Fatal("the name of a file the guard kept out appeared in a commit message")
	}

	if countLogsMentioning(*logs, ".env") != 1 {
		t.Fatal("expected the guard to still warn about .env")
	}
}

func TestCommitMessageCanHaveABody(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, "one.txt"), "1\n")

	if err := BackupWith(f.local, "backup: {summary}\n\n{files} file(s) saved on {host}", Options{Machine: "testbox"}); err != nil {
		t.Fatal(err)
	}

	if got := mustGit(t, f.local, "log", "-1", "--format=%B"); got != "backup: add one.txt\n\n1 file(s) saved on testbox" {
		t.Fatalf("got %q", got)
	}
}

func TestLiteralCommitMessageIsUnchanged(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, "one.txt"), "1\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if got := lastSubject(t, f.local, "HEAD"); got != "chore: automatic backup" {
		t.Fatalf("a message without placeholders must be used as written, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Branch mode
// ---------------------------------------------------------------------------

func TestSnapshotMessageDescribesWhatChangedSinceTheLastSnapshot(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	branchBackup := func() {
		t.Helper()

		if err := BackupWith(f.local, "backup: {summary}", branchOpts); err != nil {
			t.Fatal(err)
		}
	}

	writeFile(t, filepath.Join(f.local, "base.txt"), "edited\n")
	branchBackup()

	if got := lastSubject(t, f.remote, backupRemoteRef); got != "backup: update base.txt" {
		t.Fatalf("first snapshot: %q", got)
	}

	// The next snapshot describes only what is new, not everything since your last commit.
	writeFile(t, filepath.Join(f.local, "work.txt"), "work\n")
	branchBackup()

	if got := lastSubject(t, f.remote, backupRemoteRef); got != "backup: add work.txt" {
		t.Fatalf("second snapshot: %q", got)
	}
}

func TestSnapshotMessageInARepositoryWithNoCommits(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// A fresh repository: every file is new.
	fresh := filepath.Join(t.TempDir(), "fresh")
	mustGit(t, filepath.Dir(fresh), "init", "-b", "main", fresh)
	configureIdentity(t, fresh)
	mustGit(t, fresh, "remote", "add", "origin", f.remote)

	writeFile(t, filepath.Join(fresh, "main.go"), "package main\n")

	if err := BackupWith(fresh, "backup: {summary}", Options{Mode: ModeBranch, Machine: "testbox"}); err != nil {
		t.Fatal(err)
	}

	if got := lastSubject(t, f.remote, "refs/heads/autogit/testbox/main"); got != "backup: add main.go" {
		t.Fatalf("got %q", got)
	}
}

func TestSnapshotMessageNeverNamesKeptOutFiles(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, ".env"), "TOKEN=hunter2\n")
	writeFile(t, filepath.Join(f.local, "keep.txt"), "fine\n")

	if err := BackupWith(f.local, "backup: {summary}", branchOpts); err != nil {
		t.Fatal(err)
	}

	message := mustGit(t, f.remote, "log", "-1", "--format=%B", backupRemoteRef)

	if !strings.HasPrefix(message, "backup: add keep.txt") || strings.Contains(message, ".env") {
		t.Fatalf("got %q", message)
	}
}

func TestSnapshotMessageShowsTheRealBranchName(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	mustGit(t, f.local, "checkout", "-b", "feature/login")
	writeFile(t, filepath.Join(f.local, "login.txt"), "login\n")

	if err := BackupWith(f.local, "{branch}", branchOpts); err != nil {
		t.Fatal(err)
	}

	// The ref name has "+" in place of "/", but the message shows the branch as you know it.
	if got := lastSubject(t, f.remote, "refs/heads/autogit/testbox/feature+login"); got != "feature/login" {
		t.Fatalf("got %q", got)
	}
}

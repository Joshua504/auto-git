package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The fake secrets below are assembled at runtime so that this file does not
// contain a complete token. Otherwise AutoGit's own guard would block it.

func fakeGitHubToken() string { return "gh" + "p_" + strings.Repeat("a", 36) }

func fakePrivateKey() string {
	return "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIB\n-----END " + "RSA PRIVATE KEY-----\n"
}

func fakeAWSKey() string { return "AK" + "IA" + "ABCDEFGHIJKLMNOP" }

func fakeSolanaKeypair(count int, last int) string {
	numbers := make([]string, count)
	for i := range numbers {
		numbers[i] = fmt.Sprint(i % 200)
	}

	numbers[count-1] = fmt.Sprint(last)

	return "[" + strings.Join(numbers, ",") + "]"
}

// captureLogs replaces Logf for the duration of a test and clears the
// "already reported" memory so tests do not affect each other.
func captureLogs(t *testing.T) *[]string {
	t.Helper()

	var logs []string

	old := Logf
	Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	reportMu.Lock()
	lastReported = map[string]string{}
	reportMu.Unlock()

	t.Cleanup(func() { Logf = old })

	return &logs
}

func remoteHas(remote, name string) bool {
	return exec.Command("git", "-C", remote, "cat-file", "-e", "main:"+name).Run() == nil
}

func countLogsMentioning(logs []string, text string) int {
	count := 0

	for _, line := range logs {
		if strings.Contains(line, text) {
			count++
		}
	}

	return count
}

// ---------------------------------------------------------------------------
// Unit tests: file names, contents, allowlist
// ---------------------------------------------------------------------------

func TestCheckName(t *testing.T) {
	blocked := []string{
		".env", "config/.env.production", ".env.local", "prod.env", ".envrc",
		"server.key", "certs/tls.p12", "keystore.jks", "vault.kdbx",
		"id_rsa", "home/.ssh/config", "deploy_rsa", ".aws/credentials",
		"terraform.tfstate", "terraform.tfstate.backup", "credentials.json",
		"secrets.yaml", ".netrc", "CONFIG/.ENV",
	}

	for _, name := range blocked {
		if checkName(name) == "" {
			t.Errorf("expected %q to be blocked", name)
		}
	}

	allowed := []string{
		"main.go", "README.md", ".env.example", "config/.env.sample", "env.template",
		"id_rsa.pub", "src/keyboard.go", "cert.pem", "docs/secrets-policy.md",
		".gitignore", "Dockerfile", "monkey.go", "package.json",
	}

	for _, name := range allowed {
		if reason := checkName(name); reason != "" {
			t.Errorf("expected %q to be allowed, got %q", name, reason)
		}
	}
}

func TestScanContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		label   string // "" means nothing should be found
		line    int
	}{
		{"github token", "a\nb\ntoken = \"" + fakeGitHubToken() + "\"\n", "GitHub token", 3},
		{"private key", "x\n" + fakePrivateKey(), "private key", 2},
		{"aws key", "key=" + fakeAWSKey() + "\n", "AWS access key ID", 1},
		{"aws documentation example", "key=" + "AKIA" + "IOSFODNN7EXAMPLE" + "\n", "", 0},
		{"slack token", "t=" + "xox" + "b-1234567890-abcdefghij\n", "Slack token", 1},
		{"stripe live key", "k=" + "sk_" + "live_" + strings.Repeat("a", 24), "Stripe live key", 1},
		{"anthropic key", "k=" + "sk-" + "ant-" + strings.Repeat("a", 30), "Anthropic API key", 1},
		{"solana keypair", fakeSolanaKeypair(64, 7), "Solana keypair", 1},
		{"solana keypair with newlines", "[\n" + strings.Repeat("1,\n", 63) + "2\n]\n", "Solana keypair", 1},
		{"array with 63 numbers", fakeSolanaKeypair(63, 7), "", 0},
		{"array with a value over 255", fakeSolanaKeypair(64, 300), "", 0},
		{"ordinary go code", "package main\n\nfunc main() { println(\"hello\") }\n", "", 0},
		{"password word in prose", "Set your password in the settings page.\n", "", 0},
		{"binary data", "\x00\x01" + fakeGitHubToken(), "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, line, found := scanContent([]byte(tt.content))

			if tt.label == "" {
				if found {
					t.Fatalf("expected nothing, found %q on line %d", label, line)
				}

				return
			}

			if !found || label != tt.label || line != tt.line {
				t.Fatalf("got (%q, line %d, %v), want (%q, line %d)", label, line, found, tt.label, tt.line)
			}
		})
	}
}

func TestIsAllowed(t *testing.T) {
	patterns := []string{".env.local", "config/dev.key", "/root-only.key", "fixtures/", "*.pem"}

	allowed := []string{
		".env.local", "sub/dir/.env.local", "config/dev.key", "root-only.key",
		"fixtures/a/b.key", "certs/ca.pem",
	}

	for _, p := range allowed {
		if !isAllowed(patterns, p) {
			t.Errorf("expected %q to be allowed", p)
		}
	}

	denied := []string{".env", "other/dev.key", "sub/root-only.key", "fixturesX/a.key", "a.key"}

	for _, p := range denied {
		if isAllowed(patterns, p) {
			t.Errorf("expected %q to stay blocked", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration tests: Backup with the guard
// ---------------------------------------------------------------------------

func TestBackupKeepsEnvFileOut(t *testing.T) {
	f := newSyncFixture(t)
	logs := captureLogs(t)

	writeFile(t, filepath.Join(f.local, "main.go"), "package main\n")
	writeFile(t, filepath.Join(f.local, ".env"), "TOKEN=hunter2\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if !remoteHas(f.remote, "main.go") {
		t.Fatal("expected main.go to be backed up")
	}

	if remoteHas(f.remote, ".env") {
		t.Fatal(".env must not reach the remote")
	}

	// The file stays on disk, untracked.
	if status := mustGit(t, f.local, "status", "--porcelain"); status != "?? .env" {
		t.Fatalf("expected only .env to remain untracked, got %q", status)
	}

	if countLogsMentioning(*logs, ".env") != 1 {
		t.Fatalf("expected one warning about .env, got %v", *logs)
	}
}

func TestBackupKeepsSecretContentOut(t *testing.T) {
	f := newSyncFixture(t)
	logs := captureLogs(t)

	token := fakeGitHubToken()

	writeFile(t, filepath.Join(f.local, "config.go"), "package main\n\nvar token = \""+token+"\"\n")
	writeFile(t, filepath.Join(f.local, "keep.txt"), "fine\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if remoteHas(f.remote, "config.go") || !remoteHas(f.remote, "keep.txt") {
		t.Fatal("expected keep.txt on the remote and config.go kept out")
	}

	joined := strings.Join(*logs, "\n")

	if !strings.Contains(joined, "GitHub token") || !strings.Contains(joined, "line 3") {
		t.Fatalf("expected the warning to name the kind of secret and its line, got:\n%s", joined)
	}

	if strings.Contains(joined, token) {
		t.Fatal("the warning must never contain the secret itself")
	}
}

func TestBackupKeepsLargeFileOut(t *testing.T) {
	f := newSyncFixture(t)
	logs := captureLogs(t)

	old := MaxFileSize
	MaxFileSize = 1024

	t.Cleanup(func() { MaxFileSize = old })

	writeFile(t, filepath.Join(f.local, "big.dat"), strings.Repeat("x", 2048))
	writeFile(t, filepath.Join(f.local, "small.txt"), "ok\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if remoteHas(f.remote, "big.dat") || !remoteHas(f.remote, "small.txt") {
		t.Fatal("expected only the small file to be backed up")
	}

	if countLogsMentioning(*logs, "larger than 1 KiB") != 1 {
		t.Fatalf("expected a size warning, got %v", *logs)
	}
}

func TestBackupAllowlistOverridesGuard(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	writeFile(t, filepath.Join(f.local, allowlistFile), "# local dev settings\n.env\n")
	writeFile(t, filepath.Join(f.local, ".env"), "MODE=dev\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if !remoteHas(f.remote, ".env") {
		t.Fatal("expected the allowlisted .env to be backed up")
	}
}

func TestBackupWithOnlyBlockedFilesMakesNoCommit(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	commitsBefore := mustGit(t, f.local, "rev-list", "--count", "HEAD")
	remoteBefore := mustGit(t, f.remote, "rev-parse", "main")

	writeFile(t, filepath.Join(f.local, ".env"), "TOKEN=hunter2\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatalf("a backup with nothing committable must not fail: %v", err)
	}

	if got := mustGit(t, f.local, "rev-list", "--count", "HEAD"); got != commitsBefore {
		t.Fatalf("expected no new commit, commits went from %s to %s", commitsBefore, got)
	}

	if got := mustGit(t, f.remote, "rev-parse", "main"); got != remoteBefore {
		t.Fatal("the remote must not change")
	}
}

func TestBackupBlockedChangeSurvivesRebase(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// A tracked file that later gets a secret added to it.
	writeFile(t, filepath.Join(f.local, "settings.txt"), "mode=dev\n")
	mustGit(t, f.local, "add", ".")
	mustGit(t, f.local, "commit", "-m", "add settings")
	mustGit(t, f.local, "push", "origin", "HEAD")

	f.pushFromOther(t, "other.txt", "other\n", "remote-only commit")

	withSecret := "mode=dev\ntoken=" + fakeGitHubToken() + "\n"
	writeFile(t, filepath.Join(f.local, "settings.txt"), withSecret)

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatalf("backup should sync even though a modified file is held back: %v", err)
	}

	if !remoteHas(f.remote, "other.txt") {
		t.Fatal("expected the rebase to bring in the remote commit")
	}

	if got := mustGit(t, f.remote, "show", "main:settings.txt"); got != "mode=dev" {
		t.Fatalf("the secret reached the remote: %q", got)
	}

	// The local edit is still there, uncommitted.
	data, err := os.ReadFile(filepath.Join(f.local, "settings.txt"))
	if err != nil || string(data) != withSecret {
		t.Fatalf("the working-tree edit was lost: %v %q", err, data)
	}

	if status := mustGit(t, f.local, "status", "--porcelain"); status != "M settings.txt" {
		t.Fatalf("expected settings.txt to remain modified, got %q", status)
	}
}

func TestBackupUnstagesLiterally(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// "[ab].key" is blocked. "a.key" is allowlisted and must not be unstaged
	// just because "[ab]" would match it as a glob pattern.
	writeFile(t, filepath.Join(f.local, allowlistFile), "a.key\n")
	writeFile(t, filepath.Join(f.local, "a.key"), "allowed\n")
	writeFile(t, filepath.Join(f.local, "[ab].key"), "blocked\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if !remoteHas(f.remote, "a.key") {
		t.Fatal("a.key was unstaged by a glob match")
	}

	if remoteHas(f.remote, "[ab].key") {
		t.Fatal("[ab].key must be kept out")
	}
}

func TestBackupDeletingASecretFileIsNotBlocked(t *testing.T) {
	f := newSyncFixture(t)
	captureLogs(t)

	// A secret that was committed by hand earlier, then removed.
	writeFile(t, filepath.Join(f.local, ".env"), "TOKEN=old\n")
	mustGit(t, f.local, "add", "-f", ".env")
	mustGit(t, f.local, "commit", "-m", "oops")
	mustGit(t, f.local, "push", "origin", "HEAD")

	if err := os.Remove(filepath.Join(f.local, ".env")); err != nil {
		t.Fatal(err)
	}

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if remoteHas(f.remote, ".env") {
		t.Fatal("expected the deletion of .env to be backed up")
	}
}

func TestBackupFirstCommitInNewRepository(t *testing.T) {
	tempDir := t.TempDir()
	remote := filepath.Join(tempDir, "remote.git")
	local := filepath.Join(tempDir, "local")

	if out, err := exec.Command("git", "init", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	if err := os.Mkdir(local, 0755); err != nil {
		t.Fatal(err)
	}

	mustGit(t, local, "init", "-b", "main")
	configureIdentity(t, local)
	mustGit(t, local, "remote", "add", "origin", remote)

	captureLogs(t)

	writeFile(t, filepath.Join(local, "main.go"), "package main\n")
	writeFile(t, filepath.Join(local, ".env"), "TOKEN=hunter2\n")

	// There is no commit yet, so HEAD does not exist when the guard runs.
	if err := Backup(local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if !remoteHas(remote, "main.go") || remoteHas(remote, ".env") {
		t.Fatal("expected main.go on the remote and .env kept out")
	}
}

func TestBlockedFilesAreReportedOncePerChange(t *testing.T) {
	f := newSyncFixture(t)
	logs := captureLogs(t)

	envPath := filepath.Join(f.local, ".env")

	writeFile(t, envPath, "TOKEN=hunter2\n")

	for i := 0; i < 3; i++ {
		if err := Backup(f.local, "chore: automatic backup"); err != nil {
			t.Fatal(err)
		}
	}

	if got := countLogsMentioning(*logs, ".env"); got != 1 {
		t.Fatalf("expected one warning across three cycles, got %d", got)
	}

	// Once the file is gone and a clean cycle has run, a new .env is reported again.
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	writeFile(t, envPath, "TOKEN=hunter2\n")

	if err := Backup(f.local, "chore: automatic backup"); err != nil {
		t.Fatal(err)
	}

	if got := countLogsMentioning(*logs, ".env"); got != 2 {
		t.Fatalf("expected the recreated .env to be reported again, got %d warnings", got)
	}
}

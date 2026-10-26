package git

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Logf receives warnings about files the guard kept out of a backup.
// It defaults to the standard logger; tests and callers may replace it.
var Logf = log.Printf

// MaxFileSize is the largest file AutoGit will commit automatically.
// Larger files are left in the working tree and reported.
var MaxFileSize int64 = 25 << 20

const (
	// allowlistFile lists paths in a repository that bypass the guard.
	allowlistFile = ".autogit-allow"

	// maxScanSize is the largest file searched for secrets. Bigger files are
	// rarely source or config, and reading them would slow every cycle.
	maxScanSize = 1 << 20

	// batchSize bounds how many files are handled per git invocation, keeping
	// command lines and in-memory file contents small.
	batchSize = 100
)

// blockedFile is a staged file the guard refused to let AutoGit commit.
type blockedFile struct {
	Path   string
	Reason string
}

// ---------------------------------------------------------------------------
// File name rules
// ---------------------------------------------------------------------------

var (
	blockedBasenames = map[string]string{
		"id_rsa":           "SSH private key",
		"id_dsa":           "SSH private key",
		"id_ecdsa":         "SSH private key",
		"id_ed25519":       "SSH private key",
		".netrc":           "credentials file",
		".pgpass":          "credentials file",
		".htpasswd":        "credentials file",
		".pypirc":          "credentials file",
		".git-credentials": "credentials file",
		".envrc":           "environment file",
		"credentials.json": "credentials file",
		"secrets.json":     "secrets file",
		"secrets.yaml":     "secrets file",
		"secrets.yml":      "secrets file",
	}

	blockedExtensions = map[string]string{
		".key":      "private key file",
		".p12":      "certificate bundle with a private key",
		".pfx":      "certificate bundle with a private key",
		".jks":      "Java keystore",
		".keystore": "Java keystore",
		".kdbx":     "password database",
		".ppk":      "PuTTY private key",
		".tfstate":  "Terraform state",
	}

	blockedSuffixes = []string{"_rsa", "_dsa", "_ecdsa", "_ed25519"}

	// Everything inside these directories is blocked.
	blockedDirs = map[string]string{
		".ssh":   "inside an SSH directory",
		".aws":   "inside an AWS credentials directory",
		".gnupg": "inside a GPG directory",
		".kube":  "inside a Kubernetes config directory",
	}

	// Environment-file templates are meant to be committed.
	templateSuffixes = []string{".example", ".sample", ".template", ".dist"}
)

// checkName returns why a path must not be committed, or "" if its name is fine.
func checkName(p string) string {
	lower := strings.ToLower(p)
	segments := strings.Split(lower, "/")
	base := segments[len(segments)-1]

	for _, dir := range segments[:len(segments)-1] {
		if reason, ok := blockedDirs[dir]; ok {
			return reason
		}
	}

	if isEnvFile(base) {
		return "environment file"
	}

	if reason, ok := blockedBasenames[base]; ok {
		return reason
	}

	for _, suffix := range blockedSuffixes {
		if strings.HasSuffix(base, suffix) {
			return "SSH private key"
		}
	}

	if strings.HasSuffix(base, ".tfstate.backup") {
		return "Terraform state"
	}

	if reason, ok := blockedExtensions[path.Ext(base)]; ok {
		return reason
	}

	return ""
}

func isEnvFile(base string) bool {
	for _, suffix := range templateSuffixes {
		if strings.HasSuffix(base, suffix) {
			return false
		}
	}

	return base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env")
}

// ---------------------------------------------------------------------------
// Content rules
//
// Only token formats with a distinctive shape are listed. Broad rules such as
// "password = ..." would block ordinary code, and a blocked file is a file
// that is not backed up. This is a safety net, not a complete secret scanner.
// ---------------------------------------------------------------------------

type contentRule struct {
	label string
	re    *regexp.Regexp
	// skip, when set, discards matches that are known to be harmless.
	skip func(match []byte) bool
}

var (
	digitRuns = regexp.MustCompile(`\d+`)

	contentRules = []contentRule{
		{
			label: "private key",
			re:    regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`),
		},
		{
			label: "AWS access key ID",
			re:    regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
			// AWS's own documentation uses keys that end in EXAMPLE.
			skip: func(m []byte) bool { return bytes.Contains(m, []byte("EXAMPLE")) },
		},
		{
			label: "AWS secret access key",
			re:    regexp.MustCompile(`(?i)aws_secret_access_key\s*[=:]\s*["']?[A-Za-z0-9/+=]{40}`),
		},
		{
			label: "GitHub token",
			re:    regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})`),
		},
		{
			label: "Slack token",
			re:    regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
		},
		{
			label: "Google API key",
			re:    regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}`),
		},
		{
			label: "Stripe live key",
			re:    regexp.MustCompile(`\b[rs]k_live_[0-9A-Za-z]{20,}`),
		},
		{
			label: "Anthropic API key",
			re:    regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`),
		},
		{
			label: "OpenAI API key",
			re:    regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{40,}`),
		},
		{
			// The Solana CLI stores a keypair as a JSON array of 64 bytes.
			label: "Solana keypair",
			re:    regexp.MustCompile(`\A\s*\[\s*(?:\d{1,3}\s*,\s*){63}\d{1,3}\s*\]\s*\z`),
			skip:  notAllBytes,
		},
	}
)

// notAllBytes reports whether a number list has a value above 255, which
// means it cannot be a byte array.
func notAllBytes(match []byte) bool {
	for _, run := range digitRuns.FindAll(match, -1) {
		if n, err := strconv.Atoi(string(run)); err != nil || n > 255 {
			return true
		}
	}

	return false
}

func isBinary(content []byte) bool {
	head := content
	if len(head) > 8000 {
		head = head[:8000]
	}

	return bytes.IndexByte(head, 0) != -1
}

// scanContent looks for secrets in a text file. It returns the kind of secret
// and the line it starts on, never the secret itself.
func scanContent(content []byte) (label string, line int, found bool) {
	if len(content) > maxScanSize || isBinary(content) {
		return "", 0, false
	}

	for _, rule := range contentRules {
		for _, loc := range rule.re.FindAllIndex(content, -1) {
			if rule.skip != nil && rule.skip(content[loc[0]:loc[1]]) {
				continue
			}

			return rule.label, bytes.Count(content[:loc[0]], []byte{'\n'}) + 1, true
		}
	}

	return "", 0, false
}

// ---------------------------------------------------------------------------
// Allowlist
// ---------------------------------------------------------------------------

// loadAllowlist reads <repo root>/.autogit-allow. A missing or unreadable file
// means an empty allowlist.
//
// Each line is a path pattern relative to the repository root:
//
//	.env.local        a name with no slash matches in any directory
//	config/dev.key    a path with a slash matches from the root
//	/dev.key          a leading slash anchors to the root only
//	fixtures/         a trailing slash allows everything under a directory
//
// Blank lines and lines starting with # are ignored.
func loadAllowlist(repo string) []string {
	root, err := run(shortTimeout, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(strings.TrimSpace(root), allowlistFile))
	if err != nil {
		return nil
	}

	var patterns []string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, line)
		}
	}

	return patterns
}

func isAllowed(patterns []string, p string) bool {
	for _, pattern := range patterns {
		anchored := strings.HasPrefix(pattern, "/")
		pattern = strings.TrimPrefix(pattern, "/")

		if strings.HasSuffix(pattern, "/") {
			if strings.HasPrefix(p, pattern) {
				return true
			}

			continue
		}

		if ok, _ := path.Match(pattern, p); ok {
			return true
		}

		if !anchored && !strings.Contains(pattern, "/") {
			if ok, _ := path.Match(pattern, path.Base(p)); ok {
				return true
			}
		}
	}

	return false
}

// ---------------------------------------------------------------------------
// Guard
// ---------------------------------------------------------------------------

// guardStaged inspects everything currently staged in the repository, unstages
// every file that must not be committed automatically, and returns those files.
// The files stay untouched in the working tree.
//
// env selects the index to inspect, for example "GIT_INDEX_FILE=<path>" for a
// scratch index; nil means the repository's own index.
func guardStaged(repo string, env []string) ([]blockedFile, error) {
	output, err := runWith(runOptions{timeout: shortTimeout, path: repo, env: env},
		"diff", "--cached", "--name-only", "-z", "--no-renames", "--diff-filter=ACMT")
	if err != nil {
		return nil, err
	}

	var paths []string

	for _, p := range strings.Split(output, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}

	if len(paths) == 0 {
		return nil, nil
	}

	allow := loadAllowlist(repo)

	var (
		blocked   []blockedFile
		toInspect []string
	)

	for _, p := range paths {
		switch {
		case isAllowed(allow, p):
			continue
		case strings.Contains(p, "\n"):
			// Names with newlines cannot be passed safely to git's batch
			// interface, so they are refused rather than left unchecked.
			blocked = append(blocked, blockedFile{p, "file name contains a newline"})
		default:
			if reason := checkName(p); reason != "" {
				blocked = append(blocked, blockedFile{p, reason})
			} else {
				toInspect = append(toInspect, p)
			}
		}
	}

	inspected, err := inspectBlobs(repo, env, toInspect)
	if err != nil {
		return nil, err
	}

	blocked = append(blocked, inspected...)

	if err := unstage(repo, env, blocked); err != nil {
		return nil, err
	}

	sort.Slice(blocked, func(i, j int) bool { return blocked[i].Path < blocked[j].Path })

	return blocked, nil
}

// inspectBlobs checks the staged contents of paths for size and secrets.
func inspectBlobs(repo string, env []string, paths []string) ([]blockedFile, error) {
	var blocked []blockedFile

	for start := 0; start < len(paths); start += batchSize {
		chunk := paths[start:min(start+batchSize, len(paths))]

		sizes, err := blobSizes(repo, env, chunk)
		if err != nil {
			return nil, err
		}

		var toScan []string

		for i, p := range chunk {
			switch size := sizes[i]; {
			case size < 0:
				// Not a regular blob (for example a submodule): nothing to check.
			case size > MaxFileSize:
				blocked = append(blocked, blockedFile{p, "larger than " + formatSize(MaxFileSize)})
			case size <= maxScanSize:
				toScan = append(toScan, p)
			}
		}

		contents, err := readBlobs(repo, env, toScan)
		if err != nil {
			return nil, err
		}

		for i, p := range toScan {
			if label, line, found := scanContent(contents[i]); found {
				blocked = append(blocked, blockedFile{p, fmt.Sprintf("looks like a %s (line %d)", label, line)})
			}
		}
	}

	return blocked, nil
}

func indexSpecs(paths []string) []byte {
	var specs bytes.Buffer

	for _, p := range paths {
		specs.WriteString(":0:" + p + "\n")
	}

	return specs.Bytes()
}

// blobSizes returns the staged size of each path, or -1 when the entry is not a blob.
func blobSizes(repo string, env []string, paths []string) ([]int64, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	output, err := runWith(runOptions{timeout: shortTimeout, path: repo, env: env, stdin: indexSpecs(paths)},
		"cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != len(paths) {
		return nil, fmt.Errorf("git cat-file returned %d lines for %d paths", len(lines), len(paths))
	}

	sizes := make([]int64, len(paths))

	for i, line := range lines {
		fields := strings.Fields(line)

		if len(fields) != 3 || fields[1] != "blob" {
			sizes[i] = -1

			continue
		}

		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing blob size in %q: %w", line, err)
		}

		sizes[i] = size
	}

	return sizes, nil
}

// readBlobs returns the staged contents of each path (nil if missing).
func readBlobs(repo string, env []string, paths []string) ([][]byte, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	output, err := runWith(runOptions{timeout: longTimeout, path: repo, env: env, stdin: indexSpecs(paths)},
		"cat-file", "--batch")
	if err != nil {
		return nil, err
	}

	reader := bufio.NewReader(strings.NewReader(output))
	contents := make([][]byte, len(paths))

	for i := range paths {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading git cat-file header: %w", err)
		}

		fields := strings.Fields(header)

		if len(fields) == 2 && fields[1] == "missing" {
			continue
		}

		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected git cat-file header %q", strings.TrimSpace(header))
		}

		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("parsing blob size in %q: %w", strings.TrimSpace(header), err)
		}

		buf := make([]byte, size)

		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, fmt.Errorf("reading blob contents: %w", err)
		}

		// Each object is followed by a newline that is not part of its content.
		if _, err := reader.ReadByte(); err != nil {
			return nil, fmt.Errorf("reading blob terminator: %w", err)
		}

		contents[i] = buf
	}

	return contents, nil
}

// unstage removes the blocked files from the index, leaving the working tree alone.
func unstage(repo string, env []string, blocked []blockedFile) error {
	for start := 0; start < len(blocked); start += batchSize {
		args := []string{"reset", "-q", "--"}

		for _, b := range blocked[start:min(start+batchSize, len(blocked))] {
			args = append(args, b.Path)
		}

		// Literal pathspecs: a file named "[ab].key" must not also unstage "a.key".
		opts := runOptions{
			timeout: shortTimeout,
			path:    repo,
			env:     append(append([]string{}, env...), "GIT_LITERAL_PATHSPECS=1"),
		}

		if _, err := runWith(opts, args...); err != nil {
			return err
		}
	}

	return nil
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

var (
	reportMu     sync.Mutex
	lastReported = map[string]string{}
)

// reportBlocked logs blocked files. A repository's warnings repeat only when
// the set of blocked files changes, so a lingering .env does not log every cycle.
// Passing nil clears the memory for the repository.
func reportBlocked(repo string, blocked []blockedFile) {
	reportMu.Lock()
	defer reportMu.Unlock()

	if len(blocked) == 0 {
		delete(lastReported, repo)

		return
	}

	var signature strings.Builder

	for _, b := range blocked {
		signature.WriteString(b.Path + "\x00" + b.Reason + "\x00")
	}

	if lastReported[repo] == signature.String() {
		return
	}

	lastReported[repo] = signature.String()

	for _, b := range blocked {
		Logf("AutoGit kept %q out of the backup in %s: %s. To back it up anyway, add its path to %s in that repository.",
			b.Path, repo, b.Reason, allowlistFile)
	}
}

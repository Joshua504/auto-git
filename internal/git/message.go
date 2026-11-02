package git

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// now is the clock behind {date}, {time} and {datetime}. Tests replace it.
var now = time.Now

// maxSummaryLength keeps {summary} short enough for a commit subject line.
const maxSummaryLength = 60

// MessagePlaceholders lists the names a commit message template may use,
// each written in braces, for example {summary}.
func MessagePlaceholders() []string {
	return []string{"summary", "files", "branch", "host", "repo", "date", "time", "datetime"}
}

// fileChange is one file in a backup: a Git status letter and the file's path.
type fileChange struct {
	Status string
	Path   string
}

// messageContext is everything a template can refer to.
type messageContext struct {
	Changes []fileChange
	Branch  string
	Host    string
	Repo    string
	At      time.Time
}

// hasPlaceholders reports whether a message needs rendering at all. A message
// without braces is used exactly as written, so existing configurations behave
// as they always have.
func hasPlaceholders(message string) bool {
	return strings.Contains(message, "{")
}

// renderMessage fills in a commit message template. Values are inserted in one
// pass, so a file that happens to be named "{host}" is never expanded.
func renderMessage(template string, c messageContext) string {
	return strings.NewReplacer(
		"{summary}", summarizeChanges(c.Changes),
		"{files}", strconv.Itoa(len(c.Changes)),
		"{branch}", c.Branch,
		"{host}", c.Host,
		"{repo}", c.Repo,
		"{date}", c.At.Format("2006-01-02"),
		"{time}", c.At.Format("15:04:05"),
		"{datetime}", c.At.Format("2006-01-02 15:04:05"),
	).Replace(template)
}

// summarizeChanges describes what changed in a few words, for example
// "update main.go, add notes.txt and 2 more". It names up to three files and
// uses fewer if the result would be too long for a subject line.
func summarizeChanges(changes []fileChange) string {
	if len(changes) == 0 {
		return "update files"
	}

	for shown := min(3, len(changes)); shown >= 1; shown-- {
		text := listChanges(changes[:shown])

		if rest := len(changes) - shown; rest > 0 {
			text += fmt.Sprintf(" and %d more", rest)
		}

		if len(text) <= maxSummaryLength {
			return text
		}
	}

	return fmt.Sprintf("%d files changed", len(changes))
}

// listChanges names the files. When they all share one action it is said once,
// "add a.go, b.go", and otherwise each file gets its own, "update a.go, add b.go".
func listChanges(changes []fileChange) string {
	verb := verbFor(changes[0].Status)
	same := true
	parts := make([]string, len(changes))

	for i, change := range changes {
		parts[i] = path.Base(change.Path)

		if verbFor(change.Status) != verb {
			same = false
		}
	}

	if same {
		return verb + " " + strings.Join(parts, ", ")
	}

	for i, change := range changes {
		parts[i] = verbFor(change.Status) + " " + parts[i]
	}

	return strings.Join(parts, ", ")
}

func verbFor(status string) string {
	switch status {
	case "A":
		return "add"
	case "D":
		return "delete"
	default:
		return "update"
	}
}

// parseNameStatus reads the output of "git diff --name-status -z --no-renames".
func parseNameStatus(output string) []fileChange {
	fields := strings.Split(output, "\x00")

	var changes []fileChange

	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "" {
			break
		}

		changes = append(changes, fileChange{Status: fields[i][:1], Path: fields[i+1]})
	}

	return changes
}

// stagedChanges lists what is staged in the given index, compared with HEAD.
func stagedChanges(repo string, env []string) ([]fileChange, error) {
	output, err := runWith(runOptions{timeout: shortTimeout, path: repo, env: env},
		"diff", "--cached", "--name-status", "-z", "--no-renames")
	if err != nil {
		return nil, err
	}

	return parseNameStatus(output), nil
}

// treeChanges lists what differs between two trees.
func treeChanges(repo, oldTree, newTree string) ([]fileChange, error) {
	output, err := run(shortTimeout, repo, "diff-tree", "-r", "--name-status", "-z", "--no-renames", oldTree, newTree)
	if err != nil {
		return nil, err
	}

	return parseNameStatus(output), nil
}

// emptyTreeID is the ID Git gives a tree with nothing in it.
func emptyTreeID(repo string) (string, error) {
	output, err := runWith(runOptions{timeout: shortTimeout, path: repo, stdin: []byte{}},
		"hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(output), nil
}

// renderCommitMessage prepares the message for a backup commit. A message with
// no placeholders is returned untouched, without running any extra Git commands.
func renderCommitMessage(repo, template string, opts Options, changes []fileChange) (string, error) {
	if !hasPlaceholders(template) {
		return template, nil
	}

	branch, err := rawBranchName(repo)
	if err != nil {
		return "", err
	}

	host := opts.Machine
	if host == "" {
		host, _ = os.Hostname()
	}

	name := repo
	if abs, err := filepath.Abs(repo); err == nil {
		name = abs
	}

	return renderMessage(template, messageContext{
		Changes: changes,
		Branch:  branch,
		Host:    host,
		Repo:    filepath.Base(name),
		At:      now(),
	}), nil
}

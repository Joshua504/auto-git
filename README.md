# AutoGit

**Automatic Git backups and remote synchronization for your development projects.**

AutoGit is a lightweight Go application that automates routine Git operations while you work. It monitors configured code editors, periodically checks configured repositories, and backs up your work to your Git remote, either by committing to your current branch or by keeping snapshots on a separate backup branch that never touches your own.

The goal is simple: reduce the chance of forgetting to commit and push your work before switching computers or ending a development session.

> **Project status:** Working prototype. Two backup modes, a secrets guard, repository-state checks, timeouts and retries, editor-aware scheduling (including an opening and a closing backup), desktop notifications, enhanced backup branch retention with calendar policies and per-repository settings, concurrency protection, an offline backup queue that survives restarts, backup verification, and GitHub Actions CI are implemented and covered by automated tests, and have been exercised against real repositories. Review the limitations below before relying on AutoGit as your only backup solution.

---

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [How AutoGit Works](#how-autogit-works)
- [Requirements](#requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [Keeping Secrets Out of Backups](#keeping-secrets-out-of-backups)
- [Usage](#usage)
- [Backup Restore and Recovery](#backup-restore-and-recovery)
- [Synchronization and Conflict Handling](#synchronization-and-conflict-handling)
- [Testing](#testing)
- [Troubleshooting](#troubleshooting)
- [Limitations](#limitations)
- [Security Considerations](#security-considerations)
- [Project Structure](#project-structure)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

---

## Overview

When developing across multiple computers, it is easy to forget to push your latest work to a remote repository. This can leave your work unavailable on another machine, create divergent Git histories when both machines have new commits, or lose it entirely if a disk fails before you commit.

AutoGit is designed to automate this routine workflow.

For example:

1. You open GoLand to work on a Go project.
2. AutoGit detects that a configured editor is running, brings in any new commits from your remote so you start from the latest code, and backs up your repositories right away.
3. Its scheduler then checks them again at the specified interval for as long as an editor stays open.
4. Before anything is saved, a secrets guard keeps files that look like credentials, private keys, or oversized files out of the backup.
5. In **commit mode** (the default), AutoGit commits your changes on the current branch, fetches the remote, rebases if the remote is ahead, and pushes.
6. In **branch mode**, AutoGit leaves your branch, staged files, and working tree alone, and pushes a snapshot of your work to a separate backup branch instead.
7. When you close the last configured editor, AutoGit runs one final backup so your last edits are saved, and then pauses.

AutoGit uses Git rather than implementing its own version-control system. Your repositories, commits, branches, and remotes remain ordinary Git repositories.

---

## Features

### 1. Two backup modes

AutoGit can protect your work in two ways. Choose with `backup.mode` in `config.yaml`:

- **`commit`** (the default) commits changes on the branch you are working on, brings in remote changes, and pushes that branch.
- **`branch`** (recommended) never touches your branch, your staged files, or your working tree while it backs up. (The one thing that can move your branch is the optional [pull on open](#pull-on-open), and it only fast-forwards.) It pushes a snapshot of your working directory to a separate branch named `autogit/<machine>/<branch>`.

Leaving the `backup` section out keeps `commit` mode, so existing configurations work unchanged. See [Backup modes](#backup-modes).

### 2. Automatic change detection and commits (commit mode)

AutoGit checks configured repositories for changes using Git's working-tree status.

It detects tracked-file modifications, deletions, and untracked files that Git has not been configured to ignore.

When changes exist, AutoGit stages them, runs the [secrets guard](#keeping-secrets-out-of-backups), and creates a commit using the configured commit message.

The message can describe each backup, so your history shows what every commit contains instead of a column of identical lines:

```text
backup: update main.go, add notes.txt
backup: delete notes.txt
backup: add file1.txt, file2.txt, file3.txt and 2 more
```

Set it in `config.yaml`. See [Commit messages](#commit-messages).

AutoGit skips creating a commit when there are no local changes to commit, or when the guard kept out everything that changed.

### 3. Automatic remote synchronization (commit mode)

AutoGit fetches updates from the configured repository's `origin` remote.

It checks whether the current branch is behind its configured upstream and attempts to rebase when remote commits are missing locally.

After successful synchronization, it attempts to push the current branch.

### 4. Divergent history handling (commit mode)

When local and remote branches contain different commits, AutoGit attempts to rebase the local commits onto the upstream history.

This helps preserve both sets of commits when their changes can be reconciled.

If the rebase fails, AutoGit attempts to abort it and reports the failure instead of proceeding to push conflicting history.

Files the secrets guard kept out of a commit stay modified in your working tree. AutoGit rebases with `--autostash` so that they do not block synchronization.

**Important:** AutoGit does not automatically resolve merge or rebase conflicts. Manual intervention may be necessary before another successful synchronization.

### 5. Branch snapshots (branch mode)

In branch mode, each cycle AutoGit copies your Git index to a scratch file, stages everything into the copy, writes the result as a commit, and pushes that commit to the backup branch. Your real branch, index, and files are only read, never changed, by the backup itself.

- Staged, unstaged, and untracked files are all captured, except what the secrets guard keeps out.
- The snapshot history includes your own commits, so work you committed but never pushed is protected too.
- A new snapshot is created only when your working tree differs from the previous one.
- Git hooks do not run, so a slow or failing pre-commit hook cannot stop a backup.
- A detached HEAD, a repository with no commits yet, and a rebase or merge in progress are all backed up safely.
- If the network is down, snapshots keep accumulating locally and are all pushed when it returns.

### 6. Secrets guard

Before saving anything, AutoGit checks the files that changed and keeps out file names, file sizes, and file contents that look dangerous: `.env` files, private keys, cloud credentials, API tokens, Solana keypair files, and files over 25 MiB. Kept-out files stay untouched on your disk. See [Keeping Secrets Out of Backups](#keeping-secrets-out-of-backups).

### 7. Repository safety checks (commit mode)

AutoGit never stages or commits in a repository you are in the middle of working on. It skips the cycle, without failing, when a rebase, merge, cherry-pick, revert, or bisect is in progress, when files have unresolved conflicts, when another Git process holds the index lock, or when HEAD is detached. It also checks that Git has a `user.name` and `user.email` before it tries to commit. See [Repository states AutoGit skips](#repository-states-autogit-skips-commit-mode).

### 8. Scheduled backups

The backup interval is configurable.

For example:

```yaml
interval: "1m"
```

This configures a one-minute interval between scheduled backup checks.

Other valid Go duration strings include:

```yaml
interval: "30s"
```

```yaml
interval: "5m"
```

```yaml
interval: "1h"
```

Shorter intervals provide more frequent checks but can result in more frequent commits and network operations.

The first backup does not wait for a full interval. It runs as soon as a configured editor is detected.

#### Scheduling policies

By default, AutoGit backs up every tick regardless of whether anything changed. With a scheduling policy, you can make AutoGit back up only when repositories have active development, saving CPU, battery, and network resources.

Each repository can use its own policy through the `schedule` block:

```yaml
repositories:
  - path: "/path/to/project"
    schedule:
      policy: "smart"
      interval: "5m"
      activity_window: "2h"
      safety_interval: "48h"
```

**Policies:**

| Policy | Behavior |
|--------|----------|
| `"fixed"` (default) | Backs up on every tick — the original behavior |
| `"smart"` | Backs up only when uncommitted changes are detected, with a safety fallback (`safety_interval`, default 24h) that forces a backup even when nothing changed |
| `"hybrid"` | Like smart, but enforces a minimum time between backups (`min_interval`, default 1m) to throttle very active repositories |

**Configuration parameters:**

- `schedule.policy` — `"fixed"`, `"smart"`, or `"hybrid"`. When omitted, defaults to `"fixed"`.
- `schedule.interval` — per-repository interval override. When omitted, the global `interval` is used.
- `schedule.activity_window` — how far back to look for recent changes (default `1h`). Used by `smart` and `hybrid`.
- `schedule.safety_interval` — maximum time between backups when nothing changed (default `24h`). Used by `smart` only.
- `schedule.min_interval` — minimum time between backups (default `1m`). Used by `hybrid` only.

**How it works:**

The scheduler still ticks at the configured interval for every policy. The difference is **what happens on each tick** — the policy decides whether to actually run a backup or skip it.

With `"smart"`, if a repository hasn't changed in days, the scheduler still ticks every 5 minutes, but the backup is skipped. It only runs when you actually made changes, or when the safety interval forces one.

With `"hybrid"`, the same skip-when-nothing-changed behavior applies, but even a very active repository won't back up more often than `min_interval` allows. For example, with `interval: 30s` and `min_interval: 2m`, the repo won't back up more than once every 2 minutes.

If you leave `schedule` off entirely, the repository uses the original fixed-interval behavior with the global `interval` setting.

### 9. Battery-aware scheduling

On a laptop, AutoGit can detect whether the computer is running on battery or external power, and automatically adjust its backup behavior to save battery life.

Add a `power` section to your `config.yaml`:

```yaml
power:
  battery_interval: "15m"
  low_battery_percent: 20
```

**Configuration options:**

| Option | Description | Default |
|--------|-------------|---------|
| `battery_interval` | Backup interval when running on battery (e.g., `"15m"`, `"30m"`). Leave empty to use the normal interval on battery too. | Empty (uses normal interval) |
| `low_battery_percent` | Battery percentage below which automatic backups are paused. Set to `0` to disable. | `0` (disabled) |

**How it works:**

- When you unplug and the battery starts discharging, AutoGit switches to the longer `battery_interval`. When you plug back in, it switches back to the normal interval.
- When the battery drops below `low_battery_percent`, automatic backups are skipped entirely. Manual backups still work.
- Transitions are detected automatically — no restart is needed.

**Example configurations:**

*Conservative — save maximum battery:*
```yaml
power:
  battery_interval: "30m"
  low_battery_percent: 15
```

*Balanced — moderate reduction on battery:*
```yaml
power:
  battery_interval: "15m"
  low_battery_percent: 20
```

*Interval only — no low-battery pause:*
```yaml
power:
  battery_interval: "15m"
```

Battery-aware scheduling applies to repositories using the `smart` or `hybrid` scheduling policy. On systems without a battery (desktops), AutoGit falls back to always assuming AC power.

### 10. Editor-aware scheduling

AutoGit checks for configured editor processes every 5 seconds and enables scheduled backups while at least one configured editor is running.

Supported editor process names in the example configuration include:

- GoLand (`goland`)
- PyCharm (`pycharm`)
- Visual Studio Code (`code`)
- VSCodium (`codium`)
- Neovim (`nvim`)
- Vim (`vim`)
- Zed (`zed`)
- Cursor (`cursor`)

The configured names must match the process names detected on your system.

When an editor opens, AutoGit backs up immediately, then on every interval.

When the last configured editor closes, AutoGit stops the schedule and runs **one final backup**, so the edits you made just before closing are saved. Then it pauses until an editor opens again.

Stopping AutoGit itself with `Ctrl+C` does not run a final backup, so that quitting stays quick.

This reduces unnecessary background activity when you are not working in a configured editor.

### 11. Pull on open

When an editor opens, AutoGit first fetches your remote and brings in any commits that were pushed from another computer, before it looks at your own changes. You start working on the latest code, and your first push is not rejected because the remote moved on.

It only ever fast-forwards. It never creates a merge commit, rewrites your history, or stashes anything. If a fast-forward would need more than that, because your branch and its upstream have each moved on, or because your uncommitted changes touch files that also changed upstream, AutoGit changes nothing and tells you why. It also leaves a repository alone while you are in the middle of a rebase, merge, or similar.

This is on by default. Turn it off with `sync.pull_on_open: false`. See [Pull on open](#pull-on-open).

### 12. Resilient Git operations

AutoGit is built to keep working unattended:

- **Timeouts.** Quick local Git commands are stopped after 30 seconds. Commands that use the network, run hooks, or can be slow (`fetch`, `push`, `add`, `commit`, `rebase`) are stopped after 2 minutes. A hung connection cannot freeze AutoGit.
- **No interactive prompts.** Git is run with `GIT_TERMINAL_PROMPT=0`, so a missing credential fails immediately instead of waiting for input that nobody is there to type.
- **Retries.** Fetches and pushes are attempted up to 3 times, 2 seconds apart, when the failure looks temporary: for example a DNS failure, a connection that timed out or was reset, an unreachable network, or a 502, 503, or 504 response. Authentication failures, permission errors, a missing repository or remote, and rejected pushes are not retried, since trying again cannot fix them. A command that hit AutoGit's own timeout is not retried either; the next scheduled cycle is the retry.

### 13. Multiple repository support

The configuration accepts a list of repository paths. AutoGit can therefore be configured to check multiple local Git repositories.

Each repository must exist and be a valid Git working tree.

A failure or a skipped cycle in one repository does not stop the others.

### 14. Configurable YAML settings

AutoGit uses a YAML configuration file to define the backup interval, repositories, commit message, backup mode, and editor processes.

This keeps common settings separate from the Go implementation.

Use the `--config` flag to choose a file, or let AutoGit find `config.yaml` in the current directory or in `~/.config/autogit/`. See [Where AutoGit looks for its configuration](#where-autogit-looks-for-its-configuration).

### 15. Error reporting

AutoGit reports operational failures encountered during Git commands, including failures while fetching, rebasing, or pushing.

It distinguishes a failure from a deliberate skip. A repository that is busy or mid-operation is logged as `Skipping <repository>: <reason>`, and a failure is logged as `Backup failed for <repository>: <error>`. Files the secrets guard keeps out are logged once, with the reason.

A failed operation is not the same as a successful backup. Review the logs and repository state when an error is reported.

### 16. Desktop notifications

AutoGit can send desktop notifications with audible alerts for key events, so you do not need to watch the terminal log. When enabled, you receive a notification for:

- **Successful backups** when changes were pushed.
- **Failed backups** when a push or other operation fails.
- **Skipped repositories** when a repo is locked by another process.
- **Pulled commits** when new commits are brought in on editor open.
- **Diverged branches** when AutoGit cannot pull because your branch and upstream each have unique commits.
- **Blocked pulls** when uncommitted changes conflict with upstream changes.
- **Editor state changes** when backups are enabled or paused.

All notifications include an audible alert sound.

```yaml
notifications:
  enabled: true   # the default is false; set to true to enable
```

Notifications use the `beeep` library, which calls `notify-send` on Linux, `osascript` on macOS, and Toast notifications on Windows.

### 17. Backup branch retention

In branch mode, each backup adds a commit to the backup branch. Without retention, these branches grow indefinitely. Retention automatically prunes old snapshots, rewriting the branch history and force-pushing the result so old snapshots are removed from both local and remote.

#### Retention rules

Retention uses multiple rules that work together. A snapshot is **kept** if it passes **any** rule:

- **`max_age`** — Keep snapshots newer than this many days.
- **`max_commits`** — Keep the N most recent snapshots.
- **`keep_daily`** — Keep the last snapshot of each of the last N days.
- **`keep_weekly`** — Keep the last snapshot of each of the last N weeks (by ISO week).
- **`keep_monthly`** — Keep the last snapshot of each of the last N months.

```yaml
backup:
  mode: "branch"
  retention:
    max_age: 30        # delete snapshots older than 30 days
    max_commits: 50    # keep at most 50 snapshots per branch
    keep_daily: 7      # keep last snapshot of each of the last 7 days
    keep_weekly: 4     # keep last snapshot of each of the last 4 weeks
    keep_monthly: 12   # keep last snapshot of each of the last 12 months
```

At least one snapshot is always kept so the branch is never empty. Set all values to `0` or omit the `retention` section to disable pruning.

#### Per-repository retention

Each repository can override the global retention policy. When a repository has a `retention` block, it replaces the global `backup.retention` entirely for that repository:

```yaml
repositories:
  - path: "/path/to/important-project"
    retention:
      max_age: 90
      max_commits: 100
      keep_daily: 14
      keep_weekly: 8
      keep_monthly: 12
```

#### Protected branch safety

Before pruning, AutoGit checks whether each snapshot is an ancestor of any local user branch (branches that are not backup branches). If a snapshot is reachable from a user branch, it is marked as **protected** and will never be pruned, regardless of the retention rules.

#### Manual cleanup

You can preview or execute cleanup at any time using the `cleanup` subcommand:

```bash
# Preview what would be pruned (default, safe)
autogit cleanup

# Preview for a specific repository
autogit cleanup --repo /path/to/repo

# Execute cleanup, removing pruned snapshots
autogit cleanup --force

# Execute cleanup for a specific repository
autogit cleanup --repo /path/to/repo --force
```

The output shows each snapshot with its status: `keep`, `prune`, or `protected`.

### 18. Automated tests

The project includes tests for configuration, editor detection, Git operations, scheduling, and the editor-monitoring loop.

The full test suite can be run with:

```bash
go test ./...
```

The project has also been exercised against a real repository to test automatic commits, pushing local changes, retrieving remote changes, divergent histories, and rebase-conflict handling.

### 19. Concurrency protection

If two AutoGit instances run at the same time — for example, a scheduled systemd service and a manual foreground run — they would normally race on the same repository, corrupting backup branches or creating conflicting commits. AutoGit uses an exclusive file lock (`flock`) inside each repository's `.git` directory so that only one instance can operate on a repository at a time. The second instance skips that repository and will retry on the next cycle.

This protection covers both backup operations and the pull-on-open synchronization.

### 20. Continuous integration

A GitHub Actions workflow runs `go build`, `go vet`, and `go test -race` on every push and pull request, so broken code is caught before it reaches your branch.

### 21. Offline backup queue

When enabled, backup operations that fail due to network issues are stored locally in a SQLite database and automatically retried when connectivity returns. The queue persists across application restarts, so pending backups survive if AutoGit is stopped or crashes.

Each queued entry records the repository path, commit hash, remote URL, status, retry count, and last error. Duplicate prevention uses the repository path and commit hash as a unique key: if the same commit is already queued, it is not added again.

A background processor periodically checks for network availability and retries pending entries. When a backup succeeds normally, any matching queued entry is automatically cleared.

To enable it, add this to `config.yaml`:

```yaml
offline_queue:
  enabled: true
  retry_delay: "1m"
  max_retries: 10
```

Inspect the queue status at any time:

```bash
autogit queue
```

This shows counts of pending, completed, and failed operations, along with details for each.

### 23. Backup verification

A successful `git push` does not guarantee that a backup is recoverable later. The remote could lose objects, references could be accidentally deleted, or network issues could cause silent failures. Backup verification gives confidence that backups are actually present and intact.

For each configured repository, verification runs five checks in sequence:

1. **Remote reachability** — Runs `git fetch`. If the remote is unreachable (network down, repository deleted, authentication expired), verification stops early.
2. **Backup reference exists** — Checks that the expected backup reference exists on the remote. In commit mode this is the current branch's upstream tracking ref. In branch mode this is the backup branch's tracking ref.
3. **Commit hash comparison** — If an expected commit hash is known, it is compared with the remote commit hash. A mismatch means the backup was overwritten or corrupted.
4. **Snapshot fetchability** — Validates that the commit object, its tree, and a sample of blob files are all accessible on the remote.
5. **Corruption detection** — If any check fails, a human-readable issue is recorded.

Verification can run manually via the CLI or automatically on a schedule:

```bash
autogit verify
```

To enable periodic verification, add this to `config.yaml`:

```yaml
verification:
  enabled: true
  interval: "24h"
```

When enabled, AutoGit runs verification automatically on the configured interval (default 24 hours), starting one minute after launch so the initial backup has time to complete. This runs independently of whether an editor is open.

If any issue is found, a desktop notification is sent (if notifications are enabled). See [Verify backups](#verify-backups).

---

## How AutoGit Works

The general workflow is:

```text
Start AutoGit
     |
     v
Load configuration (--config, ./config.yaml, or ~/.config/autogit/config.yaml)
     |
     v
Check for configured editors every 5 seconds
     |
     +---- No editor open --------> Do nothing
     |
     +---- An editor opens -------> Pull new commits from the remote,
     |                              back up immediately,
     |                              then again every interval
     |
     +---- The last editor closes -> Stop the schedule,
                                     run one final backup, then wait again
```

On every editor open, AutoGit first runs the [pull on open](#pull-on-open) check on each configured repository, then the first backup.

Each backup pass handles every configured repository in turn. What a pass does depends on the backup mode.

### Commit mode

```text
Is the repository safe to touch?
     |   (rebase, merge, lock, conflicts, or detached HEAD: skip this cycle)
     v
Any local changes?
     |
     +---- Yes ---> Check Git identity -> git add . -> secrets guard -> commit
     |
     +---- No ----> Nothing to commit
     |
     v
Fetch from origin
     |
     v
Is the remote ahead?
     |
     +---- Yes ---> Rebase (on failure: abort, report, do not push)
     |
     v
Push
```

### Branch mode

```text
Fetch from origin
     |   (if the network is down: carry on, report the error afterwards)
     v
Copy your index to a scratch file and stage everything into the copy
     |
     v
Secrets guard
     |
     v
Write a snapshot commit
     |   (parents: the previous snapshot and your own HEAD)
     v
Update the hidden local record of the newest snapshot
     |
     v
Push the snapshot to autogit/<machine>/<branch>
```

This is a conceptual overview. Actual command execution and error handling determine whether a particular cycle completes successfully.

### Git operations

AutoGit relies on Git commands for its core operations.

In **commit mode**, the implementation includes operations corresponding to:

- `git rev-parse --git-path`, `git ls-files --unmerged`, and `git symbolic-ref` to check the repository state before touching it.
- `git status --porcelain` to detect changes.
- `git add .` to stage changes.
- `git diff --cached`, `git cat-file --batch`, and `git reset` to inspect staged files and unstage the ones the secrets guard keeps out.
- `git commit` to create a commit.
- `git fetch origin` to retrieve remote history.
- Git revision and upstream checks to determine whether remote commits are missing locally.
- `git rebase --autostash @{upstream}` to reconcile history when necessary.
- `git push -u origin HEAD` to push the current branch.

In **branch mode**, it uses:

- `git fetch origin` to retrieve remote history, including earlier snapshots.
- A scratch copy of the index (`GIT_INDEX_FILE`) with `git add -A .` to stage your working tree without touching your real index.
- The same secrets-guard commands, run against the scratch index.
- `git write-tree` and `git commit-tree` to create the snapshot commit.
- `git update-ref` to record the newest snapshot under `refs/autogit/`.
- `git push origin <snapshot>:refs/heads/autogit/<machine>/<branch>` to push it.

On an **editor open**, in either mode and unless pull on open is switched off, it runs `git fetch origin`, checks whether the upstream is ahead, and, only if the branch can simply move forward, `git merge --ff-only @{upstream}`.

Because AutoGit uses Git directly, your usual Git configuration, credentials, ignore rules, and remote settings can affect its behavior. In commit mode your Git hooks run when AutoGit commits. In branch mode they do not.

---

## Requirements

Before installing AutoGit, ensure your environment has:

- **Go:** the version declared in the project's `go.mod` (currently 1.27.1).
- **Git:** installed and accessible through your shell's `PATH`. AutoGit has been tested with Git 2.43. The test suite needs Git 2.28 or newer, because it uses `git init -b`.
- **A Git identity:** `user.name` and `user.email` must be configured for any repository AutoGit commits in. AutoGit reports a clear error if they are missing.
- **A Git repository:** the repository you want to back up must already exist locally.
- **A remote repository:** typically GitHub or another Git server configured as `origin`.
- **Git authentication that works unattended:** credentials or SSH configuration that allow Git to fetch and push without asking you anything. AutoGit turns Git's interactive prompts off, so use a credential helper or an SSH agent.
- **Linux:** the current editor-monitoring implementation uses `pgrep`, so the documented setup targets Linux.

In commit mode, the current branch should have a configured upstream once its initial push has been established. Branch mode only needs the `origin` remote; it does not use your branch's upstream.

---

## Installation

### 1. Install Go and Git

Verify that both tools are available:

```bash
go version
git --version
```

If either command is unavailable, install the corresponding package for your Linux distribution.

### 2. Clone AutoGit

```bash
git clone https://github.com/Joshua504/auto-git.git
cd auto-git
```

### 3. Create your configuration

Copy the example configuration and edit the copy:

```bash
cp config.example.yaml config.yaml
```

Open `config.yaml` and update the repository paths, editor names, interval, and commit message to match your environment. `config.yaml` is listed in `.gitignore`, so your personal paths are never committed.

The example repository path is only a placeholder. Replace it with the absolute path of a repository on your machine.

### 4. Run AutoGit

From the project root:

```bash
go run ./cmd/autogit
```

A successful startup should display messages similar to:

```text
AutoGit configuration loaded successfully.
Configuration file: /path/to/auto-git/config.yaml
Backup interval: 1m
Backup mode: commit
Pull new commits when an editor opens: true
Desktop notifications: off
Repositories configured: 1
Editors monitored: [goland pycharm code codium nvim vim zed cursor]
AutoGit is waiting for a configured editor to open.
```

The exact output depends on your configuration and whether a configured editor is already running.

### 5. Optional: Build a standalone executable

Instead of running through `go run` each time, build the executable:

```bash
go build -o autogit ./cmd/autogit
```

Then run it:

```bash
./autogit
```

Without a flag, AutoGit looks for `config.yaml` in the current directory first, so running it from the project root works as it always did. The executable does not have to be started from there. See [Where AutoGit looks for its configuration](#where-autogit-looks-for-its-configuration) for how it finds `config.yaml`, or point it at a file directly:

```bash
./autogit --config /path/to/config.yaml
```

---

## Configuration

AutoGit's main configuration file is `config.yaml`. A template with placeholder values is provided as `config.example.yaml`.

### Where AutoGit looks for its configuration

AutoGit uses the first of these that exists:

1. The file given with `--config`. If you pass this flag and the file does not exist, AutoGit stops with an error instead of falling back to another file.
2. `config.yaml` in the current directory.
3. `autogit/config.yaml` inside your user configuration directory, which is `~/.config/autogit/config.yaml` on Linux.

The third location lets AutoGit start from any directory, such as a desktop autostart entry, without depending on the working directory. To use it:

```bash
mkdir -p ~/.config/autogit
cp config.example.yaml ~/.config/autogit/config.yaml
```

At startup AutoGit prints the full path of the file it loaded.

### Example configuration

```yaml
interval: "1m"

repositories:
  - path: "/path/to/your/repository"

commit:
  message: "backup: {summary}"

backup:
  mode: "branch"

editors:
  - goland
  - pycharm
  - code
  - codium
  - nvim
  - vim
  - zed
  - cursor
```

Replace the example repository path with the absolute path to your own local repository.

### Configuration reference

| Setting | Purpose |
|---|---|
| `interval` | Time between scheduled backup checks. |
| `repositories` | List of local repositories AutoGit should process. |
| `repositories[].path` | Absolute path to a Git working tree. |
| `commit.message` | Commit message used for automatic commits and snapshots. May contain placeholders such as `{summary}`. See [Commit messages](#commit-messages). |
| `backup.mode` | `commit` (default) commits on your current branch. `branch` keeps snapshots on a separate backup branch. See [Backup modes](#backup-modes). |
| `backup.prefix` | Branch mode only. Namespace of the backup branches. Default: `autogit`. |
| `backup.machine` | Branch mode only. Name of this computer in the branch name. Default: the host name. |
| `sync.pull_on_open` | `true` (default) fast-forwards each repository to its upstream when an editor opens. See [Pull on open](#pull-on-open). |
| `backup.retention.max_age` | Branch mode only. Delete snapshots older than this many days. Zero means no age limit. See [Backup branch retention](#17-backup-branch-retention). |
| `backup.retention.max_commits` | Branch mode only. Keep at most this many snapshots per branch. Zero means no count limit. See [Backup branch retention](#17-backup-branch-retention). |
| `backup.retention.keep_daily` | Branch mode only. Keep the last snapshot of each of the last N days. See [Backup branch retention](#17-backup-branch-retention). |
| `backup.retention.keep_weekly` | Branch mode only. Keep the last snapshot of each of the last N weeks. See [Backup branch retention](#17-backup-branch-retention). |
| `backup.retention.keep_monthly` | Branch mode only. Keep the last snapshot of each of the last N months. See [Backup branch retention](#17-backup-branch-retention). |
| `notifications.enabled` | `false` (default). Set to `true` to enable desktop notifications with audible alerts. See [Desktop notifications](#16-desktop-notifications). |
| `offline_queue.enabled` | `false` (default). Set to `true` to persist failed backups and retry them when the network returns. See [Offline backup queue](#21-offline-backup-queue). |
| `offline_queue.path` | Directory for the queue database. Default: `~/.config/autogit`. |
| `offline_queue.retry_delay` | Wait time between retry cycles. Default: `30s`. |
| `offline_queue.max_retries` | Maximum retry attempts before marking an entry as failed. Default: `5`. |
| `verification.enabled` | `false` (default). Set to `true` to enable periodic backup verification. See [Backup verification](#23-backup-verification). |
| `verification.interval` | Time between verification runs. Default: `24h`. Accepts any Go duration string (e.g. `12h`, `168h` for weekly). |

Invalid `backup` settings stop AutoGit at startup with a message naming the setting.
| `editors` | Process names used to determine whether scheduled backups should run. |

### Backup modes

AutoGit can protect your work in two ways. Choose with `backup.mode`.

**`commit` (the default)** commits changes on the branch you are working on, brings in remote changes, and pushes that branch. Your history fills with automatic commits, and half-finished code is pushed to your real branch.

**`branch` (recommended)** never touches your branch, your staged files, or your working tree when it backs up. Instead, every cycle it takes a snapshot of your working directory and pushes it to a separate branch:

```text
autogit/<machine>/<branch>      for example   autogit/my-laptop/main
```

```yaml
backup:
  mode: "branch"
```

What branch mode gives you:

- **A clean history.** Automatic commits never appear on `main`.
- **Nothing is rebased or merged for you.** AutoGit only reads your files when it backs up, so there is nothing to conflict. The optional [pull on open](#pull-on-open) can fast-forward your branch, which cannot conflict either.
- **Backups in states commit mode must skip.** A detached HEAD, or a rebase or merge you are in the middle of, is backed up safely.
- **Everything is captured.** Staged, unstaged, and untracked files, except what the secrets guard keeps out.
- **Unpushed commits are protected too.** The snapshot history includes your own commits, so work you committed but never pushed is safe on the remote.
- **No hooks run**, so a slow or failing pre-commit hook cannot stop a backup.
- **Nothing is lost offline.** Snapshots keep accumulating locally and are all pushed when the network returns.

The branch name includes the computer's name so two computers never write to the same branch. Branch names are visible to anyone who can see the remote, so on a public repository set `backup.machine` to something other than your real host name.

Each branch is a full snapshot, not a diff. To look at or restore a snapshot:

```bash
# list the backup branches
git fetch origin
git branch -r --list 'origin/autogit/*'

# see what changed in your working directory compared with your branch
git diff HEAD origin/autogit/my-laptop/main

# bring a file back
git checkout origin/autogit/my-laptop/main -- path/to/file
```

If [retention](#17-backup-branch-retention) is configured, old snapshots are automatically pruned by age and count. Without retention, backup branches grow with every change.

Deleting the branch on the remote is not enough: AutoGit keeps its own record of the newest snapshot and recreates the branch, with its full history, on the next change. To start over, delete the remote branch and AutoGit's two local records of it:

```bash
git push origin --delete autogit/my-laptop/main
git update-ref -d refs/autogit/my-laptop/main
git update-ref -d refs/remotes/origin/autogit/my-laptop/main
```

The [secrets guard](#keeping-secrets-out-of-backups) works in both modes.

### Commit messages

By default every automatic commit would carry the same message, which makes `git log` useless for finding anything. So `commit.message` is a **template**: put placeholders in braces and AutoGit fills them in for each backup.

```yaml
commit:
  message: "backup: {summary}"
```

Which gives a history like this:

```text
backup: update main.go
backup: add notes.txt
backup: update main.go, add todo.txt
backup: delete notes.txt
backup: add file1.txt, file2.txt, file3.txt and 2 more
```

| Placeholder | Filled in with |
|---|---|
| `{summary}` | What changed, for example `update main.go, add notes.txt and 2 more`. Up to three file names, shortened if the line would be too long. |
| `{files}` | How many files changed. |
| `{branch}` | The branch you are on, or `detached`. |
| `{host}` | This computer's name: `backup.machine` if set, otherwise the host name. |
| `{repo}` | The repository's folder name. |
| `{date}` | The date, for example `2026-10-26`. |
| `{time}` | The time, for example `14:03:07`. |
| `{datetime}` | Both, for example `2026-10-26 14:03:07`. |

The message can run over several lines, for a subject plus a body:

```yaml
commit:
  message: |
    backup: {summary}

    {files} file(s) saved on {host} at {datetime}
```

How it behaves:

- **A message with no placeholders is used exactly as written.** Existing configurations such as `"chore: automatic backup"` keep working and keep producing identical commits. To get varied messages, add a placeholder.
- **A mistake stops AutoGit at startup.** A mistyped placeholder such as `{sumary}` is rejected with a message listing the valid ones, rather than appearing as literal text in every commit.
- **Braces that are not a placeholder are left alone**, for example `{ like this }` with spaces inside.
- **File names are never treated as placeholders.** A file called `{host}.txt` appears as written.
- **The summary names only what is being saved.** Files the [secrets guard](#keeping-secrets-out-of-backups) kept out are never mentioned, so a message cannot reveal that a `.env` file exists.
- **In commit mode** the summary describes what that commit contains. **In branch mode** it describes what changed since the previous snapshot, not everything since your last commit, so each snapshot describes only what is new.
- **Branch snapshots also carry a second paragraph** saying which branch and commit they were based on.

### Pull on open

When you open a configured editor, AutoGit checks each repository for commits that were pushed from somewhere else and brings them in **before** the first backup. This is what keeps you from editing stale code, or from having a push rejected later because the remote moved on while you were away.

```yaml
sync:
  pull_on_open: true   # the default; set to false to turn it off
```

Leaving the `sync` section out keeps it on.

It is deliberately cautious. It only **fast-forwards**: it moves your branch forward to include the new commits and does nothing else. It never creates a merge commit, rewrites your history, or stashes your work. Your uncommitted edits are left exactly as they are.

| What AutoGit finds | What it does | Log message |
|---|---|---|
| Nothing new upstream | Nothing | `Nothing to pull in <repo>` |
| New commits, and your branch can move forward | Fast-forwards to them | `Pulled 2 new commits into <repo>` |
| Your branch and its upstream each have commits the other lacks | Changes nothing | `Not pulling in <repo>: your branch and its upstream each have commits the other lacks.` |
| Your uncommitted changes touch files that also changed upstream | Changes nothing | `Not pulling in <repo>: your uncommitted changes touch files that also changed upstream (<files>).` |
| A rebase, merge, lock, or detached HEAD | Changes nothing | `Skipping <repo>: <reason>` |
| The remote cannot be reached | Changes nothing | `Could not check <repo> for new commits: <error>` |

When nothing is pulled, the backup still runs. The message also says what to do next:

- **In commit mode**, AutoGit will commit your changes and try to combine the two histories during the next backup, exactly as it always has. If that rebase conflicts, it is aborted and reported.
- **In branch mode**, AutoGit never integrates anything into your branch, so run `git pull` yourself (commit or stash first if your edits overlap).

In branch mode, pull on open is the only thing AutoGit does that changes your branch. If you want branch mode to be strictly read-only, set `sync.pull_on_open: false`.

Pull on open runs once each time an editor opens. It does not run during a session, and the scheduled backups do not pull.

### Example: Back up two repositories

```yaml
interval: "5m"

repositories:
  - path: "/home/your-user/projects/project-one"
  - path: "/home/your-user/projects/project-two"

commit:
  message: "backup: {summary}"

editors:
  - code
  - goland
```

Use paths that actually exist on your system. The example user and project names are placeholders.

### Choosing an interval

- `30s`: frequent checks for active development.
- `1m`: the current example and a practical starting point.
- `5m`: less frequent backup checks.
- `1h`: less frequent checks, with a longer potential delay before changes are backed up.

An interval does not guarantee a push at an exact time. A check can take longer because of network delays, Git hooks, repository size, or errors.

---

## Keeping Secrets Out of Backups

AutoGit saves whatever it finds in your working directory, so a stray `.env` file could be pushed within a minute of being created. To reduce that risk, AutoGit checks every file it is about to save and keeps out the ones that look dangerous. This works in both backup modes.

A kept-out file stays exactly where it is on your disk. It is only left out of the backup. If your changes include both safe and kept-out files, the safe ones are still backed up.

### What is kept out

**By file name**

- Environment files: `.env`, `.env.local`, `.env.production`, `prod.env`, and `.envrc`. Templates ending in `.example`, `.sample`, `.template`, or `.dist` are allowed.
- Private keys and keystores: `id_rsa`, `id_dsa`, `id_ecdsa`, `id_ed25519`, anything ending in `_rsa`, `_dsa`, `_ecdsa`, or `_ed25519`, and files ending in `.key`, `.p12`, `.pfx`, `.jks`, `.keystore`, `.kdbx`, or `.ppk`.
- Credential files: `.netrc`, `.pgpass`, `.htpasswd`, `.pypirc`, `.git-credentials`, `credentials.json`, `secrets.json`, `secrets.yaml`, and `secrets.yml`.
- Terraform state: `*.tfstate` and `*.tfstate.backup`.
- Anything inside a `.ssh`, `.aws`, `.gnupg`, or `.kube` directory.

**By size**

- Files larger than 25 MiB.

**By content**

Text files up to 1 MiB are searched for:

- Private key blocks (RSA, EC, OpenSSH, PGP, and similar).
- AWS access key IDs and AWS secret access key assignments. AWS's own documented example keys are ignored.
- GitHub tokens, Slack tokens, Google API keys, live Stripe keys, Anthropic API keys, and OpenAI-style API keys.
- Solana keypair files: the JSON array of 64 numbers that the Solana CLI writes.

A `.pem` file is checked by its content, so certificates are saved and private keys are not.

Only files that changed since your last commit are checked.

### How you will know

AutoGit logs one warning for each kept-out file, saying why:

```text
AutoGit kept ".env" out of the backup in /path/to/repo: environment file. To back it up anyway, add its path to .autogit-allow in that repository.
```

For content matches the reason names the kind of secret and the line number, never the secret itself. A warning is logged again only when the set of kept-out files changes, so a lingering `.env` does not fill the log every cycle.

### Backing up a file anyway

If a file was kept out by mistake, list it in a file named `.autogit-allow` at the root of that repository, one pattern per line:

```text
# local development settings I do want backed up
.env.local
fixtures/
config/dev.key
```

- A name with no slash, such as `.env.local`, matches in any directory.
- A path with a slash, such as `config/dev.key`, matches from the repository root.
- A leading slash, such as `/dev.key`, matches the repository root only.
- A trailing slash, such as `fixtures/`, allows everything under that directory.
- Blank lines and lines starting with `#` are ignored.

`.autogit-allow` is an ordinary file in your repository, so it is backed up too.

### What this does not do

The guard is a safety net, not a complete secret scanner.

- It recognizes only the file names and token formats listed above. A password in a format it does not know can still be saved.
- It checks only files AutoGit itself is about to save. It does not scan your existing commits. Commits you made by hand are pushed as they are: in commit mode with your branch, in branch mode as part of each snapshot's history.
- It cannot remove a secret that was already pushed. Revoke or rotate it.

---

## Usage

### Normal workflow

1. Start AutoGit.
2. Open one of the configured editors. AutoGit pulls in any new commits from the remote, then backs up right away.
3. Work normally in a configured repository.
4. Leave AutoGit running. It backs up again at every interval.
5. Check the logs for errors, skip messages, or kept-out files if a backup does not appear to complete.
6. Close your editor when you are done. AutoGit runs one final backup.
7. Before switching computers, verify that your latest work is available on the remote.

### Check that your work reached the remote

**In commit mode**, navigate to your project:

```bash
cd /path/to/your/repository
```

Inspect the branch state:

```bash
git status -sb
```

Inspect the latest commit:

```bash
git log -1 --oneline
```

Compare local and upstream commit counts:

```bash
git rev-list --left-right --count HEAD...@{upstream}
```

When the local branch and upstream have matching history, the final command reports:

```text
0       0
```

This verifies that neither side has commits missing from the other at the time of the check. It does not prove that a new backup was performed during the most recent scheduler cycle.

**In branch mode**, your own branch is not pushed, so look at the backup branch instead:

```bash
git fetch origin
git branch -r --list 'origin/autogit/*'
git log -1 --oneline origin/autogit/<machine>/<branch>
```

To see what is in your working directory but not yet in your branch, compared with the newest snapshot:

```bash
git diff HEAD origin/autogit/<machine>/<branch> --stat
```

AutoGit also keeps its own record of the newest snapshot on your computer. List it with:

```bash
git for-each-ref refs/autogit
```

### Offline backup queue

When the network is unavailable, AutoGit can preserve pending backup operations and retry them automatically when connectivity returns. This is useful when working with unreliable internet connections — for example, on a laptop during travel.

**How it works:**

1. When a backup push fails with a network error and the queue is enabled, the operation is stored in a local SQLite database.
2. A background process periodically checks whether the network is available.
3. When connectivity returns, pending operations are retried automatically.
4. If a backup succeeds normally (not from the queue), any matching queued entry is cleared.
5. After the configured number of failed retries, an entry is marked as permanently failed.

**Enabling the queue:**

Add the following to `config.yaml`:

```yaml
offline_queue:
  enabled: true
  retry_delay: "1m"
  max_retries: 10
```

The queue database is stored at `~/.config/autogit/queue.db` by default. You can change the directory with `offline_queue.path`.

**Checking queue status:**

```bash
autogit queue
```

Example output:

```
AutoGit Offline Backup Queue Status
====================================

Pending:   2
Completed: 5
Failed:    0

Pending backups:
  /home/user/projects/my-app                a1b2c3d4  (retry: 1)
  /home/user/projects/website                e5f6a7b8  (retry: 0)

Recently completed:
  /home/user/projects/my-app                9c8d7e6f  (completed: 2026-11-03 10:15:22)
```

**Duplicate prevention:**

The queue uses the repository path and commit hash as a unique key. If the same commit is already queued (pending or retrying), it is not added again. This prevents uploading the same snapshot multiple times.

### Verify backups

Run `autogit verify` to check that all configured repositories have valid, recoverable backups on their remotes:

```bash
autogit verify
```

Example output:

```
AutoGit Backup Verification
=============================

/home/user/projects/my-app: ✓ OK
  Remote reachable: yes
  Backup reference: exists
  Commit hash: matches
  Snapshot fetchable: yes

/home/user/projects/website: ✗ FAILED
  Remote reachable: yes
  Backup reference: missing
  Issues:
    - backup reference does not exist on the remote
```

For each repository, verification checks:

- **Remote reachability** — Can AutoGit contact the remote?
- **Backup reference** — Does the expected backup reference exist on the remote?
- **Commit hash** — If an expected hash is known, does it match the remote?
- **Snapshot fetchability** — Are the commit, tree, and blob objects accessible?

If any issue is found, a desktop notification is sent (if notifications are enabled).

To run verification automatically, enable it in `config.yaml`:

```yaml
verification:
  enabled: true
  interval: "24h"
```

AutoGit will then verify all repositories on the configured schedule, independently of whether an editor is open.

### Restore from backups

When you need to recover lost work, `autogit restore` lets you browse and restore from previous backup snapshots without manually searching through Git history. This works with **branch mode** backups stored on `autogit/<machine>/<branch>`.

**List available snapshots:**

```bash
autogit restore --op list
```

This shows all snapshots across all configured repositories, sorted newest-first, with the commit hash, date, originating machine, branch, and commit message. Filter by machine or branch:

```bash
autogit restore --op list --machine mylaptop --branch main
```

**Preview changes in a snapshot:**

Before restoring, see what files differ between a snapshot and your current working tree:

```bash
autogit restore --op show --hash abc1234 --repo /path/to/repo
```

**Compare two snapshots:**

See what changed between two backup snapshots:

```bash
autogit restore --op diff --from abc1234 --to def5678 --repo /path/to/repo
```

Or compare a snapshot against your current working tree:

```bash
autogit restore --op diff --from abc1234 --to WORKTREE --repo /path/to/repo
```

**Restore a single file:**

Extract one file from a snapshot without affecting the rest of your working tree:

```bash
autogit restore --op file --hash abc1234 --file src/main.go --dest ./restored/main.go --repo /path/to/repo
```

**Restore a complete snapshot to a directory:**

The safest way to inspect a full snapshot is to restore it to a separate directory:

```bash
autogit restore --op restore --hash abc1234 --dir ./snapshot-abc1234 --repo /path/to/repo
```

This creates `./snapshot-abc1234/` with all files from that snapshot, with no Git metadata.

**Restore a complete snapshot into the working tree:**

To overwrite your current working tree with a snapshot, use `--force`. AutoGit will preview the changes first:

```bash
autogit restore --op restore --hash abc1234 --force --repo /path/to/repo
```

This updates tracked files in place without moving HEAD. Untracked files are not affected.

### Cleanup backup snapshots

Use `autogit cleanup` to preview or execute backup branch cleanup. By default it runs in dry-run mode, showing which snapshots would be kept, pruned, or protected:

```bash
# Preview cleanup (default, safe)
autogit cleanup

# Preview for a specific repository
autogit cleanup --repo /path/to/repo

# Execute cleanup, removing pruned snapshots
autogit cleanup --force

# Execute cleanup for a specific repository
autogit cleanup --repo /path/to/repo --force
```

Example output:

```
/home/user/projects/my-app (15 snapshots)
------------------------------------------------------------------------------------------
Hash          Date                 Status
------------------------------------------------------------------------------------------
a1b2c3d4e5f6  2026-11-03 14:30     keep
b2c3d4e5f6a1  2026-11-03 10:15     keep
c3d4e5f6a1b2  2026-11-02 18:45     keep
d4e5f6a1b2c3  2026-10-15 09:20     protected
e5f6a1b2c3d4  2026-09-01 12:00     prune

Summary: 3 kept, 11 pruned, 1 protected

--- Dry run: no changes made ---

Total: 15 snapshots, 3 kept, 11 pruned, 1 protected
```

Snapshots marked `protected` are ancestors of active user branches and will never be pruned.

**Common flags:**

| Flag | Description |
|------|-------------|
| `--op` | Operation: `list`, `show`, `diff`, `file`, or `restore` |
| `--hash` | Snapshot commit hash |
| `--from` | Source snapshot hash (for `diff`) |
| `--to` | Target snapshot hash or `WORKTREE` (for `diff`) |
| `--file` | File path within the snapshot (for `file`) |
| `--dest` | Destination path for restored file (for `file`) |
| `--dir` | Destination directory for full restore |
| `--force` | Restore into the current working tree |
| `--machine` | Filter by machine name (for `list`) |
| `--branch` | Filter by branch name (for `list`) |
| `--repo` | Target a specific repository path |
| `--config` | Path to the configuration file |

### Stop AutoGit

In the terminal running AutoGit, press:

```text
Ctrl+C
```

This stops the running process. It does not uninstall AutoGit or remove your repositories. It also does not run a final backup, so if you have unsaved progress, close your editor first and let the final backup finish.

### Running AutoGit in the background

AutoGit can run as a systemd user service so it starts automatically at login and restarts after failures. This means you do not need to remember to start it in a terminal.

**Battery impact is minimal.** AutoGit only performs Git operations while a configured editor is open. When no editor is running, it sleeps between short process checks. The service is configured with `RestartSec=60` and `StartLimitBurst=3` to avoid aggressive restart loops.

#### Install the service

Build the binary and install the service:

```bash
go build -o autogit ./cmd/autogit
./autogit install
```

The `install` command creates a systemd user service file that points to your binary and configuration. It looks for `config.yaml` in the current directory or in `~/.config/autogit/`. You can also specify a config file directly:

```bash
./autogit install --config /path/to/config.yaml
```

After installing, enable and start the service:

```bash
systemctl --user daemon-reload
systemctl --user enable --now autogit.service
```

#### Check the service status

```bash
systemctl --user status autogit.service
```

#### View logs

```bash
journalctl --user -u autogit.service -f
```

#### Uninstall the service

```bash
./autogit uninstall
```

This stops the service, removes the service file, and reloads the systemd daemon.

---

## Synchronization and Conflict Handling

The first part of this section describes **commit mode**. [Branch mode](#in-branch-mode) works differently and is covered at the end.

### When local changes exist

AutoGit stages local changes and runs the secrets guard, then commits what is left, before fetching remote history. This gives local work a commit that can be replayed if rebasing is needed.

It then fetches the remote, checks for missing upstream commits, rebases when necessary, and attempts to push.

### When there are no local changes

AutoGit skips creating a new commit. It can still fetch remote history, reconcile a branch that is behind its upstream, and attempt to push.

### When the remote contains new commits

AutoGit fetches the remote and checks whether the upstream has commits missing locally. If it does, AutoGit attempts to rebase the local branch onto the upstream.

### When a rebase conflict occurs

If Git cannot complete the rebase, AutoGit attempts to abort it and reports the error. The intention is to leave the local commit and working tree available for manual resolution, rather than pushing after a failed rebase.

After a conflict, inspect the repository carefully:

```bash
git status
git log --oneline --graph --decorate -10
```

Resolve the conflict using normal Git workflows. Confirm the branch is in a valid state and synchronized before relying on subsequent backups.

**AutoGit does not merge conflicting code or decide which version of a file should win.**

### When a push fails

A failed push does not mean the local commit has disappeared. It may remain locally and be pushed during a later successful cycle, provided the repository state and remote history permit it.

Temporary network failures are retried automatically, up to 3 attempts. A rejected push, such as a non-fast-forward, is not retried.

Check the logs and compare local and remote history rather than assuming that every scheduled check successfully reached the remote.

### When an editor opens

Before the first backup, AutoGit fast-forwards the repository to its upstream if the remote has new commits. It never merges, rebases, or stashes for this step, and it leaves everything as it is when your branch has diverged or your uncommitted edits touch the incoming files. In commit mode the next backup then deals with the divergence in the usual way. See [Pull on open](#pull-on-open).

### Repository states AutoGit skips (commit mode)

In commit mode, `git add .` followed by `git commit` is only safe in a repository that is not in the middle of something. Run during a merge conflict, it would mark files containing conflict markers as resolved and commit them. So before it touches anything, AutoGit checks the repository and skips the cycle, without treating it as a failure, in these cases:

| Reason logged | What it means |
|---|---|
| `a rebase is in progress` or `a rebase or patch application is in progress` | You are in the middle of a rebase. |
| `a merge is in progress` | A merge has not been finished. |
| `a cherry-pick is in progress`, `a revert is in progress`, `a bisect is in progress` | The matching Git operation is unfinished. |
| `the repository has unresolved merge conflicts` | Some files are still conflicted, even with no operation in progress, for example after `git stash pop`. |
| `another git process is using the repository` | An `index.lock` file exists: your IDE or a Git command is running. |
| `HEAD is detached (not on a branch)` | Commits made here would belong to no branch and could never be pushed. |

Each skip is logged as:

```text
Skipping /path/to/repo: a merge is in progress
```

The next cycle checks again, so backups resume by themselves once you finish the operation. Nothing is staged, committed, or pushed in the meantime.

If `index.lock` is still reported when no Git command is running, it is probably left over from a crash. AutoGit never deletes it for you. Check that no Git process is running, then remove it yourself:

```bash
rm /path/to/repo/.git/index.lock
```

If another Git process takes the lock after AutoGit's check but before its own command runs, that failure is also treated as a skip.

### Missing Git identity

If `user.name` or `user.email` is not set, AutoGit stops before staging anything and reports how to fix it. This is an error, not a skip, because it needs you to act:

```bash
git config user.name "Your Name"
git config user.email "you@example.com"
```

### In branch mode

Branch mode avoids most of the above because its backup only reads your files and never changes your branch:

- There is no rebase, merge, or conflict to handle, since AutoGit never integrates anything into your branch.
- A detached HEAD, a rebase or merge in progress, and unresolved conflicts are all backed up. The snapshot is taken of whatever is in your working directory, conflict markers included. A detached HEAD is backed up under the branch name `detached`.
- The only shared state is Git's own refs. If another Git process holds a lock on one, the cycle is skipped and retried.
- A missing Git identity is still reported, because creating the snapshot commit needs one.
- If the fetch fails, the snapshot is still taken and kept locally, and the failure is reported. A later cycle pushes everything that is waiting.

Your own branch on the remote is never pushed or changed in branch mode. Push it yourself as usual.

---

## Testing

Run the complete Go test suite from the project root:

```bash
go test ./...
```

The suite covers the project's configuration, editor, Git, and scheduler packages, and the editor-monitoring loop in `cmd/autogit`.

To run tests with Go's race detector on a supported platform:

```bash
go test -race ./...
```

The race-detector command is an additional check; its successful execution should be verified in your environment.

### Test isolation

The Git tests create throwaway repositories in temporary directories, and every Git command they run uses a temporary home directory. Your own Git configuration, such as a global ignore file that already ignores `.env`, cannot change the results, and the tests never touch your real repositories.


### Automated scenario coverage

Beyond the basics, the tests exercise these scenarios against real Git repositories with a local bare repository as the remote:

- **Synchronization:** a remote that is ahead, a rebase of local and remote commits, a rebase conflict that is aborted without pushing, and a push that is retried after a temporary failure.
- **Timeouts and retries:** a fetch that hangs is stopped, a timeout is not retried, and temporary versus permanent errors are told apart.
- **Secrets guard:** each file-name rule, each content pattern, the size limit, the `.autogit-allow` list, file names containing glob characters, deleted files, a repository with no commits yet, and warnings that are logged once.
- **Repository states:** a rebase, merge, unresolved conflict, held index lock, and detached HEAD are skipped and left untouched, and a missing Git identity is reported.
- **Branch mode:** your branch, index, and files are left alone, snapshots chain together, unpushed commits are included, a detached HEAD, a rebase, a merge, and a `stash pop` conflict are backed up, secrets are kept out, work is not lost offline, a reinstall and diverged histories are handled, hooks do not run, and branch names containing slashes are safe.
- **Commit messages:** each placeholder is filled in, a summary names up to three files and says a shared action once, a message without placeholders is unchanged, a multi-line message works, a file named like a placeholder is not expanded, kept-out files are never named, a snapshot describes only what changed since the previous one, and a mistyped placeholder is rejected at startup.
- **Pull on open:** new commits are fast-forwarded in with no merge commit, your staged, unstaged, and untracked work is kept, and nothing is changed when the branch has diverged, when your edits overlap incoming files, when a rebase or merge is in progress, on a detached HEAD, or when the remote is unreachable.
- **Editor monitoring:** the pull runs before the opening backup and again on each open, a backup on open, a final backup on close, no final backup when AutoGit is stopped, and backups that never overlap.
- **Configuration:** where the file is found, the `--config` flag, the backup section's validation, retention settings, notification toggle, and a check that `config.example.yaml` always loads.
- **Notifications:** enabled and disabled sender behavior.
- **Concurrency protection:** exclusive lock acquisition, concurrent backup serialization, lock release after errors.

### Practical test coverage

The current version has also been exercised by hand with these scenarios:

- Detecting configured editor processes.
- Enabling and pausing scheduled backups according to editor activity.
- Committing and pushing local changes.
- Skipping unnecessary new commits when there are no local changes.
- Fetching remote-only commits.
- Retrying a push after a temporary remote failure.
- Rebasing local and remote histories when they have diverged.
- Aborting a failed rebase without proceeding to push.
- Confirming the final local branch matches its upstream after successful synchronization.
- Automatically committing and pushing a test file's creation and deletion.

Practical testing is valuable, but it does not guarantee correct behavior in every repository state or network condition.

---

## Troubleshooting

### AutoGit keeps waiting for an editor

Check that the editor is open and its actual process name matches an entry in `editors`.

For example:

```bash
pgrep -a -x goland
```

If the process name differs, update the configuration accordingly.

### AutoGit cannot find the configuration file

AutoGit looks for a configuration file in this order: the path given with `--config`, then `config.yaml` in the current directory, then `~/.config/autogit/config.yaml`. If none exists it stops and lists every place it looked.

Create one from the template:

```bash
cp config.example.yaml config.yaml
```

Or point AutoGit at a file directly:

```bash
./autogit --config /path/to/config.yaml
```

If you pass `--config` and the file does not exist, AutoGit stops with an error rather than falling back to another file.

### AutoGit stops at startup with `commit.message uses {...}`

The commit message contains a word in braces that is not a placeholder, usually a typo. The error names it and lists the valid ones:

```text
commit.message uses {sumary}, which is not a placeholder; use one of: {summary}, {files}, {branch}, {host}, {repo}, {date}, {time}, {datetime}
```

Correct the spelling in `config.yaml`. If you meant to write literal braces, put a space inside them, as in `{ text }`.

### The log says `Skipping <repository>: ...`

AutoGit found the repository in the middle of something and left it alone on purpose. It is not a failure, and backups resume by themselves once the state clears. See [Repository states AutoGit skips](#repository-states-autogit-skips-commit-mode) for each reason. If you want backups to continue in those states, use [branch mode](#backup-modes).

### The log says `Not pulling in <repository>`

AutoGit found new commits on the remote when your editor opened but could not simply fast-forward to them, so it left your repository exactly as it was. The message gives the reason:

- **Your branch and its upstream each have commits the other lacks.** You committed here while someone, or another computer, pushed there. In commit mode the next backup tries to combine them. Otherwise run `git pull --rebase` or `git pull` yourself.
- **Your uncommitted changes touch files that also changed upstream.** The files are named in the message. Commit or stash your changes, then run `git pull`.

To see what is waiting on the remote:

```bash
git fetch origin
git log --oneline HEAD..@{upstream}
```

You can turn the check off with `sync.pull_on_open: false`.

### A file is not being backed up

First look for a warning in AutoGit's log. If the secrets guard kept the file out, the log says why:

```text
AutoGit kept ".env" out of the backup in /path/to/repo: environment file. ...
```

To back it up anyway, add its path to `.autogit-allow` in that repository. See [Keeping Secrets Out of Backups](#keeping-secrets-out-of-backups).

If there is no warning, check whether Git is ignoring the file, and which rule is responsible:

```bash
git check-ignore -v path/to/file
git status --short --ignored
```

Ignored files are not included by `git add`. Check repository-level `.gitignore` files and your global Git ignore configuration. A global ignore file is easy to forget about.

### `git cannot commit ... because user.name and user.email are not set`

Git has no identity for that repository, so AutoGit stopped before staging anything. Set one:

```bash
git config user.name "Your Name"
git config user.email "you@example.com"
```

Use `git config --global ...` to set it for every repository.

### `another git process is using the repository` keeps appearing

An `index.lock` file exists. If your IDE or a Git command is running, this is normal and clears by itself. If it persists with nothing running, the file is left over from a crash. Check that no Git process is running, then remove it:

```bash
rm /path/to/repo/.git/index.lock
```

### Git fetch or push fails

Check that Git works independently:

```bash
git remote -v
git status -sb
git fetch origin
```

Verify remote URLs, authentication, network connectivity, and the configured upstream.

AutoGit runs Git without interactive prompts. If Git needs a password or passphrase it fails immediately with a message such as `terminal prompts disabled` or `could not read Username`. Set up a credential helper or an SSH agent so that Git can authenticate on its own.

Do not share access tokens, passwords, or private keys when requesting help.

### `git fetch timed out after 2m0s`

A Git command took longer than AutoGit allows (30 seconds for quick local commands, 2 minutes for fetch, push, add, commit, and rebase) and was stopped. This usually means a stalled network connection or a very large repository. Timeouts are not retried within the same cycle; the next scheduled cycle tries again.

### The local and remote branches have diverged

Inspect the history:

```bash
git log --oneline --graph --decorate --all -15
```

If AutoGit reports a failed rebase, inspect the repository status and resolve the issue manually. Do not repeatedly force-push to bypass a conflict.

### I cannot find my backup branch (branch mode)

Backup branches are named `autogit/<machine>/<branch>`, where `<machine>` is `backup.machine` or your computer's host name, and slashes in your branch name become `+`. List them:

```bash
git fetch origin
git branch -r --list 'origin/autogit/*'
git ls-remote --heads origin 'autogit/*'
```

AutoGit also keeps a local record of the newest snapshot:

```bash
git for-each-ref refs/autogit
```

### I deleted a backup branch and it came back (branch mode)

AutoGit remembers the newest snapshot on your computer, so deleting only the remote branch makes it recreate the branch on the next change, with its full history. To start the backup branch over, delete the remote branch and AutoGit's two local records of it:

```bash
git push origin --delete autogit/<machine>/<branch>
git update-ref -d refs/autogit/<machine>/<branch>
git update-ref -d refs/remotes/origin/autogit/<machine>/<branch>
```

The next backup creates a fresh branch from your current work.

### A backup check completes but the latest commit is missing remotely

A generic completion message alone is not proof that a new commit was pushed. Inspect AutoGit's logs, then compare local and remote state:

```bash
git status -sb
git log -1 --oneline
git rev-list --left-right --count HEAD...@{upstream}
```

In branch mode your own branch is not pushed. Check the backup branch instead, as described in [Usage](#check-that-your-work-reached-the-remote).

Investigate any reported errors before assuming the backup succeeded.

---

## Limitations

AutoGit is intended to automate routine Git operations, not to replace Git knowledge, remote hosting, or a comprehensive backup strategy.

### 1. It is not continuous file synchronization

AutoGit runs scheduled checks. It does not immediately mirror every keystroke or file change to another computer.

A change made just after a check may remain local until the next cycle.

### 2. It depends on a configured editor being open

In the current implementation, scheduled backups are enabled only while a configured editor process is detected.

AutoGit backs up when an editor opens and once more when the last one closes, so changes made while no editor is open are picked up at the next open. Two things can still leave work unsaved:

- Stopping AutoGit with `Ctrl+C` or `SIGTERM` does not run a final backup.
- AutoGit checks for editors every 5 seconds. Closing and reopening an editor within one check is not noticed, so no final backup runs.

### 3. Conflicts require manual intervention

In commit mode, AutoGit attempts to rebase divergent histories but does not resolve content conflicts automatically.

A failed rebase is reported and aborted where possible. The user must resolve the underlying issue before synchronization can succeed.

Branch mode never integrates anything into your branch, so it has no conflicts of this kind.

### 4. It requires Git and working remote access

AutoGit depends on the Git executable, local repository state, configured remotes, credentials that work without prompting, and network connectivity.

It cannot guarantee that a commit reaches the remote when any of these dependencies fail.

### 5. Automatic staging may include unintended files

Commit mode stages with `git add .` and branch mode with `git add -A .`. This can include any eligible, non-ignored file in the repository, not just source files you intended to publish.

The [secrets guard](#keeping-secrets-out-of-backups) keeps out files that match its rules, but it cannot know what you consider private. Use `.gitignore` appropriately and review the repository before enabling AutoGit on sensitive projects.

### 6. It does not replace secure secret management

The secrets guard is a safety net, not a secret scanner. It recognizes a fixed list of file names and token formats. A password, token, or key in a format it does not know can still be committed and pushed.

It checks only files AutoGit itself is about to save. It does not scan commits you made yourself, and those are pushed as they are: in commit mode with your branch, in branch mode as part of each snapshot's history.

Never rely solely on a global Git ignore file for secrets. Review repository-level ignore rules, and do not place sensitive material in tracked files.

### 7. It does not provide a complete backup retention system

AutoGit uses your Git repository and remote. It does not independently create a versioned offline archive or guarantee recovery if both the local repository and remote hosting account are lost or compromised.

In branch mode, [backup branch retention](#17-backup-branch-retention) can automatically prune old snapshots by age, count, and calendar policies. It also protects snapshots that are ancestors of active user branches. Use `autogit cleanup` to preview or execute cleanup manually.

Maintain independent backups for important work.

### 8. The editor monitor is Linux-oriented

The current editor detector uses `pgrep`. The documented process names and behavior target Linux environments.

Native Windows and macOS support should not be assumed without adapting and testing process detection for those systems.

### 9. Initial branch and upstream configuration matter

AutoGit assumes a usable Git repository and an `origin` remote.

In commit mode, existing branches should have an appropriate upstream configured for remote synchronization. A new branch or repository with no upstream may need an initial push or manual setup, depending on its current state and remote configuration.

Branch mode pushes to `origin` by name and does not need an upstream.

### 10. A successful scheduler cycle is not a universal guarantee

A cycle can encounter an error while committing, fetching, rebasing, or pushing. Review errors and verify the remote state when it matters.

### 11. It is not a collaboration or locking system

AutoGit does not lock files, coordinate simultaneous editing across computers, or prevent two users or devices from modifying the same code concurrently. Git's normal synchronization and conflict mechanisms still apply.

### 12. Background service requires systemd

AutoGit can run as a systemd user service (see [Running AutoGit in the background](#running-autogit-in-the-background)). If your system does not use systemd, you will need to arrange for AutoGit to start at login through another mechanism.

### 13. Branch mode does not push your own branch

Branch mode protects your work on a separate backup branch. It never pushes or updates your own branch on the remote. Your commits are safe on the backup branch, but to share them with others or another computer through your normal branch, push it yourself.

### 14. Skipped and kept-out files are not backed up

A repository that is skipped in commit mode is not backed up until its state clears. A file the secrets guard keeps out is not backed up at all until you allow it in `.autogit-allow`. Watch AutoGit's log for both.

### 15. A final backup can take time

The backup that runs when you close your editor is a normal backup. On a slow or stalled connection it can take as long as AutoGit's timeouts allow before it gives up.

### 16. Pull on open only fast-forwards, and only on open

If your branch has diverged from its upstream, or your uncommitted edits touch the same files as the incoming commits, nothing is pulled and you have to resolve it (or, in commit mode, wait for the next backup to try). Commits pushed from another computer in the middle of a session are not pulled in until the next time an editor opens. In branch mode, enabling pull on open means AutoGit can move your branch forward.

### 17. Commit messages say which files changed, not why

`{summary}` is built from file names and whether each was added, updated, or deleted. It cannot describe what changed inside a file or the reason for the change. Write your own commits for work you want explained.

---

## Security Considerations

Before using AutoGit on important projects:

- Configure `.gitignore` to exclude secrets, generated files, dependencies, and other files that should not be committed.
- Treat the [secrets guard](#keeping-secrets-out-of-backups) as a backstop, not as protection you can rely on alone.
- Review existing tracked files for accidentally committed credentials.
- Ensure remote authentication is configured securely, and use a credential helper or SSH agent, since AutoGit cannot prompt for a password.
- Use a private repository for anything sensitive. A backup branch contains a full snapshot of your working tree, minus ignored and kept-out files, so anyone who can read the repository can read your snapshots.
- Remember that branch names are visible to anyone who can see the remote. In branch mode the name includes your computer's name, so on a public repository set `backup.machine` to something other than your real host name.
- Be careful with `.autogit-allow`. Every pattern in it switches off the guard for matching files.
- Do not use force-push as a routine workaround for synchronization failures.
- Keep independent backups of important projects.
- Verify remote synchronization before switching computers or deleting local copies.
- Treat automatic commits as real commits: in commit mode they become part of your branch's history and may be pushed to other collaborators. In branch mode they live on a separate branch, but they are still pushed to the remote.

If a secret has already been committed or pushed, simply deleting the file in a later commit does not erase the secret from earlier history. Revoke or rotate exposed credentials and follow an appropriate history-remediation process.

---

## Project Structure

The current project is organized into Go packages with separate responsibilities:

```text
auto-git/
├── cmd/
│   └── autogit/
│       ├── main.go
│       ├── main_test.go
│       └── install.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   ├── commit_message_test.go
│   │   ├── config_test.go
│   │   ├── locate.go
│   │   ├── locate_test.go
│   │   ├── backup_test.go
│   │   └── pull_on_open_test.go
│   ├── editor/
│   │   ├── editor.go
│   │   └── editor_test.go
│   ├── git/
│   │   ├── git.go
│   │   ├── git_test.go
│   │   ├── guard.go
│   │   ├── guard_test.go
│   │   ├── lock.go
│   │   ├── lock_test.go
│   │   ├── message.go
│   │   ├── message_test.go
│   │   ├── pull.go
│   │   ├── pull_test.go
│   │   ├── retry.go
│   │   ├── retry_test.go
│   │   ├── snapshot.go
│   │   ├── snapshot_test.go
│   │   ├── restore.go
│   │   ├── restore_test.go
│   │   ├── verify.go
│   │   ├── verify_test.go
│   │   ├── health.go
│   │   ├── state.go
│   │   ├── state_test.go
│   │   └── sync_test.go
│   ├── notify/
│   │   ├── notify.go
│   │   └── notify_test.go
│   ├── power/
│   │   ├── power.go
│   │   └── power_test.go
│   ├── queue/
│   │   ├── queue.go
│   │   ├── queue_test.go
│   │   ├── processor.go
│   │   └── processor_test.go
│   └── scheduler/
│       ├── scheduler.go
│       └── scheduler_test.go
├── .github/
│   └── workflows/
│       └── ci.yaml
├── config.example.yaml
├── .gitignore
├── go.mod
├── go.sum
└── README.md
```

Your own `config.yaml` is not part of the repository. It is created from `config.example.yaml` and is listed in `.gitignore`.

Some files may differ as the project evolves. This layout describes the current known organization.

### Package responsibilities

- `cmd/autogit`: application entry point, command-line flags, configuration loading, the editor-monitoring loop that triggers the opening and closing backups, and the `install`/`uninstall` subcommands for systemd service management.
    - `install.go`: installing and removing the systemd user service.
- `internal/config`: configuration parsing, validation, and finding the configuration file.
- `internal/editor`: detection of configured editor processes.
- `internal/git`: everything that touches a repository.
    - `git.go`: running Git with timeouts, the backup modes, and the commit-mode backup.
    - `snapshot.go`: the branch-mode backup.
    - `restore.go`: listing, diffing, and restoring from backup snapshots.
    - `verify.go`: checking that backups are recoverable on the remote.
    - `health.go`: repository health checks (path, remote, authentication, sync status).
    - `guard.go`: the secrets guard and the `.autogit-allow` list.
    - `message.go`: commit message templates and the summary of what changed.
    - `pull.go`: bringing in new remote commits when an editor opens.
    - `state.go`: repository-state checks, skipped cycles, and the identity check.
    - `retry.go`: retrying temporary network failures.
    - `lock.go`: exclusive per-repository file locks to prevent concurrent AutoGit instances from racing.
- `internal/notify`: desktop notifications with audible alerts using the `beeep` library.
- `internal/power`: battery state detection for power-aware scheduling.
- `internal/queue`: offline backup queue that survives restarts.
- `internal/scheduler`: scheduled backup execution and lifecycle management.

---

## Roadmap

Possible future improvements include:

- **Service management improvements:** better error reporting and status commands for the systemd service.
- **Cross-platform editor monitoring:** support native process detection on macOS and Windows.
- **Improved backup reporting:** clearer per-repository status and explicit success or failure summaries.
- **Configurable secrets guard:** let the file-name rules, content patterns, and size limit be changed in `config.yaml`, and add a review mode before committing.
- **Per-repository settings:** choose the backup mode, and other options, for each repository separately.
- **Retry policies:** the current retries use a fixed 2-second delay and 3 attempts. Backoff and configurable limits are not implemented.
- **Richer commit messages:** describe changes by content, for example which functions changed, instead of by file name.
- **Pulling during a session:** check for new remote commits on a schedule, not only when an editor opens.
- **Optional final backup on shutdown:** run a last backup when AutoGit itself is stopped.
- **Conflict recovery guidance:** clearer instructions for recovering after failed synchronization.
- **Backup history:** record when each repository was checked and whether changes reached the remote.
- **Configuration improvements:** allow editor monitoring to be disabled for users who prefer scheduled backups regardless of editor state.

These are potential future improvements, not claims about features already implemented.

---

## Contributing

Contributions, bug reports, and suggestions are welcome.

When reporting an issue, include:

- Your operating system and Go version.
- The relevant AutoGit log messages.
- A description of the repository state.
- Steps to reproduce the issue.

Remove access tokens, passwords, private keys, and other sensitive information before sharing logs or configuration.

Run the tests before submitting changes:

```bash
go test ./...
```

## License

Add the project's chosen license and its corresponding license file before distributing AutoGit. Until a license is included, do not assume that the repository grants permission for unrestricted redistribution or reuse.
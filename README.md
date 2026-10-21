# AutoGit

**Automatic Git backups and remote synchronization for your development projects.**

AutoGit is a lightweight Go application that automates routine Git operations while you work. It monitors configured code editors, periodically checks configured repositories for changes, commits local work, fetches remote updates, and pushes changes to your Git remote.

The goal is simple: reduce the chance of forgetting to commit and push your work before switching computers or ending a development session.

> **Project status:** Working prototype with practical testing completed on a real Git repository. Remote synchronization, automatic commits and pushes, editor monitoring, and safe handling of rebase conflicts have been tested. Review the limitations below before relying on AutoGit as your only backup solution.

---

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [How AutoGit Works](#how-autogit-works)
- [Requirements](#requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [Usage](#usage)
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

When developing across multiple computers, it is easy to forget to push your latest work to a remote repository. This can leave your work unavailable on another machine or create divergent Git histories when both machines have new commits.

AutoGit is designed to automate this routine workflow.

For example:

1. You open GoLand to work on a Go project.
2. AutoGit detects that a configured editor is running.
3. Its scheduler starts checking your configured repositories at the specified interval.
4. When it detects local changes, it stages and commits them.
5. It fetches the latest history from the remote repository.
6. If the remote contains commits missing locally, AutoGit attempts to rebase local history onto the remote history.
7. It pushes the resulting history to the remote.
8. When all configured editors close, scheduled backups pause.

AutoGit uses Git rather than implementing its own version-control system. Your repositories, commits, branches, and remotes remain ordinary Git repositories.

## Features

### 1. Automatic change detection

AutoGit checks configured repositories for changes using Git's working-tree status.

It detects tracked-file modifications, deletions, and untracked files that Git has not been configured to ignore.

When changes exist, AutoGit stages them and creates a commit using the configured commit message.

### 2. Automatic commits

AutoGit creates commits without requiring you to run `git add` and `git commit` manually.

The default configuration uses:

```text
chore: automatic backup
```

You can change the message in `config.yaml`.

AutoGit skips creating a commit when there are no local changes to commit.

### 3. Automatic remote synchronization

AutoGit fetches updates from the configured repository's `origin` remote.

It checks whether the current branch is behind its configured upstream and attempts to rebase when remote commits are missing locally.

After successful synchronization, it attempts to push the current branch.

### 4. Divergent history handling

When local and remote branches contain different commits, AutoGit attempts to rebase the local commits onto the upstream history.

This helps preserve both sets of commits when their changes can be reconciled.

If the rebase fails, AutoGit attempts to abort it and reports the failure instead of proceeding to push conflicting history.

**Important:** AutoGit does not automatically resolve merge or rebase conflicts. Manual intervention may be necessary before another successful synchronization.

### 5. Scheduled backups

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

### 6. Editor-aware scheduling

AutoGit monitors configured editor processes and enables scheduled backups while at least one configured editor is running.

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

When no configured editor is running, AutoGit pauses scheduled backups. When a configured editor opens again, scheduling resumes.

This reduces unnecessary background activity when you are not working in a configured editor.

### 7. Multiple repository support

The configuration accepts a list of repository paths. AutoGit can therefore be configured to check multiple local Git repositories.

Each repository must exist and be a valid Git working tree.

### 8. Configurable YAML settings

AutoGit uses a YAML configuration file to define the backup interval, repositories, commit message, and editor processes.

This keeps common settings separate from the Go implementation.

### 9. Error reporting

AutoGit reports operational failures encountered during Git commands, including failures while fetching, rebasing, or pushing.

A failed operation is not the same as a successful backup. Review the logs and repository state when an error is reported.

### 10. Automated tests

The project includes tests for configuration, editor detection, Git operations, and scheduling.

The full test suite can be run with:

```bash
go test ./...
```

The project has also been exercised against a real repository to test automatic commits, pushing local changes, retrieving remote changes, divergent histories, and rebase-conflict handling.

---

## How AutoGit Works

The general workflow is:

```text
Start AutoGit
     |
     v
Load configuration
     |
     v
Monitor configured editors
     |
     +---- No editor open ----> Pause scheduled backups
     |                                |
     |                         Check again periodically
     |
     +---- Editor open -------> Enable scheduler
                                      |
                                      v
                              Check repositories
                                      |
                                      v
                              Detect local changes
                                      |
                         +------------+------------+
                         |                         |
                     Changes                    No changes
                         |                         |
                  Stage and commit          Skip new commit
                         |                         |
                         +------------+------------+
                                      |
                                      v
                               Fetch from origin
                                      |
                                      v
                         Check upstream divergence
                                      |
                                      v
                         Rebase if remote is ahead
                                      |
                                      v
                                  Push
                                      |
                                      v
                              Wait for next cycle
```

This is a conceptual overview. Actual command execution and error handling determine whether a particular cycle completes successfully.

### Git operations

AutoGit relies on Git commands for its core operations. The implementation includes operations corresponding to:

- `git status --porcelain` to detect changes.
- `git add .` to stage changes.
- `git commit` to create a commit.
- `git fetch origin` to retrieve remote history.
- Git revision and upstream checks to determine whether remote commits are missing locally.
- `git rebase @{upstream}` to reconcile history when necessary.
- `git push -u origin HEAD` to push the current branch.

Because AutoGit uses Git directly, your usual Git configuration, credentials, ignore rules, hooks, and remote settings can affect its behavior.

---

## Requirements

Before installing AutoGit, ensure your environment has:

- **Go:** a version compatible with the project's `go.mod`. Development and testing have been performed with Go 1.26.x.
- **Git:** installed and accessible through your shell's `PATH`.
- **A Git repository:** the repository you want to back up must already exist locally.
- **A remote repository:** typically GitHub or another Git server configured as `origin`.
- **Git authentication:** credentials or SSH configuration that allow Git to fetch and push.
- **Linux:** the current editor-monitoring implementation uses `pgrep`, so the documented setup targets Linux.

For remote synchronization, the current branch should have a configured upstream once its initial push has been established.

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

### 3. Review the configuration

Open `config.yaml` in your editor and update the repository paths, editor names, interval, and commit message to match your environment.

Do not assume the example repository path exists on your machine.

### 4. Run AutoGit

From the project root:

```bash
go run ./cmd/autogit
```

A successful startup should display messages similar to:

```text
AutoGit configuration loaded successfully.
Backup interval: 1m
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

Then run it from the project root:

```bash
./autogit
```

Keep the configuration file available at the location expected by the application. The simplest approach is to launch AutoGit from the project root, where `config.yaml` resides.

---

## Configuration

AutoGit's main configuration file is `config.yaml`.

### Example configuration

```yaml
interval: "1m"

repositories:
  - path: "/home/bishop/GolandProjects/Lets-GO"

commit:
  message: "chore: automatic backup"

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
| `commit.message` | Commit message used for automatic commits. |
| `editors` | Process names used to determine whether scheduled backups should run. |

### Example: Back up two repositories

```yaml
interval: "5m"

repositories:
  - path: "/home/your-user/projects/project-one"
  - path: "/home/your-user/projects/project-two"

commit:
  message: "chore: automatic backup"

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

## Usage

### Normal workflow

1. Start AutoGit.
2. Open one of the configured editors.
3. Work normally in a configured repository.
4. Leave AutoGit running.
5. Allow the scheduled cycle to process your changes.
6. Check the logs for errors if a backup does not appear to complete.
7. Before switching computers, verify that your latest commit is available on the remote.

### Check repository synchronization

Navigate to your project:

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

### Stop AutoGit

In the terminal running AutoGit, press:

```text
Ctrl+C
```

This stops the running process. It does not uninstall AutoGit or remove your repositories.

### Running AutoGit in the background

The current documented workflow runs AutoGit in a terminal. If you close that terminal or terminate the process, scheduled backups stop.

Running it automatically as a background service at login would require an additional setup, such as a user-level systemd service. That setup is not currently part of the core installation instructions.

---

## Synchronization and Conflict Handling

### When local changes exist

AutoGit stages and commits local changes before fetching remote history. This gives local work a commit that can be replayed if rebasing is needed.

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

Check the logs and compare local and remote history rather than assuming that every scheduled check successfully reached the remote.

---

## Testing

Run the complete Go test suite from the project root:

```bash
go test ./...
```

The suite covers the project's configuration, editor, Git, and scheduler packages.

To run tests with Go's race detector on a supported platform:

```bash
go test -race ./...
```

The race-detector command is an additional check; its successful execution should be verified in your environment.

### Practical test coverage

The current version has been exercised with these scenarios:

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

Run AutoGit from the project root and confirm that `config.yaml` exists there:

```bash
ls -l config.yaml
```

The application's configuration-loading behavior determines which working directory or path is used.

### Git fetch or push fails

Check that Git works independently:

```bash
git remote -v
git status -sb
git fetch origin
```

Verify remote URLs, authentication, network connectivity, and the configured upstream.

Do not share access tokens, passwords, or private keys when requesting help.

### The local and remote branches have diverged

Inspect the history:

```bash
git log --oneline --graph --decorate --all -15
```

If AutoGit reports a failed rebase, inspect the repository status and resolve the issue manually. Do not repeatedly force-push to bypass a conflict.

### A file is not being backed up

Check whether Git is ignoring it:

```bash
git status --short --ignored
```

Ignored files are generally not included by `git add .`. Check repository-level `.gitignore` files and your global Git ignore configuration.

### A backup check completes but the latest commit is missing remotely

A generic completion message alone is not proof that a new commit was pushed. Inspect AutoGit's logs, then compare local and remote state:

```bash
git status -sb
git log -1 --oneline
git rev-list --left-right --count HEAD...@{upstream}
```

Investigate any reported errors before assuming the backup succeeded.

---

## Limitations

AutoGit is intended to automate routine Git operations, not to replace Git knowledge, remote hosting, or a comprehensive backup strategy.

### 1. It is not continuous file synchronization

AutoGit runs scheduled checks. It does not immediately mirror every keystroke or file change to another computer.

A change made just after a check may remain local until the next cycle.

### 2. It depends on a configured editor being open

In the current implementation, scheduled backups are enabled only while a configured editor process is detected.

If no configured editor is running, AutoGit pauses its scheduler. Changes may therefore remain uncommitted until an editor opens and a subsequent backup cycle runs.

### 3. Conflicts require manual intervention

AutoGit attempts to rebase divergent histories but does not resolve content conflicts automatically.

A failed rebase is reported and aborted where possible. The user must resolve the underlying issue before synchronization can succeed.

### 4. It requires Git and working remote access

AutoGit depends on the Git executable, local repository state, configured remotes, credentials, and network connectivity.

It cannot guarantee that a commit reaches the remote when any of these dependencies fail.

### 5. Automatic staging may include unintended files

The current implementation uses `git add .` from the repository root context supplied to Git.

This can stage any eligible, non-ignored changes in the repository, not just source files you intended to publish.

Use `.gitignore` appropriately and review the repository before enabling AutoGit on sensitive projects.

### 6. It does not replace secure secret management

AutoGit is not a secret scanner. It may commit and push credentials, private configuration, environment files, or other sensitive content if those files are eligible for staging.

Never rely solely on a global Git ignore file for secrets. Review repository-level ignore rules, and do not place sensitive material in tracked files.

### 7. It does not provide a complete backup retention system

AutoGit uses your Git repository and remote. It does not independently create a versioned offline archive, provide backup retention policies, or guarantee recovery if both the local repository and remote hosting account are lost or compromised.

Maintain independent backups for important work.

### 8. The editor monitor is Linux-oriented

The current editor detector uses `pgrep`. The documented process names and behavior target Linux environments.

Native Windows and macOS support should not be assumed without adapting and testing process detection for those systems.

### 9. Initial branch and upstream configuration matter

AutoGit assumes a usable Git repository and an `origin` remote. Existing branches should have an appropriate upstream configured for remote synchronization.

A new branch or repository with no upstream may need an initial push or manual setup, depending on its current state and remote configuration.

### 10. A successful scheduler cycle is not a universal guarantee

A cycle can encounter an error while committing, fetching, rebasing, or pushing. Review errors and verify the remote state when it matters.

### 11. It is not a collaboration or locking system

AutoGit does not lock files, coordinate simultaneous editing across computers, or prevent two users or devices from modifying the same code concurrently. Git's normal synchronization and conflict mechanisms still apply.

### 12. Background-service setup is separate

The core workflow requires AutoGit to remain running. Starting it automatically at login, restarting it after a crash, and managing it as a system service require additional setup.

---

## Security Considerations

Before using AutoGit on important projects:

- Configure `.gitignore` to exclude secrets, generated files, dependencies, and other files that should not be committed.
- Review existing tracked files for accidentally committed credentials.
- Ensure remote authentication is configured securely.
- Do not use force-push as a routine workaround for synchronization failures.
- Keep independent backups of important projects.
- Verify remote synchronization before switching computers or deleting local copies.
- Treat automatic commits as real commits: they become part of the repository's history and may be pushed to other collaborators.

If a secret has already been committed or pushed, simply deleting the file in a later commit does not erase the secret from earlier history. Revoke or rotate exposed credentials and follow an appropriate history-remediation process.

---

## Project Structure

The current project is organized into Go packages with separate responsibilities:

```text
auto-git/
├── cmd/
│   └── autogit/
│       └── main.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── config_test.go
│   ├── editor/
│   │   ├── editor.go
│   │   └── editor_test.go
│   ├── git/
│   │   ├── git.go
│   │   └── git_test.go
│   └── scheduler/
│       ├── scheduler.go
│       └── scheduler_test.go
├── config.yaml
├── go.mod
└── README.md
```

Some files may differ as the project evolves. This layout describes the current known organization.

### Package responsibilities

- `cmd/autogit`: application entry point, configuration loading, editor monitoring, and scheduler lifecycle.
- `internal/config`: configuration parsing and validation.
- `internal/editor`: detection of configured editor processes.
- `internal/git`: repository checks, staging, commits, fetches, rebasing, and pushing.
- `internal/scheduler`: scheduled backup execution and lifecycle management.

---

## Roadmap

Possible future improvements include:

- **Service management:** start AutoGit at login and restart it after failures.
- **Cross-platform editor monitoring:** support native process detection on macOS and Windows.
- **Improved backup reporting:** clearer per-repository status and explicit success or failure summaries.
- **Safer staging options:** configurable file selection, exclusions, or a review mode before committing.
- **Retry policies:** controlled retries and backoff for transient network failures.
- **Conflict recovery guidance:** clearer instructions for recovering after failed synchronization.
- **Notifications:** report failed pushes, conflicts, and successful backups.
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

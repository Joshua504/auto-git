---
sessionId: session-261103-125023-aw0y
---

# Requirements

### Overview & Goals

Add a **`restore`** subcommand to AutoGit that lets users recover their work from previous backup snapshots. Snapshots are Git commits stored either on the current branch (commit mode) or on a separate backup branch `autogit/<machine>/<branch>` (branch mode). The restore feature makes backups truly useful by providing easy recovery without manually searching through Git history.

### User Stories

- As a user, I want to **list available snapshots** so I can see what backups exist.
- As a user, I want to **preview which files changed** in a snapshot before restoring.
- As a user, I want to **restore a single file** from a snapshot without affecting the rest of my working tree.
- As a user, I want to **restore a complete snapshot** to a separate directory for safe inspection.
- As a user, I want to **compare two snapshots** to see what changed between them.
- As a user, I want to see **snapshot timestamps and originating machines** so I know when and where each backup was made.
- As a user, I want to **preview a restore** before executing it.
- As a user, I want **explicit confirmation** before modifying my current working tree.
- As a user, I want to **restore from a remote backup branch** when snapshots were pushed from another machine.

### Scope

**In Scope:**
- New `restore` subcommand with sub-operations: `list`, `show`, `diff`, `file`, `restore`
- Listing snapshots from local backup refs and remote backup branches
- Previewing changed files between snapshots or between a snapshot and the working tree
- Restoring a single file from a snapshot into the current working tree
- Restoring a complete snapshot to a separate directory (safe, non-destructive)
- Restoring a complete snapshot into the current working tree (with explicit `--force` confirmation)
- Comparing two snapshots (`diff`)
- Fetching remote backup branches before listing/restoring
- Showing snapshot metadata: timestamp, machine, branch, commit hash, message

**Out of Scope:**
- Interactive TUI or GUI (CLI-only)
- Restoring commit-mode snapshots from the current branch history (the current branch is already accessible via standard Git commands)
- Automatic conflict resolution during restore
- Deleting or managing snapshots (covered by existing retention pruning)

# Technical Design

### Current Implementation

AutoGit stores snapshots as Git commits:
- **Branch mode** (recommended): snapshots live on `refs/autogit/<machine>/<branch>` locally and `refs/heads/autogit/<machine>/<branch>` on the remote. The `backupNames` struct in `internal/git/snapshot.go` manages these refs.
- **Commit mode**: snapshots are regular commits on the current branch.

Existing subcommands (`verify`, `queue`, `health`, `install`, `uninstall`) follow a pattern in `cmd/autogit/main.go`: each is a `cmdXxx` function dispatched from `main()` via `os.Args[1]` with its own `flag.FlagSet`.

The `internal/git` package provides the low-level Git operations. Tests use a `syncFixture` (bare remote + two clones) from `internal/git/sync_test.go`.

### Key Decisions

1. **New file `internal/git/restore.go`** — all restore-related Git logic lives here, following the pattern of `verify.go`, `health.go`, `pull.go`, and `snapshot.go`.

2. **Snapshot metadata struct** — `SnapshotInfo` holds hash, author date, author name/email (machine), branch, and commit message. This is returned by `ListSnapshots()`.

3. **Remote support** — before listing or restoring, the command fetches from the remote to discover snapshots pushed from other machines. Users can specify `--machine` and `--branch` flags to target a specific backup branch.

4. **Two restore targets** — `--dir <path>` restores to a separate directory (safe, default). `--force` restores into the current working tree after listing what will change.

5. **Sub-operations as flags** — rather than nested subcommands (`autogit restore list`), the operation is selected with `--op` flag: `list`, `show`, `diff`, `file`, `restore`. This matches the flat flag-based pattern used by existing commands.

### Proposed Changes

#### 1. `internal/git/restore.go` — Git operations for restore

**`SnapshotInfo`** struct:
```go
type SnapshotInfo struct {
    Hash      string    // full commit hash
    ShortHash string    // first 7 chars
    Date      time.Time // author date
    Author    string    // "Name <email>"
    Machine   string    // extracted from backup branch name
    Branch    string    // the working branch that was backed up
    Message   string    // first line of commit message
}
```

**`ListSnapshots(path string, opts Options, machine, branch string) ([]SnapshotInfo, error)`**
- Fetches from remote to get the latest backup branches.
- If `machine` and `branch` are specified, lists commits on that specific backup branch.
- Otherwise, discovers all `autogit/*/*` backup branches (both local and remote) and lists their tips.
- Returns snapshots sorted newest-first.

**`SnapshotDiff(path string, opts Options, fromHash, toHash string) ([]FileChange, error)`**
- Returns the list of files that differ between two snapshots (or between a snapshot and the working tree when `toHash` is `"WORKTREE"`).
- Uses `git diff --name-status <from>..<to>`.

**`FileChange`** struct:
```go
type FileChange struct {
    Path    string // relative path in the repository
    Status  string // "M", "A", "D", "R"
    From    string // old path (for renames)
}
```

**`RestoreFile(path, snapshotHash, filePath, destPath string) error`**
- Extracts a single file from a snapshot commit to `destPath`.
- Uses `git show <hash>:<filePath>`.

**`RestoreSnapshotToDir(path string, opts Options, snapshotHash, destDir string) error`**
- Checks out the entire tree of a snapshot to a separate directory.
- Uses `git read-tree` and `git checkout-index -a -f --prefix=<destDir>/`.

**`RestoreSnapshotToWorktree(path string, snapshotHash string) error`**
- Restores a snapshot into the current working tree.
- Uses `git checkout <hash> -- .` to overwrite working tree files.
- Does NOT move HEAD — only updates the working tree.

#### 2. `cmd/autogit/main.go` — CLI dispatch

New case in the subcommand switch:
```go
case "restore":
    restoreFlag := flag.NewFlagSet("restore", flag.ExitOnError)
    // flags...
    restoreFlag.Parse(os.Args[2:])
    if err := cmdRestore(*restoreFlagConfig); err != nil {
        log.Fatalf("Restore failed: %v", err)
    }
    return
```

**`cmdRestore(configFlag string)`** handles:
- `--op list` — list snapshots (with optional `--machine`, `--branch`, `--repo` filters)
- `--op show` — show files changed in a snapshot (`--hash`)
- `--op diff` — compare two snapshots (`--from`, `--to`)
- `--op file` — restore a single file (`--hash`, `--file`, `--dest`)
- `--op restore` — restore a complete snapshot (`--hash`, `--dir` or `--force`)

#### 3. `internal/git/restore_test.go` — tests

Tests follow the `syncFixture` pattern:
- `TestListSnapshotsEmpty` — no snapshots returns empty list
- `TestListSnapshots` — multiple backups produce sorted snapshot list
- `TestSnapshotDiff` — diff between two snapshots returns correct file changes
- `TestRestoreFile` — single file extracted correctly
- `TestRestoreSnapshotToDir` — full tree restored to separate directory
- `TestRestoreSnapshotToWorktree` — working tree updated, HEAD unchanged
- `TestListSnapshotsRemote` — snapshots from remote are discovered after fetch

#### 4. `config.example.yaml` and `README.md` — documentation

- Add restore command documentation to README.
- No config changes required — restore reads the same `backup` section to determine branch mode and prefix.

### Architecture Diagram

``` mermaid
graph LR
    A[cmd/autogit/main.go] -->|dispatch| B[cmdRestore]
    B -->|list| C[ListSnapshots]
    B -->|show| D[SnapshotDiff]
    B -->|diff| D
    B -->|file| E[RestoreFile]
    B -->|restore| F[RestoreSnapshotToDir]
    B -->|restore --force| G[RestoreSnapshotToWorktree]
    C --> H[git log / git fetch]
    D --> I[git diff --name-status]
    E --> J[git show hash:file]
    F --> K[git read-tree + checkout-index]
    G --> L[git checkout hash -- .]
```

# Delivery Steps

### ✓ Step 1: Implement restore Git operations in internal/git/restore.go
Create the core restore package with all Git-level operations.
- Define `SnapshotInfo`, `FileChange` structs.
- Implement `ListSnapshots()` — fetches remote, discovers backup branches, parses `git log` output for hash, date, author, and message. Extracts machine and branch from the backup branch name.
- Implement `SnapshotDiff()` — runs `git diff --name-status` between two refs or between a snapshot and the working tree (`HEAD`).
- Implement `RestoreFile()` — extracts a single file via `git show <hash>:<path>`.
- Implement `RestoreSnapshotToDir()` — uses `git read-tree` and `git checkout-index -a -f --prefix=<dir>/` to restore a full tree to a separate directory.
- Implement `RestoreSnapshotToWorktree()` — uses `git checkout <hash> -- .` to restore into the current working tree without moving HEAD.
- Add helper `discoverBackupBranches()` to find all `autogit/*/*` refs (local and remote tracking).

### ✓ Step 2: Add restore subcommand to cmd/autogit/main.go
Wire up the CLI interface for the restore feature.
- Add `case "restore"` to the subcommand switch in `main()` with its own `flag.FlagSet`.
- Implement `cmdRestore()` that loads config, resolves repository paths, and dispatches to the appropriate Git operation based on `--op` flag.
- Support operations: `list` (with `--machine`, `--branch`, `--repo` filters), `show` (`--hash`), `diff` (`--from`, `--to`), `file` (`--hash`, `--file`, `--dest`), `restore` (`--hash`, `--dir` or `--force`).
- Format output as readable tables for `list` and `show`/`diff`.
- Require explicit `--force` flag for `restore` into the current working tree; print a preview of changes first.
- Support `--repo` flag to target a specific repository from the config, or default to all configured repos for `list`.

### ✓ Step 3: Add tests in internal/git/restore_test.go
Cover all restore operations with tests using the existing `syncFixture` pattern.
- `TestListSnapshotsEmpty` — verify empty result when no backups exist.
- `TestListSnapshots` — create multiple branch-mode backups, verify `ListSnapshots` returns them sorted newest-first with correct metadata.
- `TestListSnapshotsMultiMachine` — simulate snapshots from two machines, verify both appear.
- `TestSnapshotDiff` — verify file change list between two snapshots is correct (added, modified, deleted files).
- `TestSnapshotDiffWorktree` — verify diff against current working tree.
- `TestRestoreFile` — restore a single file from a snapshot, verify content matches.
- `TestRestoreSnapshotToDir` — restore full snapshot to a separate directory, verify all files present.
- `TestRestoreSnapshotToWorktree` — restore to working tree, verify files updated and HEAD unchanged.
- `TestDiscoverBackupBranches` — verify local and remote backup branches are discovered.

### ✓ Step 4: Update documentation
Document the new restore feature.
- Add a **Backup Restore and Recovery** section to `README.md` covering all sub-operations with usage examples.
- Update the Table of Contents in `README.md`.
- Remove the restore-related items from the Roadmap section since they are now implemented.
- Update `config.example.yaml` if any restore-related config is needed (none expected, but verify).
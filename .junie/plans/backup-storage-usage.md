---
sessionId: session-261103-154747-1qii
---

# Requirements

### Overview & Goals

Add a `storage` subcommand to AutoGit that reports how much disk space backups consume. Users should be able to see per-repository and total storage usage, identify large files in snapshots, and receive cleanup recommendations.

### User Stories

- As a developer, I want to see how much storage each configured repository's backups use so I can identify which repos consume the most space.
- As a developer, I want to see the total storage across all repositories so I can track overall AutoGit disk usage.
- As a developer, I want to identify large files included in snapshots so I can decide whether to exclude them.
- As a developer, I want to see backup branch storage estimates (local `.git/objects` used by backup refs) so I know how much space branch-mode snapshots consume.
- As a developer, I want to identify repositories with rapidly growing backup histories so I can adjust retention policies.
- As a developer, I want cleanup recommendations so I can reduce storage consumption.

### Functional Requirements

1. **`autogit storage`** — new subcommand that displays a storage usage report.
2. **Per-repository breakdown** — for each configured repository:
   - Local `.git` directory size (total repository size on disk).
   - Backup branch commit count (number of snapshots on `autogit/*/*` branches).
   - Backup branch object size estimate (sum of object sizes reachable from backup refs).
   - Top N largest files in the latest snapshot (default top 10).
3. **Total summary** — aggregate storage across all configured repositories.
4. **Growth analysis** — for each repository, estimate the rate of backup growth (commits per day, size growth over time).
5. **Cleanup recommendations** — suggest actions based on findings:
   - Repositories with high backup commit counts → suggest tightening retention.
   - Large files in snapshots → suggest adding them to `.gitignore` or the secrets guard.
   - Repositories with rapidly growing histories → suggest retention policies.
6. **Output formats** — support `text` (default, human-readable) and `json` (for scripting).
7. **Flags**:
   - `--repo` — target a specific repository.
   - `--format` — output format (`text` or `json`).
   - `--top-files` — number of largest files to show per snapshot (default 10).
   - `--details` — include per-file breakdown and growth analysis.

### Out of Scope

- Remote storage usage (estimating space on GitHub/GitLab servers) — not reliably accessible via Git.
- Automatic cleanup — the `cleanup` command already handles that.
- Real-time monitoring — this is a point-in-time report.

# Technical Design

### Current Implementation

- **Subcommand pattern**: `main.go` dispatches subcommands (`health`, `history`, `restore`, `cleanup`, etc.) via a `switch` on `os.Args[1]`. Each has a `cmd*` function that parses flags, loads config, and produces output.
- **`internal/git/` package**: Provides Git operations. Key patterns:
  - `HealthResult` struct in `health.go` — per-repository health data.
  - `SnapshotInfo` struct in `restore.go` — snapshot metadata including `Size` (tree size via `git ls-tree -r --long`).
  - `GetSnapshotSize()` in `restore.go` — estimates snapshot tree size.
  - `ListSnapshotHistory()` in `restore.go` — walks backup branch commits.
  - `run()` helper in `git.go` — executes git commands with timeout.
- **Dependencies**: `go-humanize` is available (indirect dep) for formatting byte sizes.

### Key Decisions

1. **New file `internal/git/storage.go`** — keeps storage logic co-located with other Git operations, following the existing pattern (`health.go`, `restore.go`, etc.).
2. **`git count-objects` and `git verify-pack`** for object-level size estimation — these are the standard Git commands for measuring object store usage.
3. **`git rev-list --objects <ref>` + `git cat-file --batch-check`** to estimate backup branch object size — walk all objects reachable from backup refs, sum their sizes.
4. **Growth rate** calculated from the date range of backup commits (oldest to newest) and the number of commits.
5. **`go-humanize`** moved to a direct dependency for `humanize.Bytes()` formatting.

### Proposed Changes

#### 1. `internal/git/storage.go` — new file

**Data structures:**

- `StorageReport` — per-repository storage data:
  - `Path` (string) — repository path.
  - `GitDirSize` (int64) — size of `.git` directory in bytes.
  - `BackupCommits` (int) — number of backup branch commits.
  - `BackupObjectSize` (int64) — estimated object store size for backup refs.
  - `LatestSnapshotSize` (int64) — tree size of the latest snapshot.
  - `TopFiles` ([]LargeFile) — largest files in the latest snapshot.
  - `GrowthRate` — commits per day over the backup history.
  - `FirstBackupDate` / `LastBackupDate` (time.Time) — date range.
- `LargeFile` — a file in a snapshot:
  - `Path` (string) — relative path.
  - `Size` (int64) — size in bytes.
- `StorageSummary` — aggregate across repositories:
  - `Reports` ([]StorageReport).
  - `TotalGitDirSize` (int64).
  - `TotalBackupCommits` (int).
  - `TotalBackupObjectSize` (int64).
  - `Recommendations` ([]string).

**Functions:**

- `GetGitDirSize(path string) (int64, error)` — walks `.git` directory and sums file sizes.
- `GetBackupObjectSize(path string, opts Options) (int64, error)` — resolves backup refs, walks reachable objects via `git rev-list --objects`, sums sizes via `git cat-file --batch-check`.
- `GetTopFilesInSnapshot(path, hash string, n int) ([]LargeFile, error)` — uses `git ls-tree -r --long <hash>`, sorts by size, returns top N.
- `GetBackupGrowthRate(path string, opts Options) (GrowthRate, error)` — calculates commits/day from backup commit dates.
- `CheckStorage(path string, opts Options, topFiles int) (StorageReport, error)` — orchestrates all measurements for one repository.
- `CheckAllStorage(paths []string, opts Options, topFiles int) StorageSummary` — aggregates across repositories and generates recommendations.

#### 2. `cmd/autogit/main.go` — add `storage` subcommand

- Add `case "storage"` to the subcommand switch.
- Implement `cmdStorage(configFlag string) error` following the pattern of `cmdHealth`:
  - Parse flags: `--repo`, `--format`, `--top-files`, `--details`.
  - Load config, build `git.Options`.
  - Call `git.CheckAllStorage()`.
  - Print text or JSON output.

#### 3. `internal/git/storage_test.go` — new test file

- Test `GetGitDirSize` with a real temp repo.
- Test `GetTopFilesInSnapshot` with known file sizes.
- Test `CheckStorage` end-to-end with a repo that has backup snapshots.
- Test `StorageSummary.Recommendations` generation logic.

#### 4. `go.mod` — promote `go-humanize` to direct dependency

### Components

| File | Change |
|------|--------|
| `internal/git/storage.go` | **New** — storage analysis logic |
| `internal/git/storage_test.go` | **New** — tests |
| `cmd/autogit/main.go` | **Modified** — add `storage` subcommand and `cmdStorage` |
| `go.mod` | **Modified** — promote `go-humanize` to direct dep |

### Architecture Diagram

``` mermaid
graph LR
  A[cmd/autogit/main.go] -->|dispatch| B[cmdStorage]
  B -->|loads| C[config.Config]
  B -->|calls| D[git.CheckAllStorage]
  D -->|per repo| E[git.CheckStorage]
  E --> F[GetGitDirSize]
  E --> G[GetBackupObjectSize]
  E --> H[GetTopFilesInSnapshot]
  E --> I[GetBackupGrowthRate]
  G --> J[git rev-list --objects]
  G --> K[git cat-file --batch-check]
  H --> L[git ls-tree -r --long]
  D --> M[StorageSummary + Recommendations]
  B -->|prints| N[text / JSON output]
```

### Risks

- **Large repositories**: `git rev-list --objects` over all backup refs can be slow on repos with thousands of snapshots. Mitigation: use `shortTimeout` (30s) and skip repos that time out with a warning.
- **`.git` directory walk**: Following symlinks could be dangerous. Mitigation: use `os.Lstat` (no symlink follow) and skip non-regular files.
- **Shared objects**: Backup branch object size may overlap with main branch objects (Git packs share objects). The estimate is an upper bound, not exact. This is documented in the output.

# Delivery Steps

### ✓ Step 1: Implement storage analysis logic in internal/git/storage.go
Create the core storage analysis module with data structures and Git-based measurement functions.
- Define `StorageReport`, `LargeFile`, `GrowthRate`, and `StorageSummary` structs.
- Implement `GetGitDirSize()` — walks `.git` directory using `os.Lstat` to sum file sizes.
- Implement `GetBackupObjectSize()` — resolves backup refs, runs `git rev-list --objects` and `git cat-file --batch-check` to sum object sizes.
- Implement `GetTopFilesInSnapshot()` — runs `git ls-tree -r --long <hash>`, parses sizes, sorts descending, returns top N.
- Implement `GetBackupGrowthRate()` — calculates commits/day from backup commit date range.
- Implement `CheckStorage()` — orchestrates all measurements for a single repository.
- Implement `CheckAllStorage()` — aggregates across repositories and generates cleanup recommendations.
- Promote `go-humanize` to a direct dependency in `go.mod` for `humanize.Bytes()` formatting.

### ✓ Step 2: Add storage subcommand to main.go
Wire up the `autogit storage` CLI subcommand following the existing pattern (cmdHealth, cmdCleanup).
- Add `case "storage"` to the subcommand switch in `main()`.
- Implement `cmdStorage()` with flags: `--repo`, `--format` (text/json), `--top-files` (default 10), `--details`.
- Load config, build `git.Options`, and call `git.CheckAllStorage()`.
- Implement `printStorageText()` — human-readable report with per-repository breakdown, top files, growth rates, recommendations, and total summary.
- Implement `printStorageJSON()` — JSON output using `encoding/json`.
- Add `import` for `github.com/dustin/go-humanize`.

### ✓ Step 3: Add tests for storage analysis
Create `internal/git/storage_test.go` with integration tests.
- `TestGetGitDirSize` — create a temp repo, add files, verify `.git` size is positive.
- `TestGetTopFilesInSnapshot` — create a commit with known file sizes, verify top files are returned sorted by size.
- `TestCheckStorage` — end-to-end test with a repo that has backup snapshots (create backup refs manually).
- `TestStorageSummaryRecommendations` — verify recommendations are generated for high commit counts, large files, and rapid growth.
- `TestGetBackupGrowthRate` — verify commits/day calculation with known commit dates.
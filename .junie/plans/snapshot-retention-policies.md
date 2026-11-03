---
sessionId: session-261103-153314-1efg
---

# Technical Design

### Current Implementation

**Configuration** (`internal/config/config.go`):
- `RetentionCfg` struct with `MaxAge` (int, days) and `MaxCommits` (int)
- `ShouldPrune()` method checks if either value is > 0
- Global retention lives under `backup.retention` in config
- Per-repository `ScheduleConfig` exists as a pattern to follow

**Pruning** (`internal/git/snapshot.go`):
- `PruneBackupBranch()` (line 501) lists all commits on the backup branch, determines which to keep based on `max_age` and `max_commits`, and rebuilds the chain of kept commits
- Called after each successful push in `backupBranch()` (line 180)
- Also called when nothing new to push (line 168)
- `rebuildKeptChain()` (line 634) recreates kept commits without pruned ancestors
- `forcePushBackupBranch()` (line 727) force-pushes the result

**CLI** (`cmd/autogit/main.go`):
- Existing subcommands: `install`, `uninstall`, `verify`, `queue`, `health`, `restore`, `history`, `compare`
- `cmdRestore()` (line 891) shows the pattern for a subcommand with operations
- `cmdHistory()` (line 1184) shows the pattern for listing/filtering snapshots

**Options** (`internal/git/git.go`):
- `RetentionCfg` struct (line 226) mirrors the config version
- `Options` struct (line 243) carries `Retention` to backup operations
- `main.go` constructs `git.Options` with retention at 5 locations (lines 149, 431, 598, 925, 1214)

### Key Decisions

1. **Per-repo retention with global fallback**: Add `Retention` field to `Repository` struct. When empty, use `cfg.Backup.Retention`. Follows the `ScheduleConfig` pattern exactly.

2. **Calendar policies as additive filters**: `keep_daily`, `keep_weekly`, `keep_monthly` are additional fields in `RetentionCfg`. A snapshot is kept if it passes **any** rule (age, count, or calendar). This is backward-compatible and extensible.

3. **Calendar snapshot selection**: For `keep_daily=N`, keep the last snapshot of each of the last N days. For `keep_weekly=N`, keep the last snapshot of each of the last N weeks (by ISO week). For `keep_monthly=N`, keep the last snapshot of each of the last N months.

4. **Protected branch check**: Use `git merge-base --is-ancestor` to check if a snapshot commit is an ancestor of any local non-backup branch. This is the same mechanism already used in `snapshot.go` (`isAncestor()`).

5. **New `cleanup` subcommand**: Follows the `restore` subcommand pattern with `--dry-run` as default and `--force` to execute.

6. **Retention engine as a separate function**: Extract the retention evaluation logic into `EvaluateRetention()` that returns a list of commits to keep, keeping `PruneBackupBranch()` focused on the Git operations.

### Proposed Changes

#### 1. Configuration Changes

**`internal/config/config.go`:**
- Extend `RetentionCfg` with calendar fields:
  - `KeepDaily int` — keep last snapshot of each of the last N days
  - `KeepWeekly int` — keep last snapshot of each of the last N weeks
  - `KeepMonthly int` — keep last snapshot of each of the last N months
- Add `Retention RetentionCfg` field to `Repository` struct
- Add `GetRetention(path string) RetentionCfg` method on `Config` that returns per-repo retention or falls back to global
- Update `BackupConfig.validate()` to validate new fields (non-negative)
- Add `RetentionCfg.validate()` method

**`internal/git/git.go`:**
- Extend `RetentionCfg` struct with same calendar fields
- Update `ShouldPrune()` to also return true if any calendar field is > 0

**`config.example.yaml`:**
- Document per-repo retention and calendar policies

#### 2. Retention Engine

**`internal/git/retention.go`** (new file):
- `SnapshotCandidate` struct: hash, date, and metadata for a single snapshot
- `EvaluateRetention(path string, names backupNames, retention RetentionCfg) (kept []string, protected []string, err error)`:
  - Lists all snapshot commits (reuses existing logic from `PruneBackupBranch`)
  - Evaluates each commit against all retention rules
  - Checks protected branches
  - Returns hashes of commits to keep, hashes protected by branches, and any error
- `isCalendarSnapshot(ci commitInfo, now time.Time) (daily bool, weekly bool, monthly bool)`:
  - Determines if a snapshot qualifies as a daily/weekly/monthly representative
- `isAncestorOfUserBranch(path string, commit string) (bool, error)`:
  - Checks if a commit is an ancestor of any local non-backup branch

#### 3. Updated Pruning

**`internal/git/snapshot.go`:**
- Update `PruneBackupBranch()` to call `EvaluateRetention()` instead of inline logic
- Keep the chain-rebuilding logic (`rebuildKeptChain`) unchanged
- Add logging for protected snapshots that were skipped

#### 4. CLI Cleanup Command

**`cmd/autogit/main.go`:**
- Add `case "cleanup"` in the subcommand switch (after `compare`)
- `cmdCleanup()` function:
  - Parse flags: `--dry-run` (default true), `--force`, `--repo`, `--config`
  - Load config, iterate repositories
  - For each repo, call `EvaluateRetention()` and report results
  - If `--force`, execute pruning via `PruneBackupBranch()`
  - Output: table of snapshots with status (keep/prune/protected)

#### 5. Updated Options Construction

**`cmd/autogit/main.go`:**
- Update all 5 locations where `git.Options` is constructed to use per-repo retention when available (via `cfg.GetRetention(repo.Path)`)
- For commands that operate on a single repo (restore, history, compare), use the repo-specific retention

### Data Models / Contracts

#### Extended RetentionCfg

type RetentionCfg struct {
    MaxAge      int  // max age in days (existing)
    MaxCommits  int  // max snapshot count (existing)
    KeepDaily   int  // keep last snapshot of each of the last N days
    KeepWeekly  int  // keep last snapshot of each of the last N weeks
    KeepMonthly int  // keep last snapshot of each of the last N months
}

#### Repository with Retention

type Repository struct {
    Path      string         `yaml:"path"`
    Schedule  ScheduleConfig `yaml:"schedule,omitempty"`
    Retention RetentionCfg   `yaml:"retention,omitempty"`  // NEW
}

#### Config.GetRetention

func (c Config) GetRetention(repoPath string) RetentionCfg {
    // Return per-repo retention if set, otherwise global
}

#### EvaluateRetention Result

func EvaluateRetention(path string, names backupNames, retention RetentionCfg) (
    kept []string,      // commit hashes to keep
    protected []string, // commit hashes protected by branch ancestry
    err error,
)

### File Structure

```
internal/
  config/
    config.go              # MODIFIED: RetentionCfg extended, Repository.Retention added, GetRetention()
    config_test.go         # MODIFIED: tests for per-repo retention fallback
    backup_test.go         # MODIFIED: tests for calendar retention fields
  git/
    git.go                 # MODIFIED: RetentionCfg extended, ShouldPrune() updated
    retention.go           # NEW: retention engine (EvaluateRetention, calendar logic, protected branch check)
    retention_test.go      # NEW: tests for retention engine
    snapshot.go            # MODIFIED: PruneBackupBranch() uses EvaluateRetention()
    snapshot_test.go       # MODIFIED: tests for pruning with calendar policies

cmd/
  autogit/
    main.go                # MODIFIED: cleanup subcommand, per-repo retention in Options construction

config.example.yaml        # MODIFIED: document per-repo retention and calendar policies
README.md                  # MODIFIED: document new retention features
```

### Architecture Diagram

``` mermaid
graph LR
    A[config.yaml] --> B[Config.GetRetention]
    B --> C{Per-repo set?}
    C -->|Yes| D[Repository.Retention]
    C -->|No| E[Backup.Retention]
    D --> F[EvaluateRetention]
    E --> F
    F --> G[Age check]
    F --> H[Count check]
    F --> I[Calendar check]
    F --> J[Protected branch check]
    G --> K[Kept set]
    H --> K
    I --> K
    J --> K
    K --> L[PruneBackupBranch]
    L --> M[rebuildKeptChain]
    M --> N[force-push]
    O[autogit cleanup] --> F
    O -->|dry-run| P[Preview output]
    O -->|force| L
```

### Risks

1. **Calendar policy ambiguity**: If multiple snapshots exist on the same day, only the last one is kept as the "daily" representative. This is intentional but could surprise users who expect all snapshots within a retention window.

2. **Protected branch check performance**: For repositories with many branches, checking each snapshot against all branches could be slow. Mitigation: cache the list of local branches and skip backup branches (those under the `autogit/` prefix).

3. **Force-push safety**: Pruning rewrites backup branch history and force-pushes. If a user has cloned the backup branch elsewhere, those copies will diverge. This is documented but worth noting.

4. **Backward compatibility**: Existing configs without calendar fields will work unchanged (zero values mean "no limit"). The `ShouldPrune()` method is extended to include calendar fields.

# Delivery Steps

### ✓ Step 1: Extend RetentionCfg with calendar policies and per-repo support
Add calendar retention fields to `RetentionCfg` and per-repository retention to `Repository`, with global fallback via `Config.GetRetention()`.

**`internal/config/config.go`:**
- Add `KeepDaily`, `KeepWeekly`, `KeepMonthly` fields to `RetentionCfg`
- Add `Retention RetentionCfg` field to `Repository` struct
- Add `GetRetention(repoPath string) RetentionCfg` method on `Config`
- Add `RetentionCfg.validate()` method (non-negative checks)
- Update `BackupConfig.validate()` to call `RetentionCfg.validate()`

**`internal/git/git.go`:**
- Add `KeepDaily`, `KeepWeekly`, `KeepMonthly` to `git.RetentionCfg`
- Update `ShouldPrune()` to return true if any calendar field > 0

**`internal/config/backup_test.go`:**
- Add tests for calendar retention validation
- Add tests for per-repo retention fallback
- Add tests for `GetRetention()` method

**`config.example.yaml`:**
- Document per-repo retention and calendar policies with examples

### ✓ Step 2: Implement retention engine
Create the retention evaluation engine that determines which snapshots to keep based on all retention rules and protected branch checks.

**`internal/git/retention.go`** (new):
- `SnapshotCandidate` struct with hash, date, and metadata
- `EvaluateRetention(path, names, retention)` — main entry point that:
  - Lists all snapshot commits from the backup branch
  - Evaluates each against age, count, calendar, and protected branch rules
  - Returns `kept` hashes, `protected` hashes, and error
- `isCalendarSnapshot()` — determines if a snapshot qualifies as daily/weekly/monthly representative
- `isAncestorOfUserBranch()` — checks if a commit is an ancestor of any local non-backup branch
- `getLocalUserBranches()` — lists local branches excluding backup branches (autogit/ prefix)

**`internal/git/retention_test.go`** (new):
- Test age-based retention
- Test count-based retention
- Test daily/weekly/monthly calendar retention
- Test protected branch safety
- Test combined rules (snapshot kept if any rule passes)
- Test edge cases: empty branch, single commit, all protected

### ✓ Step 3: Update PruneBackupBranch to use retention engine
Refactor `PruneBackupBranch()` to delegate retention evaluation to the new engine while keeping the chain-rebuilding logic intact.

**`internal/git/snapshot.go`:**
- Update `PruneBackupBranch()` to call `EvaluateRetention()` instead of inline evaluation logic
- Keep `rebuildKeptChain()` and `forcePushBackupBranch()` unchanged
- Add logging for protected snapshots that were skipped
- Add logging for calendar-policy-kept snapshots

**`internal/git/snapshot_test.go`:**
- Add integration test: pruning with calendar policies keeps correct snapshots
- Add integration test: pruning skips snapshots protected by user branches
- Add integration test: pruning with combined rules (age + calendar)
- Verify existing tests still pass (backward compatibility)

### ✓ Step 4: Add autogit cleanup CLI subcommand
Implement the `autogit cleanup` subcommand for manual cleanup with preview support.

**`cmd/autogit/main.go`:**
- Add `case "cleanup"` in the subcommand switch block
- Implement `cmdCleanup()`:
  - Parse flags: `--dry-run` (default true), `--force`, `--repo`, `--config`
  - Load config, validate
  - For each target repository:
    - Build `backupNames` from repo path and backup options
    - Call `EvaluateRetention()` with per-repo retention
    - If `--dry-run`: print table of snapshots with status (keep/prune/protected)
    - If `--force`: call `PruneBackupBranch()` to execute cleanup
  - Print summary: total snapshots, kept, pruned, protected
- Update all 5 locations where `git.Options` is constructed to use `cfg.GetRetention(repo.Path)` for per-repo retention

**Tests:**
- Add CLI integration tests for `cleanup --dry-run` output format
- Add CLI integration tests for `cleanup --force` execution
- Add CLI integration tests for `--repo` filtering

### ✓ Step 5: Update config.example.yaml and README.md
Document the new retention features in the example config and README.

**`config.example.yaml`:**
- Add per-repo retention example under a repository entry
- Document `keep_daily`, `keep_weekly`, `keep_monthly` fields
- Show a complete example with all retention options

**`README.md`:**
- Update the "Backup branch retention" section (Section 17) to cover:
  - Per-repository retention with global fallback
  - Calendar-based retention policies (daily/weekly/monthly)
  - Protected branch safety
  - Manual cleanup via `autogit cleanup`
- Update the configuration reference table with new fields
- Add `autogit cleanup` to the CLI command documentation
- Update the project status blurb to mention enhanced retention
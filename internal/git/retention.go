package git

import (
	"fmt"
	"strings"
	"time"
)

// SnapshotCandidate represents a single snapshot commit for retention evaluation.
type SnapshotCandidate struct {
	Hash string
	Date time.Time
}

// EvaluateRetention determines which backup branch snapshots to keep based on
// the retention policy and protected branch checks.
//
// It returns the hashes of commits to keep, the hashes of commits that are
// protected by user branch ancestry, and any error encountered.
func EvaluateRetention(path string, names backupNames, retention RetentionCfg) ([]string, []string, error) {
	if !retention.ShouldPrune() {
		// No retention rules — keep everything.
		hashes, err := listSnapshotCommits(path, names)
		return hashes, nil, err
	}

	commits, err := listSnapshotCommitsWithDate(path, names)
	if err != nil {
		return nil, nil, err
	}

	if len(commits) == 0 {
		return nil, nil, nil
	}

	// Get user branches once for protected branch checks.
	userBranches, err := getLocalUserBranches(path, names.branch)
	if err != nil {
		return nil, nil, fmt.Errorf("listing local branches: %w", err)
	}

	now := time.Now()
	keptSet := make(map[string]bool)
	var protected []string

	// Pre-compute calendar representatives once instead of per-commit.
	var dailyReps, weeklyReps, monthlyReps map[string]bool
	if retention.KeepDaily > 0 {
		dailyReps = filterCalendarRepresentatives(commits, now, retention.KeepDaily, "daily")
	}
	if retention.KeepWeekly > 0 {
		weeklyReps = filterCalendarRepresentatives(commits, now, retention.KeepWeekly, "weekly")
	}
	if retention.KeepMonthly > 0 {
		monthlyReps = filterCalendarRepresentatives(commits, now, retention.KeepMonthly, "monthly")
	}

	// Evaluate each commit against all retention rules.
	for _, c := range commits {
		keep := false

		// Rule 1: Age-based retention — keep snapshots within max_age days.
		if retention.MaxAge > 0 {
			ageHours := now.Sub(c.Date).Hours()
			ageDays := int(ageHours / 24)
			if ageDays < retention.MaxAge {
				keep = true
			}
		}

		// Rule 3: Calendar-based retention.
		if dailyReps != nil && dailyReps[c.Hash] {
			keep = true
		}
		if weeklyReps != nil && weeklyReps[c.Hash] {
			keep = true
		}
		if monthlyReps != nil && monthlyReps[c.Hash] {
			keep = true
		}

		// Rule 4: Protected branch check — never prune if ancestor of a user branch.
		if len(userBranches) > 0 {
			isProtected, err := isAncestorOfUserBranch(path, c.Hash, userBranches)
			if err != nil {
				return nil, nil, fmt.Errorf("checking branch ancestry: %w", err)
			}
			if isProtected {
				keep = true
				protected = append(protected, c.Hash)
			}
		}

		if keep {
			keptSet[c.Hash] = true
		}
	}

	// Rule 2: Count-based retention — keep the N most recent snapshots.
	// Commits are ordered newest-first, so the first N are the most recent.
	if retention.MaxCommits > 0 {
		count := 0
		for _, c := range commits {
			if count >= retention.MaxCommits {
				break
			}
			keptSet[c.Hash] = true
			count++
		}
	}

	// Always keep at least one commit so the branch isn't empty.
	if len(keptSet) == 0 && len(commits) > 0 {
		keptSet[commits[0].Hash] = true
	}

	// Convert set to slice.
	var kept []string
	for hash := range keptSet {
		kept = append(kept, hash)
	}

	return kept, protected, nil
}

// selectCalendarRepresentatives determines which snapshots qualify as daily,
// weekly, and monthly representatives. For each period, it selects the last
// snapshot of that period within the retention window.
func selectCalendarRepresentatives(commits []SnapshotCandidate, now time.Time) (
	daily map[string]bool,
	weekly map[string]bool,
	monthly map[string]bool,
) {
	// We need to know the retention config to determine how many periods to look back.
	// Since we don't have access to the retention config here, we'll select representatives
	// for all periods that have snapshots, and the caller will filter by the retention count.
	// This is handled by the caller passing the retention config.

	// For now, we collect all potential representatives and let the caller filter.
	// Group commits by day, week, and month.
	dayGroups := make(map[string][]SnapshotCandidate)
	weekGroups := make(map[string][]SnapshotCandidate)
	monthGroups := make(map[string][]SnapshotCandidate)

	for _, c := range commits {
		dayKey := c.Date.Format("2006-01-02")
		year, week := c.Date.ISOWeek()
		weekKey := fmt.Sprintf("%d-W%02d", year, week)
		monthKey := c.Date.Format("2006-01")

		dayGroups[dayKey] = append(dayGroups[dayKey], c)
		weekGroups[weekKey] = append(weekGroups[weekKey], c)
		monthGroups[monthKey] = append(monthGroups[monthKey], c)
	}

	// For each group, the last snapshot (newest) is the representative.
	// Since commits are ordered newest-first, the first in each group is the representative.
	daily = make(map[string]bool)
	for _, group := range dayGroups {
		if len(group) > 0 {
			daily[group[0].Hash] = true
		}
	}

	weekly = make(map[string]bool)
	for _, group := range weekGroups {
		if len(group) > 0 {
			weekly[group[0].Hash] = true
		}
	}

	monthly = make(map[string]bool)
	for _, group := range monthGroups {
		if len(group) > 0 {
			monthly[group[0].Hash] = true
		}
	}

	return daily, weekly, monthly
}

// filterCalendarRepresentatives limits calendar representatives to the most recent N periods.
func filterCalendarRepresentatives(commits []SnapshotCandidate, now time.Time, keepCount int, period string) map[string]bool {
	if keepCount <= 0 {
		return nil
	}

	// Group commits by period.
	groups := make(map[string]SnapshotCandidate)
	var periodKeys []string
	seenKeys := make(map[string]bool)

	for _, c := range commits {
		var key string
		switch period {
		case "daily":
			key = c.Date.Format("2006-01-02")
		case "weekly":
			year, week := c.Date.ISOWeek()
			key = fmt.Sprintf("%d-W%02d", year, week)
		case "monthly":
			key = c.Date.Format("2006-01")
		}

		if !seenKeys[key] {
			seenKeys[key] = true
			periodKeys = append(periodKeys, key)
		}
		// Since commits are newest-first, the first one we see for a key is the representative.
		if _, exists := groups[key]; !exists {
			groups[key] = c
		}
	}

	// Sort period keys in descending order (most recent first) and take the last N.
	// Period keys are already sortable as strings for daily (YYYY-MM-DD) and monthly (YYYY-MM).
	// For weekly (YYYY-WNN), they are also sortable.
	// Since commits are newest-first, periodKeys are already in descending order.
	if len(periodKeys) > keepCount {
		periodKeys = periodKeys[:keepCount]
	}

	// Build the result set.
	result := make(map[string]bool)
	for _, key := range periodKeys {
		if c, exists := groups[key]; exists {
			result[c.Hash] = true
		}
	}

	return result
}

// isAncestorOfUserBranch checks if a commit is an ancestor of any of the given branches.
func isAncestorOfUserBranch(path, commit string, branches []string) (bool, error) {
	for _, branch := range branches {
		ancestor, err := isAncestor(path, commit, branch)
		if err != nil {
			return false, err
		}
		if ancestor {
			return true, nil
		}
	}
	return false, nil
}

// getLocalUserBranches lists local branches excluding backup branches.
func getLocalUserBranches(path, backupBranch string) ([]string, error) {
	output, err := run(shortTimeout, path, "branch", "--format=%(refname:short)")
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	var branches []string
	for _, line := range lines {
		branch := strings.TrimSpace(line)
		if branch == "" {
			continue
		}
		// Skip backup branches (those starting with the backup prefix).
		if strings.HasPrefix(branch, "autogit/") {
			continue
		}
		branches = append(branches, branch)
	}

	return branches, nil
}

// ListSnapshotCommitsWithDate lists all snapshot commits with their dates, newest first.
func ListSnapshotCommitsWithDate(path string, names backupNames) ([]SnapshotCandidate, error) {
	return listSnapshotCommitsWithDate(path, names)
}

// listSnapshotCommitsWithDate lists all snapshot commits with their dates, newest first.
func listSnapshotCommitsWithDate(path string, names backupNames) ([]SnapshotCandidate, error) {
	localTip, err := revParseCommit(path, names.local)
	if err != nil {
		return nil, err
	}
	if localTip == "" {
		return nil, nil
	}

	output, err := run(shortTimeout, path, "log", "--format=%H%x09%at", localTip)
	if err != nil {
		return nil, fmt.Errorf("listing backup branch commits: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return nil, nil
	}

	var commits []SnapshotCandidate
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			continue
		}

		var date int64
		_, err := fmt.Sscanf(parts[1], "%d", &date)
		if err != nil {
			continue
		}

		commits = append(commits, SnapshotCandidate{
			Hash: parts[0],
			Date: time.Unix(date, 0),
		})
	}

	return commits, nil
}

// listSnapshotCommits lists all snapshot commit hashes, newest first.
func listSnapshotCommits(path string, names backupNames) ([]string, error) {
	commits, err := listSnapshotCommitsWithDate(path, names)
	if err != nil {
		return nil, err
	}

	var hashes []string
	for _, c := range commits {
		hashes = append(hashes, c.Hash)
	}
	return hashes, nil
}

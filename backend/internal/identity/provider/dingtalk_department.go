package provider

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// DingTalk department attributes show a hierarchy below its highest collected
// ancestor. The directory API returns one parent at a time, so names are prepended
// while traversal moves upward; blank names contribute no path component. A
// nonpositive parent, self-reference, repeated department, or depth limit ends a
// successful walk without a separate partial-result flag.
//
// The first collected name is then removed, which normally omits the organization
// root but also applies to a bounded or incomplete hierarchy. A root-only walk
// therefore produces an empty attribute value, which profile sync can use to clear
// an earlier value. Department lookup failure returns an error instead of the
// collected path, leaving the sync caller to skip that update.

// ResolveDingTalkDeptPath returns slash-separated department names after walking
// parents through client. It stops at nonpositive IDs, repeated IDs, self-parents,
// or fifty requests; a department lookup error returns an error immediately.
//
// Only nonblank names are collected, and the highest collected name is removed
// after any successful stop. That rule also applies when a cycle or request cap
// limits traversal, so the result can omit the top of a partial hierarchy. A
// root-only or nameless walk returns an empty path for profile synchronization.
func ResolveDingTalkDeptPath(ctx context.Context, client *DingTalkClient, deptID int64) (string, error) {
	slog.Info("dingtalk sync: resolve dept path start", "dept_id", deptID)
	// Bound directory requests when parent data never reaches a root.
	const maxDepth = 50
	visited := make(map[int64]bool, maxDepth)
	var parts []string

	current := deptID
	// Track each visited identifier before fetching its parent so cyclic directory
	// data cannot repeat requests or add the same department name indefinitely.
	for range maxDepth {
		if current < 1 || visited[current] {
			break
		}
		visited[current] = true

		info, err := client.GetDeptInfo(ctx, current)
		if err != nil {
			return "", fmt.Errorf("get dept info %d: %w", current, err)
		}
		if strings.TrimSpace(info.Name) != "" {
			parts = append([]string{strings.TrimSpace(info.Name)}, parts...)
		}
		// A nonpositive parent or self-parent completes this walk at its current
		// ancestor; names already collected still follow the common highest-name removal rule.
		if info.ParentID < 1 || info.ParentID == current {
			break
		}
		current = info.ParentID
	}

	// Profile attributes omit the top collected component, including after cycle
	// or depth termination. This can leave a root-only walk empty for clearing an attribute.
	if len(parts) > 0 {
		parts = parts[1:]
	}

	return strings.Join(parts, "/"), nil
}

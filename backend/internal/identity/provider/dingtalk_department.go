package provider

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// ResolveDingTalkDeptPath walks parent departments and returns their slash-separated names.
// It stops at a missing parent, a cycle, or 50 levels, then omits the highest ancestor name.
func ResolveDingTalkDeptPath(ctx context.Context, client *DingTalkClient, deptID int64) (string, error) {
	slog.Info("dingtalk sync: resolve dept path start", "dept_id", deptID)
	const maxDepth = 50
	visited := make(map[int64]bool, maxDepth)
	var parts []string

	current := deptID
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
		// Stop at the root or a self-reference.
		if info.ParentID < 1 || info.ParentID == current {
			break
		}
		current = info.ParentID
	}

	// Drop the highest ancestor name from the collected path.
	// For a complete hierarchy, this removes the organization name; a root-only path is empty.
	if len(parts) > 0 {
		parts = parts[1:]
	}

	return strings.Join(parts, "/"), nil
}

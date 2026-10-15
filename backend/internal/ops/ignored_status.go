package ops

import "sort"

// ignored_status.go normalizes client-status exclusions for SLA queries and
// policy comparisons. Nil input selects the default authentication statuses,
// 401 and 403, while an allocated empty slice disables status exclusions.
// Nonempty input retains every supplied integer, sorted and deduplicated for
// query construction.
//
// Normalization allocates a result before sorting, so comparisons and SQL
// construction can use a stable ordering without rearranging caller input.
// The default status list is also freshly allocated for each call. Validation
// of permitted status values belongs to settings updates; normalization accepts
// integers outside the usual HTTP status range.

// DefaultOpsIgnoredStatusCodes allocates the default client-status exclusions
// for Ops SLA queries.
func DefaultOpsIgnoredStatusCodes() []int {
	return []int{401, 403}
}

// NormalizeOpsIgnoredStatusCodes returns independently allocated, sorted,
// unique exclusion codes. Nil input selects the default 401 and 403 policy; an
// allocated empty slice selects no status exclusions, and integer validity is
// checked by settings updates.
func NormalizeOpsIgnoredStatusCodes(codes []int) []int {
	if codes == nil {
		return DefaultOpsIgnoredStatusCodes()
	}
	if len(codes) == 0 {
		return []int{}
	}
	seen := make(map[int]struct{}, len(codes))
	out := make([]int, 0, len(codes))
	for _, code := range codes {
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	sort.Ints(out)
	return out
}

package ops

import "sort"

// Client-status exclusions distinguish an absent policy from an intentionally
// empty policy. Nil input selects the default authentication statuses, while an
// empty slice disables status exclusions; normalization sorts and deduplicates
// supplied integers for query construction and policy comparison.

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

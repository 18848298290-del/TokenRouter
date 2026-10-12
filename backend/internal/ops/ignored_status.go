package ops

import "sort"

func DefaultOpsIgnoredStatusCodes() []int {
	return []int{401, 403}
}

// NormalizeOpsIgnoredStatusCodes returns sorted unique codes. Nil uses defaults; an empty slice disables exclusions.
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

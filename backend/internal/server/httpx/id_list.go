package httpx

import (
	"slices"
)

// Resource-ID collections can include duplicates and nonpositive entries from
// request payloads. Batch handlers receive positive unique identifiers in a
// stable numeric order, with an originally empty collection represented by nil
// and a supplied collection of invalid entries represented by an empty result.

// NormalizeInt64IDList returns sorted, unique positive IDs without modifying ids.
// Empty input returns nil; nonempty input with no valid IDs returns an
// independently allocated empty slice.
func NormalizeInt64IDList(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}

	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	slices.Sort(out)
	return out
}

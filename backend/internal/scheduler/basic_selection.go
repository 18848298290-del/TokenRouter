package scheduler

import (
	mathrand "math/rand"
	"sort"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Basic scheduling applies priority, observed load, active reset windows, and
// recent use as successive preferences. Smaller numeric priorities and lower
// load rates keep their candidates available for later tie decisions. Reset
// preference narrows to the earliest future window when one exists, while a
// pool with no active window keeps every candidate.
//
// Least-recent-use selection prefers an unused provider, then the oldest exact
// timestamp, and can favor OAuth among that final tied set. Sorting paths
// additionally randomize adjacent peers whose last-used timestamps share a
// Unix second. Each shuffler retains the boundaries of its own comparison
// keys, so callers establish adjacent groups first and keep borrowed provider
// and load observations stable during selection.

// BasicProvider holds borrowed provider identity and observations used by
// basic ordering rules.
type BasicProvider struct {
	ID       int64  // persistent provider record identifier
	Type     string // provider authentication kind identifier
	Priority int    // global numeric provider priority
	// LastUsedAt is an optional borrowed observation. Nil represents a never-used
	// provider; sorting compares exact timestamps while shuffle grouping compares
	// their Unix seconds.
	LastUsedAt *time.Time // latest recorded provider use
	// SessionWindowEnd is an optional borrowed reset observation.
	// FilterBySoonestReset considers it active when the supplied clock is before
	// its value.
	SessionWindowEnd *time.Time // provider session window ending
}

// BasicCandidate couples a provider's basic metadata with its current load
// observation.
type BasicCandidate struct {
	// Provider supplies identity, priority, and time observations for filtering.
	// Callers supply nonnil records and keep them unchanged while sorting or
	// selecting.
	Provider *BasicProvider // borrowed provider ordering metadata
	// Load supplies load-rate tie decisions and shuffle-group equality.
	// Load-dependent helpers require a nonnil observation and borrow it for the
	// duration of the call.
	Load *ProviderLoadInfo // borrowed provider load observation
}

// FilterByMinPriority retains candidates with the smallest numeric provider
// priority in input order. Empty input is returned unchanged, and populated
// results use new slice storage while retaining provider and load pointers.
func FilterByMinPriority(providers []BasicCandidate) []BasicCandidate {
	if len(providers) == 0 {
		return providers
	}
	minPriority := providers[0].Provider.Priority
	for _, acc := range providers[1:] {
		if acc.Provider.Priority < minPriority {
			minPriority = acc.Provider.Priority
		}
	}
	result := make([]BasicCandidate, 0, len(providers))
	for _, acc := range providers {
		if acc.Provider.Priority == minPriority {
			result = append(result, acc)
		}
	}
	return result
}

// FilterByMinLoadRate retains candidates tied for the lowest observed load
// rate in input order. Empty input is returned unchanged; nonempty results
// copy candidate values and retain their borrowed provider and load pointers.
func FilterByMinLoadRate(providers []BasicCandidate) []BasicCandidate {
	if len(providers) == 0 {
		return providers
	}
	minLoadRate := providers[0].Load.LoadRate
	for _, acc := range providers[1:] {
		if acc.Load.LoadRate < minLoadRate {
			minLoadRate = acc.Load.LoadRate
		}
	}
	result := make([]BasicCandidate, 0, len(providers))
	for _, acc := range providers {
		if acc.Load.LoadRate == minLoadRate {
			result = append(result, acc)
		}
	}
	return result
}

// FilterBySoonestReset keeps candidates sharing the earliest active
// session-window end. Empty and singleton pools are returned unchanged, and
// larger pools sample the supplied clock once so every window is compared
// against the same instant. Provider pointers remain borrowed in the filtered
// result.
//
// A future reset supplies a preference while nil or expired windows supply no
// active observation. When every window is inactive, the whole pool remains
// available for later priority, load, or LRU decisions. Once an active window
// exists, candidates with no active observation are removed along with those
// whose reset is later.
func FilterBySoonestReset(providers []BasicCandidate, now func() time.Time) []BasicCandidate {
	if len(providers) <= 1 {
		return providers
	}
	instant := now()
	var minEnd *time.Time
	for _, acc := range providers {
		end := acc.Provider.SessionWindowEnd
		if end == nil || !instant.Before(*end) {
			continue
		}
		if minEnd == nil || end.Before(*minEnd) {
			minEnd = end
		}
	}
	if minEnd == nil {
		// Missing and expired windows cannot supply an upcoming reset preference. Keep
		// every candidate available so later load and recency decisions can still select a provider.
		return providers
	}
	result := make([]BasicCandidate, 0, len(providers))
	for _, acc := range providers {
		end := acc.Provider.SessionWindowEnd
		if end != nil && instant.Before(*end) && end.Equal(*minEnd) {
			result = append(result, acc)
		}
	}
	return result
}

// SelectByLRU returns a pointer to one candidate in the supplied slice or nil
// for an empty pool. It first identifies the least-recent-use set using exact
// timestamps, with nil timestamps representing unused providers. The returned
// candidate retains the caller's provider and load references.
//
// OAuth preference applies after recency has narrowed the pool, so a recently
// used OAuth record cannot displace an older record. Every remaining peer has
// the same final preference, and random selection distributes choices within
// that set. If the recency tie has no OAuth record, its entire set remains
// available. The returned pointer refers to the chosen slice entry, so later
// mutations of that entry are visible through the result.
func SelectByLRU(providers []BasicCandidate, preferOAuth bool) *BasicCandidate {
	if len(providers) == 0 {
		return nil
	}
	if len(providers) == 1 {
		return &providers[0]
	}

	// Unused providers form the least-recent-use set when present. Recorded times
	// become relevant only when every candidate has already served a request.
	var minTime *time.Time
	hasNil := false
	for _, acc := range providers {
		if acc.Provider.LastUsedAt == nil {
			hasNil = true
			break
		}
		if minTime == nil || acc.Provider.LastUsedAt.Before(*minTime) {
			minTime = acc.Provider.LastUsedAt
		}
	}

	var candidateIdxs []int
	for i, acc := range providers {
		if hasNil {
			if acc.Provider.LastUsedAt == nil {
				candidateIdxs = append(candidateIdxs, i)
			}
		} else {
			if acc.Provider.LastUsedAt != nil && acc.Provider.LastUsedAt.Equal(*minTime) {
				candidateIdxs = append(candidateIdxs, i)
			}
		}
	}

	if len(candidateIdxs) == 1 {
		return &providers[candidateIdxs[0]]
	}

	// Restrict preference to the recency tie so a more recently used OAuth
	// provider cannot bypass the least-recent-use decision established above.
	if preferOAuth {
		var oauthIdxs []int
		for _, idx := range candidateIdxs {
			if providers[idx].Provider.Type == capability.ProviderTypeOAuth {
				oauthIdxs = append(oauthIdxs, idx)
			}
		}
		// OAuth preference can narrow a recency tie only when it has matching
		// records. Otherwise the existing candidate set remains available for random selection.
		if len(oauthIdxs) > 0 {
			candidateIdxs = oauthIdxs
		}
	}

	// Each remaining candidate has the same recency and enabled kind preference.
	// Randomize this final tie to vary selections while preserving the chosen ordering rules.
	selectedIdx := candidateIdxs[mathrand.Intn(len(candidateIdxs))]
	return &providers[selectedIdx]
}

// SortProvidersByPriorityAndLastUsed orders the supplied slice by numeric
// priority and recent-use preference, then shuffles adjacent tie groups. It
// prefers nil use timestamps and, when enabled, OAuth among unused providers.
// Provider pointers and their timestamp values remain borrowed.
//
// The initial sort compares exact times, while the shuffle groups use Unix
// seconds. Providers used at different instants within one second can
// therefore move within their common priority group. The shuffle partitions
// OAuth peers ahead of other kinds when requested, while retaining the
// boundaries established by priority and last-used-second equality.
func SortProvidersByPriorityAndLastUsed(providers []*BasicProvider, preferOAuth bool) {
	sort.SliceStable(providers, func(i, j int) bool {
		a, b := providers[i], providers[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		switch {
		case a.LastUsedAt == nil && b.LastUsedAt != nil:
			return true
		case a.LastUsedAt != nil && b.LastUsedAt == nil:
			return false
		case a.LastUsedAt == nil && b.LastUsedAt == nil:
			if preferOAuth && a.Type != b.Type {
				return a.Type == capability.ProviderTypeOAuth
			}
			return false
		default:
			return a.LastUsedAt.Before(*b.LastUsedAt)
		}
	})
	ShuffleWithinPriorityAndLastUsed(providers, preferOAuth)
}

// ShuffleWithinSortGroups randomizes adjacent candidates sharing priority,
// load rate, and last-used Unix second. Callers sort the input first and
// supply stable nonnil provider and load observations. The function changes
// slice order in place while retaining each candidate's borrowed records.
//
// Equality is checked against the first candidate of each adjacent run.
// Distinct load rates or last-used seconds keep otherwise equal-priority
// records in separate runs, while subsecond timestamp differences remain
// eligible for the same shuffle. Singleton runs retain their positions.
func ShuffleWithinSortGroups(providers []BasicCandidate) {
	if len(providers) <= 1 {
		return
	}
	i := 0
	for i < len(providers) {
		j := i + 1
		for j < len(providers) && SameProviderWithLoadGroup(providers[i], providers[j]) {
			j++
		}
		if j-i > 1 {
			mathrand.Shuffle(j-i, func(a, b int) {
				providers[i+a], providers[i+b] = providers[i+b], providers[i+a]
			})
		}
		i = j
	}
}

// SameProviderWithLoadGroup reports whether two candidates share priority,
// load rate, and last-used-second grouping.
func SameProviderWithLoadGroup(a, b BasicCandidate) bool {
	if a.Provider.Priority != b.Provider.Priority {
		return false
	}
	if a.Load.LoadRate != b.Load.LoadRate {
		return false
	}
	return SameLastUsedAt(a.Provider.LastUsedAt, b.Provider.LastUsedAt)
}

// ShuffleWithinPriorityAndLastUsed randomizes adjacent peers with equal
// priority and last-used Unix second. Callers supply an already sorted slice
// containing nonnil providers, and the function changes that slice's order
// while retaining its provider references.
//
// When OAuth preference is enabled, each tied group is partitioned into OAuth
// and other kinds before its partitions are shuffled separately. OAuth remains
// ahead within that group while randomization still varies choices among peers
// of the same kind. Neighboring priority or last-used-second groups retain
// their positions.
func ShuffleWithinPriorityAndLastUsed(providers []*BasicProvider, preferOAuth bool) {
	if len(providers) <= 1 {
		return
	}
	i := 0
	for i < len(providers) {
		j := i + 1
		for j < len(providers) && SameProviderGroup(providers[i], providers[j]) {
			j++
		}
		if j-i > 1 {
			if preferOAuth {
				oauth := make([]*BasicProvider, 0, j-i)
				others := make([]*BasicProvider, 0, j-i)
				for _, acc := range providers[i:j] {
					if acc.Type == capability.ProviderTypeOAuth {
						oauth = append(oauth, acc)
					} else {
						others = append(others, acc)
					}
				}
				if len(oauth) > 1 {
					mathrand.Shuffle(len(oauth), func(a, b int) { oauth[a], oauth[b] = oauth[b], oauth[a] })
				}
				if len(others) > 1 {
					mathrand.Shuffle(len(others), func(a, b int) { others[a], others[b] = others[b], others[a] })
				}
				copy(providers[i:], oauth)
				copy(providers[i+len(oauth):], others)
			} else {
				mathrand.Shuffle(j-i, func(a, b int) {
					providers[i+a], providers[i+b] = providers[i+b], providers[i+a]
				})
			}
		}
		i = j
	}
}

// SameProviderGroup reports whether two providers share priority and
// last-used-second grouping.
func SameProviderGroup(a, b *BasicProvider) bool {
	if a.Priority != b.Priority {
		return false
	}
	return SameLastUsedAt(a.LastUsedAt, b.LastUsedAt)
}

// SameLastUsedAt reports timestamp equality at Unix-second resolution for
// shuffle grouping. Two nil observations compare equal, one nil observation
// differs from a populated value, and nanosecond differences within a second
// remain tied.
func SameLastUsedAt(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		// Use whole-second bins so different recorded instants can participate in
		// the same randomized tie group after the caller's priority or load
		// comparisons.
		return a.Unix() == b.Unix()
	}
}

// SortProvidersByPriorityOnly orders providers by ascending numeric priority
// and optional OAuth preference. It mutates the supplied slice, so callers
// keep its nonnil provider records stable throughout sorting. Equal comparison
// keys retain their input order.
//
// Authentication kind is considered only after priority matches. Enabling
// preference moves OAuth ahead of other kinds inside that priority group. The
// comparison gives unused and previously used records equal treatment when
// their enabled keys match.
func SortProvidersByPriorityOnly(providers []*BasicProvider, preferOAuth bool) {
	sort.SliceStable(providers, func(i, j int) bool {
		a, b := providers[i], providers[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if preferOAuth && a.Type != b.Type {
			return a.Type == capability.ProviderTypeOAuth
		}
		return false
	})
}

// ShuffleWithinPriority randomizes adjacent equal-priority groups using a
// clock-seeded generator. Callers first sort by priority; the function changes
// only ordering within each adjacent group and retains all borrowed provider
// pointers.
func ShuffleWithinPriority(providers []*BasicProvider, now func() time.Time) {
	if len(providers) <= 1 {
		return
	}
	r := mathrand.New(mathrand.NewSource(now().UnixNano()))
	start := 0
	for start < len(providers) {
		priority := providers[start].Priority
		end := start + 1
		for end < len(providers) && providers[end].Priority == priority {
			end++
		}

		if end-start > 1 {
			r.Shuffle(end-start, func(i, j int) {
				providers[start+i], providers[start+j] = providers[start+j], providers[start+i]
			})
		}
		start = end
	}
}

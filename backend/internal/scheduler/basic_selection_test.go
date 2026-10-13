package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestShuffleWithinSortGroups_Empty passes nil and empty load-aware candidate
// slices to the shuffler. Both calls must complete safely so absent candidate
// pools remain acceptable inputs to basic ordering.
func TestShuffleWithinSortGroups_Empty(t *testing.T) {
	ShuffleWithinSortGroups(nil)
	ShuffleWithinSortGroups([]BasicCandidate{})
}

// TestShuffleWithinSortGroups_SingleElement shuffles a pool containing one
// provider and load observation. The provider ID must remain at its original
// index because no tied peer can exchange positions.
func TestShuffleWithinSortGroups_SingleElement(t *testing.T) {
	providers := []BasicCandidate{
		{Provider: &BasicProvider{ID: 1, Priority: 1}, Load: &ProviderLoadInfo{LoadRate: 10}},
	}
	ShuffleWithinSortGroups(providers)
	require.Equal(t, int64(1), providers[0].Provider.ID)
}

// TestShuffleWithinSortGroups_DifferentGroups_OrderPreserved samples providers
// with different priority, load, or use observations. Their IDs must stay at
// the same indices because every candidate belongs to a separate shuffle
// group.
func TestShuffleWithinSortGroups_DifferentGroups_OrderPreserved(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)

	providers := []BasicCandidate{
		{Provider: &BasicProvider{ID: 1, Priority: 1, LastUsedAt: &earlier}, Load: &ProviderLoadInfo{LoadRate: 10}},
		{Provider: &BasicProvider{ID: 2, Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 20}},
		{Provider: &BasicProvider{ID: 3, Priority: 2, LastUsedAt: &earlier}, Load: &ProviderLoadInfo{LoadRate: 10}},
	}

	// Every candidate differs in priority, load, or last-used time, so none may move across groups.
	for range 20 {
		cpy := make([]BasicCandidate, len(providers))
		copy(cpy, providers)
		ShuffleWithinSortGroups(cpy)
		require.Equal(t, int64(1), cpy[0].Provider.ID)
		require.Equal(t, int64(2), cpy[1].Provider.ID)
		require.Equal(t, int64(3), cpy[2].Provider.ID)
	}
}

// TestShuffleWithinSortGroups_SameGroup_Shuffled uses equal priority and load
// with timestamps inside one Unix second. Samples must retain all IDs and
// produce at least two different first providers.
func TestShuffleWithinSortGroups_SameGroup_Shuffled(t *testing.T) {
	now := time.Now()
	// Timestamps in the same second form one shuffle group.
	sameSecond := time.Unix(now.Unix(), 0)
	sameSecond2 := time.Unix(now.Unix(), 500_000_000) // Keep different nanoseconds inside the same shuffle second.

	providers := []BasicCandidate{
		{Provider: &BasicProvider{ID: 1, Priority: 1, LastUsedAt: &sameSecond}, Load: &ProviderLoadInfo{LoadRate: 10}},
		{Provider: &BasicProvider{ID: 2, Priority: 1, LastUsedAt: &sameSecond2}, Load: &ProviderLoadInfo{LoadRate: 10}},
		{Provider: &BasicProvider{ID: 3, Priority: 1, LastUsedAt: &sameSecond}, Load: &ProviderLoadInfo{LoadRate: 10}},
	}

	// Repeat sampling to check that the first candidate varies.
	seen := map[int64]bool{}
	for range 100 {
		cpy := make([]BasicCandidate, len(providers))
		copy(cpy, providers)
		ShuffleWithinSortGroups(cpy)
		seen[cpy[0].Provider.ID] = true
		// Shuffling must retain every candidate ID.
		ids := map[int64]bool{}
		for _, a := range cpy {
			ids[a.Provider.ID] = true
		}
		require.True(t, ids[1] && ids[2] && ids[3])
	}
	// At least two IDs should appear first across the samples.
	require.GreaterOrEqual(t, len(seen), 2, "shuffle should produce different orderings")
}

// TestShuffleWithinSortGroups_NilLastUsedAt_SameGroup samples three unused
// providers with matching priority and load observations. At least two IDs
// must appear first, protecting randomization of the common nil-timestamp
// group.
func TestShuffleWithinSortGroups_NilLastUsedAt_SameGroup(t *testing.T) {
	providers := []BasicCandidate{
		{Provider: &BasicProvider{ID: 1, Priority: 1, LastUsedAt: nil}, Load: &ProviderLoadInfo{LoadRate: 0}},
		{Provider: &BasicProvider{ID: 2, Priority: 1, LastUsedAt: nil}, Load: &ProviderLoadInfo{LoadRate: 0}},
		{Provider: &BasicProvider{ID: 3, Priority: 1, LastUsedAt: nil}, Load: &ProviderLoadInfo{LoadRate: 0}},
	}

	seen := map[int64]bool{}
	for range 100 {
		cpy := make([]BasicCandidate, len(providers))
		copy(cpy, providers)
		ShuffleWithinSortGroups(cpy)
		seen[cpy[0].Provider.ID] = true
	}
	require.GreaterOrEqual(t, len(seen), 2, "nil LastUsedAt providers should be shuffled")
}

// TestShuffleWithinSortGroups_MixedGroups places a tied pair between two
// singleton shuffle groups. Repeated shuffles must keep the singleton
// positions and retain both middle IDs within the pair's indices.
func TestShuffleWithinSortGroups_MixedGroups(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)
	sameAsNow := time.Unix(now.Unix(), 0)

	// Group 1: priority 1, load 10, earlier use; ID 1 stands alone.
	// Group 2: priority 1, load 20, current use; IDs 2 and 3 may swap.
	// Group 3: priority 2, load 10, earlier use; ID 4 stands alone.
	providers := []BasicCandidate{
		{Provider: &BasicProvider{ID: 1, Priority: 1, LastUsedAt: &earlier}, Load: &ProviderLoadInfo{LoadRate: 10}},
		{Provider: &BasicProvider{ID: 2, Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 20}},
		{Provider: &BasicProvider{ID: 3, Priority: 1, LastUsedAt: &sameAsNow}, Load: &ProviderLoadInfo{LoadRate: 20}},
		{Provider: &BasicProvider{ID: 4, Priority: 2, LastUsedAt: &earlier}, Load: &ProviderLoadInfo{LoadRate: 10}},
	}

	for range 20 {
		cpy := make([]BasicCandidate, len(providers))
		copy(cpy, providers)
		ShuffleWithinSortGroups(cpy)

		// The order between groups remains fixed.
		require.Equal(t, int64(1), cpy[0].Provider.ID, "group 1 position fixed")
		require.Equal(t, int64(4), cpy[3].Provider.ID, "group 3 position fixed")

		// Group 2 may shuffle within positions 1 and 2.
		mid := map[int64]bool{cpy[1].Provider.ID: true, cpy[2].Provider.ID: true}
		require.True(t, mid[2] && mid[3], "group 2 elements should stay in positions 1-2")
	}
}

// TestShuffleWithinPriorityAndLastUsed_Empty supplies nil and empty provider
// slices to recency-group shuffling. Both calls must complete safely so a
// missing candidate pool does not require separate caller handling.
func TestShuffleWithinPriorityAndLastUsed_Empty(t *testing.T) {
	ShuffleWithinPriorityAndLastUsed(nil, false)
	ShuffleWithinPriorityAndLastUsed([]*BasicProvider{}, false)
}

// TestShuffleWithinPriorityAndLastUsed_SingleElement supplies one unused
// provider to recency-group shuffling. Its ID must remain first because the
// pool contains no peer whose position can be exchanged.
func TestShuffleWithinPriorityAndLastUsed_SingleElement(t *testing.T) {
	providers := []*BasicProvider{{ID: 1, Priority: 1}}
	ShuffleWithinPriorityAndLastUsed(providers, false)
	require.Equal(t, int64(1), providers[0].ID)
}

// TestShuffleWithinPriorityAndLastUsed_SameGroup_Shuffled samples three unused
// providers at the same priority. At least two first IDs must be observed,
// protecting random ordering within their common recency group.
func TestShuffleWithinPriorityAndLastUsed_SameGroup_Shuffled(t *testing.T) {
	providers := []*BasicProvider{
		{ID: 1, Priority: 1, LastUsedAt: nil},
		{ID: 2, Priority: 1, LastUsedAt: nil},
		{ID: 3, Priority: 1, LastUsedAt: nil},
	}

	seen := map[int64]bool{}
	for range 100 {
		cpy := make([]*BasicProvider, len(providers))
		copy(cpy, providers)
		ShuffleWithinPriorityAndLastUsed(cpy, false)
		seen[cpy[0].ID] = true
	}
	require.GreaterOrEqual(t, len(seen), 2, "same group should be shuffled")
}

// TestShuffleWithinPriorityAndLastUsed_DifferentPriority_OrderPreserved
// shuffles three unused providers whose priorities differ. Repeated samples
// must retain each ID's position, protecting the separation between adjacent
// priority groups.
func TestShuffleWithinPriorityAndLastUsed_DifferentPriority_OrderPreserved(t *testing.T) {
	providers := []*BasicProvider{
		{ID: 1, Priority: 1, LastUsedAt: nil},
		{ID: 2, Priority: 2, LastUsedAt: nil},
		{ID: 3, Priority: 3, LastUsedAt: nil},
	}

	for range 20 {
		cpy := make([]*BasicProvider, len(providers))
		copy(cpy, providers)
		ShuffleWithinPriorityAndLastUsed(cpy, false)
		require.Equal(t, int64(1), cpy[0].ID)
		require.Equal(t, int64(2), cpy[1].ID)
		require.Equal(t, int64(3), cpy[2].ID)
	}
}

// TestShuffleWithinPriorityAndLastUsed_DifferentLastUsedAt_OrderPreserved
// combines an unused record and distinct recorded use times at equal priority.
// Repeated samples must leave every ID in its established recency position.
func TestShuffleWithinPriorityAndLastUsed_DifferentLastUsedAt_OrderPreserved(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)

	providers := []*BasicProvider{
		{ID: 1, Priority: 1, LastUsedAt: nil},
		{ID: 2, Priority: 1, LastUsedAt: &earlier},
		{ID: 3, Priority: 1, LastUsedAt: &now},
	}

	for range 20 {
		cpy := make([]*BasicProvider, len(providers))
		copy(cpy, providers)
		ShuffleWithinPriorityAndLastUsed(cpy, false)
		require.Equal(t, int64(1), cpy[0].ID)
		require.Equal(t, int64(2), cpy[1].ID)
		require.Equal(t, int64(3), cpy[2].ID)
	}
}

// TestSameLastUsedAt compares missing, identical, and different-second
// timestamp observations. It checks that nanosecond differences within one
// Unix second remain equal while one missing observation differs from a
// populated value.
func TestSameLastUsedAt(t *testing.T) {
	now := time.Now()
	sameSecond := time.Unix(now.Unix(), 0)
	sameSecondDiffNano := time.Unix(now.Unix(), 999_999_999)
	differentSecond := now.Add(1 * time.Second)

	t.Run("both nil", func(t *testing.T) {
		require.True(t, SameLastUsedAt(nil, nil))
	})

	t.Run("one nil one not", func(t *testing.T) {
		require.False(t, SameLastUsedAt(nil, &now))
		require.False(t, SameLastUsedAt(&now, nil))
	})

	t.Run("same second different nanoseconds", func(t *testing.T) {
		require.True(t, SameLastUsedAt(&sameSecond, &sameSecondDiffNano))
	})

	t.Run("different seconds", func(t *testing.T) {
		require.False(t, SameLastUsedAt(&now, &differentSecond))
	})

	t.Run("exact same time", func(t *testing.T) {
		require.True(t, SameLastUsedAt(&now, &now))
	})
}

// TestSameProviderWithLoadGroup varies priority, observed load, and last-used
// timestamps independently. It checks that load-aware shuffle equality
// requires matching observations and accepts nil or same-second recency ties.
func TestSameProviderWithLoadGroup(t *testing.T) {
	now := time.Now()
	sameSecond := time.Unix(now.Unix(), 0)

	t.Run("same group", func(t *testing.T) {
		a := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 10}}
		b := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: &sameSecond}, Load: &ProviderLoadInfo{LoadRate: 10}}
		require.True(t, SameProviderWithLoadGroup(a, b))
	})

	t.Run("different priority", func(t *testing.T) {
		a := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 10}}
		b := BasicCandidate{Provider: &BasicProvider{Priority: 2, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 10}}
		require.False(t, SameProviderWithLoadGroup(a, b))
	})

	t.Run("different load rate", func(t *testing.T) {
		a := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 10}}
		b := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 20}}
		require.False(t, SameProviderWithLoadGroup(a, b))
	})

	t.Run("different last used at", func(t *testing.T) {
		later := now.Add(1 * time.Second)
		a := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 10}}
		b := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: &later}, Load: &ProviderLoadInfo{LoadRate: 10}}
		require.False(t, SameProviderWithLoadGroup(a, b))
	})

	t.Run("both nil LastUsedAt", func(t *testing.T) {
		a := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: nil}, Load: &ProviderLoadInfo{LoadRate: 0}}
		b := BasicCandidate{Provider: &BasicProvider{Priority: 1, LastUsedAt: nil}, Load: &ProviderLoadInfo{LoadRate: 0}}
		require.True(t, SameProviderWithLoadGroup(a, b))
	})
}

// TestSameProviderGroup compares provider pairs with shared or different
// priority and recency observations. It checks that the grouping predicate
// accepts the shared nil case and rejects distinct group keys.
func TestSameProviderGroup(t *testing.T) {
	now := time.Now()

	t.Run("same group", func(t *testing.T) {
		a := &BasicProvider{Priority: 1, LastUsedAt: nil}
		b := &BasicProvider{Priority: 1, LastUsedAt: nil}
		require.True(t, SameProviderGroup(a, b))
	})

	t.Run("different priority", func(t *testing.T) {
		a := &BasicProvider{Priority: 1, LastUsedAt: nil}
		b := &BasicProvider{Priority: 2, LastUsedAt: nil}
		require.False(t, SameProviderGroup(a, b))
	})

	t.Run("different LastUsedAt", func(t *testing.T) {
		later := now.Add(1 * time.Second)
		a := &BasicProvider{Priority: 1, LastUsedAt: &now}
		b := &BasicProvider{Priority: 1, LastUsedAt: &later}
		require.False(t, SameProviderGroup(a, b))
	})
}

// TestSortProvidersByPriorityAndLastUsed_WithShuffle compares equal-key
// randomization with ordering across distinct priorities. It requires multiple
// first IDs for peers and ascending numeric priority for the mixed-priority
// input.
func TestSortProvidersByPriorityAndLastUsed_WithShuffle(t *testing.T) {
	t.Run("same priority and nil LastUsedAt are shuffled", func(t *testing.T) {
		providers := []*BasicProvider{
			{ID: 1, Priority: 1, LastUsedAt: nil},
			{ID: 2, Priority: 1, LastUsedAt: nil},
			{ID: 3, Priority: 1, LastUsedAt: nil},
		}

		seen := map[int64]bool{}
		for range 100 {
			cpy := make([]*BasicProvider, len(providers))
			copy(cpy, providers)
			SortProvidersByPriorityAndLastUsed(cpy, false)
			seen[cpy[0].ID] = true
		}
		require.GreaterOrEqual(t, len(seen), 2, "identical sort keys should produce different orderings after shuffle")
	})

	t.Run("different priorities still sorted correctly", func(t *testing.T) {
		now := time.Now()
		providers := []*BasicProvider{
			{ID: 3, Priority: 3, LastUsedAt: &now},
			{ID: 1, Priority: 1, LastUsedAt: &now},
			{ID: 2, Priority: 2, LastUsedAt: &now},
		}

		SortProvidersByPriorityAndLastUsed(providers, false)
		require.Equal(t, int64(1), providers[0].ID)
		require.Equal(t, int64(2), providers[1].ID)
		require.Equal(t, int64(3), providers[2].ID)
	})
}

// TestSortProvidersByPriorityAndLastUsed_ByPriority sorts providers with equal
// use times and different numeric priorities. The resulting IDs must follow
// ascending priority so lower numeric values remain preferred.
func TestSortProvidersByPriorityAndLastUsed_ByPriority(t *testing.T) {
	now := time.Now()
	providers := []*BasicProvider{
		{ID: 1, Priority: 5, LastUsedAt: testTimePtr(now)},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 3, Priority: 3, LastUsedAt: testTimePtr(now)},
	}
	SortProvidersByPriorityAndLastUsed(providers, false)
	require.Equal(t, int64(2), providers[0].ID, "优先级最低的排第一")
	require.Equal(t, int64(3), providers[1].ID)
	require.Equal(t, int64(1), providers[2].ID)
}

// TestSortProvidersByPriorityAndLastUsed_SamePriorityByLastUsed sorts equally
// prioritized unused, older, and recently used providers. The IDs must place
// nil first and the older recorded timestamp before the latest use.
func TestSortProvidersByPriorityAndLastUsed_SamePriorityByLastUsed(t *testing.T) {
	now := time.Now()
	providers := []*BasicProvider{
		{ID: 1, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now.Add(-1 * time.Hour))},
		{ID: 3, Priority: 1, LastUsedAt: nil},
	}
	SortProvidersByPriorityAndLastUsed(providers, false)
	require.Equal(t, int64(3), providers[0].ID, "nil LastUsedAt 排最前")
	require.Equal(t, int64(2), providers[1].ID, "更早使用的排前面")
	require.Equal(t, int64(1), providers[2].ID)
}

// TestSortProvidersByPriorityAndLastUsed_PreferOAuth sorts unused API-key and
// OAuth providers at equal priority. Enabling preference must place the OAuth
// record first when neither candidate has a recorded use time.
func TestSortProvidersByPriorityAndLastUsed_PreferOAuth(t *testing.T) {
	providers := []*BasicProvider{
		{ID: 1, Priority: 1, LastUsedAt: nil, Type: capability.ProviderTypeAPIKey},
		{ID: 2, Priority: 1, LastUsedAt: nil, Type: capability.ProviderTypeOAuth},
	}
	SortProvidersByPriorityAndLastUsed(providers, true)
	require.Equal(t, int64(2), providers[0].ID, "preferOAuth 时 OAuth 提供商排前面")
}

// TestSortProvidersByPriorityAndLastUsed_StableSort repeatedly sorts three
// unused API-key providers with equal keys. Samples must retain every ID and
// vary the first record, protecting randomization after stable sorting.
func TestSortProvidersByPriorityAndLastUsed_StableSort(t *testing.T) {
	providers := []*BasicProvider{
		{ID: 1, Priority: 1, LastUsedAt: nil, Type: capability.ProviderTypeAPIKey},
		{ID: 2, Priority: 1, LastUsedAt: nil, Type: capability.ProviderTypeAPIKey},
		{ID: 3, Priority: 1, LastUsedAt: nil, Type: capability.ProviderTypeAPIKey},
	}

	// SortProvidersByPriorityAndLastUsed shuffles candidates tied on priority and last-used second.
	// Repeated runs retain the same elements while varying their order.
	seenFirst := map[int64]bool{}
	for range 100 {
		cpy := make([]*BasicProvider, len(providers))
		copy(cpy, providers)
		SortProvidersByPriorityAndLastUsed(cpy, false)
		seenFirst[cpy[0].ID] = true

		ids := map[int64]bool{}
		for _, a := range cpy {
			ids[a.ID] = true
		}
		require.True(t, ids[1] && ids[2] && ids[3])
	}
	require.GreaterOrEqual(t, len(seenFirst), 2, "同组提供商应能被随机打散")
}

// TestSortProvidersByPriorityAndLastUsed_MixedPriorityAndTime mixes recency
// observations across two priorities. IDs must stay in priority groups while
// older recorded use wins within one group and nil use wins within the other.
func TestSortProvidersByPriorityAndLastUsed_MixedPriorityAndTime(t *testing.T) {
	now := time.Now()
	providers := []*BasicProvider{
		{ID: 1, Priority: 2, LastUsedAt: nil},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 3, Priority: 1, LastUsedAt: testTimePtr(now.Add(-1 * time.Hour))},
		{ID: 4, Priority: 2, LastUsedAt: testTimePtr(now.Add(-2 * time.Hour))},
	}
	SortProvidersByPriorityAndLastUsed(providers, false)
	// Priority one records are compared by their populated use times in this
	// scenario, so the older record precedes the current one within that priority group.
	require.Equal(t, int64(3), providers[0].ID, "优先级1 + 更早")
	require.Equal(t, int64(2), providers[1].ID, "优先级1 + 现在")
	// Priority 2 follows; nil precedes a timestamp.
	require.Equal(t, int64(1), providers[2].ID, "优先级2 + nil")
	require.Equal(t, int64(4), providers[3].ID, "优先级2 + 有时间")
}

// TestFilterByMinPriority_Empty filters a nil candidate slice by priority. The
// returned slice must remain nil so callers retain the empty input's
// representation during basic candidate narrowing.
func TestFilterByMinPriority_Empty(t *testing.T) {
	result := FilterByMinPriority(nil)
	require.Nil(t, result)
}

// TestFilterByMinPriority_SelectsMinPriority filters providers with several
// numeric priorities and load observations. It checks that both
// lowest-priority IDs remain in input order while other priority values are
// removed.
func TestFilterByMinPriority_SelectsMinPriority(t *testing.T) {
	providers := []BasicCandidate{
		makeAccWithLoad(1, 5, 10, nil, capability.ProviderTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, capability.ProviderTypeAPIKey),
		makeAccWithLoad(3, 1, 20, nil, capability.ProviderTypeAPIKey),
		makeAccWithLoad(4, 2, 10, nil, capability.ProviderTypeAPIKey),
	}
	result := FilterByMinPriority(providers)
	require.Len(t, result, 2)
	require.Equal(t, int64(2), result[0].Provider.ID)
	require.Equal(t, int64(3), result[1].Provider.ID)
}

// TestFilterByMinLoadRate_Empty filters a nil candidate slice by observed
// load. The result must remain nil so an absent basic pool retains its
// representation after load preference is applied.
func TestFilterByMinLoadRate_Empty(t *testing.T) {
	result := FilterByMinLoadRate(nil)
	require.Nil(t, result)
}

// TestFilterByMinLoadRate_SelectsMinLoadRate filters equally prioritized
// providers whose load rates differ. It checks that both lowest-load IDs
// remain in their input order for subsequent recency selection.
func TestFilterByMinLoadRate_SelectsMinLoadRate(t *testing.T) {
	providers := []BasicCandidate{
		makeAccWithLoad(1, 1, 30, nil, capability.ProviderTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, capability.ProviderTypeAPIKey),
		makeAccWithLoad(3, 1, 10, nil, capability.ProviderTypeAPIKey),
		makeAccWithLoad(4, 1, 20, nil, capability.ProviderTypeAPIKey),
	}
	result := FilterByMinLoadRate(providers)
	require.Len(t, result, 2)
	require.Equal(t, int64(2), result[0].Provider.ID)
	require.Equal(t, int64(3), result[1].Provider.ID)
}

// TestSelectByLRU_Empty selects from a nil candidate slice with OAuth
// preference disabled. The result must be nil so an absent candidate pool
// produces no selected provider.
func TestSelectByLRU_Empty(t *testing.T) {
	result := SelectByLRU(nil, false)
	require.Nil(t, result)
}

// TestSelectByLRU_Single selects from a one-provider pool containing an unused
// record. The result must be nonnil and retain that provider's ID, protecting
// the singleton selection path.
func TestSelectByLRU_Single(t *testing.T) {
	providers := []BasicCandidate{makeAccWithLoad(1, 1, 10, nil, capability.ProviderTypeAPIKey)}
	result := SelectByLRU(providers, false)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.Provider.ID)
}

// TestSelectByLRU_NilLastUsedAtWins compares an unused provider with two
// recorded use timestamps. Selection must return the unused ID, protecting nil
// recency preference even when another provider was used long ago.
func TestSelectByLRU_NilLastUsedAtWins(t *testing.T) {
	now := time.Now()
	providers := []BasicCandidate{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), capability.ProviderTypeAPIKey),
		makeAccWithLoad(2, 1, 10, nil, capability.ProviderTypeAPIKey),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(-1*time.Hour)), capability.ProviderTypeAPIKey),
	}
	result := SelectByLRU(providers, false)
	require.NotNil(t, result)
	require.Equal(t, int64(2), result.Provider.ID)
}

// TestSelectByLRU_EarliestTimeWins compares three providers with populated use
// timestamps and equal priority and load. It requires the oldest recorded ID
// to win the recency decision.
func TestSelectByLRU_EarliestTimeWins(t *testing.T) {
	now := time.Now()
	providers := []BasicCandidate{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), capability.ProviderTypeAPIKey),
		makeAccWithLoad(2, 1, 10, testTimePtr(now.Add(-1*time.Hour)), capability.ProviderTypeAPIKey),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(-2*time.Hour)), capability.ProviderTypeAPIKey),
	}
	result := SelectByLRU(providers, false)
	require.NotNil(t, result)
	require.Equal(t, int64(3), result.Provider.ID)
}

// TestSelectByLRU_TiePreferOAuth places OAuth and API-key providers in an
// exact recency tie beside a newer record. Repeated choices must return the
// tied OAuth ID when preference is enabled.
func TestSelectByLRU_TiePreferOAuth(t *testing.T) {
	now := time.Now()
	// Providers 1 and 2 share the earliest last-used time.
	providers := []BasicCandidate{
		makeAccWithLoad(1, 1, 10, testTimePtr(now), capability.ProviderTypeAPIKey),
		makeAccWithLoad(2, 1, 10, testTimePtr(now), capability.ProviderTypeOAuth),
		makeAccWithLoad(3, 1, 10, testTimePtr(now.Add(1*time.Hour)), capability.ProviderTypeAPIKey),
	}
	for range 50 {
		result := SelectByLRU(providers, true)
		require.NotNil(t, result)
		require.Equal(t, capability.ProviderTypeOAuth, result.Provider.Type)
		require.Equal(t, int64(2), result.Provider.ID)
	}
}

// TestFilterByMinPriority exercises empty, singleton, equal-priority, and
// mixed-priority pools. It checks retained lengths and ordered lowest-priority
// IDs so priority filtering keeps all peers eligible for later decisions.
func TestFilterByMinPriority(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		result := FilterByMinPriority(nil)
		require.Empty(t, result)
	})

	t.Run("single provider", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, Priority: 5}, Load: &ProviderLoadInfo{}},
		}
		result := FilterByMinPriority(providers)
		require.Len(t, result, 1)
		require.Equal(t, int64(1), result[0].Provider.ID)
	})

	t.Run("multiple providers same priority", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, Priority: 3}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, Priority: 3}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 3, Priority: 3}, Load: &ProviderLoadInfo{}},
		}
		result := FilterByMinPriority(providers)
		require.Len(t, result, 3)
	})

	t.Run("filters to min priority only", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, Priority: 5}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, Priority: 1}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 3, Priority: 3}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 4, Priority: 1}, Load: &ProviderLoadInfo{}},
		}
		result := FilterByMinPriority(providers)
		require.Len(t, result, 2)
		require.Equal(t, int64(2), result[0].Provider.ID)
		require.Equal(t, int64(4), result[1].Provider.ID)
	})
}

// TestFilterByMinLoadRate exercises empty, singleton, equal-load, mixed-load,
// and explicit zero-load pools. It checks lengths and IDs so the lowest
// observed load remains the filter's complete tie set.
func TestFilterByMinLoadRate(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		result := FilterByMinLoadRate(nil)
		require.Empty(t, result)
	})

	t.Run("single provider", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1}, Load: &ProviderLoadInfo{LoadRate: 50}},
		}
		result := FilterByMinLoadRate(providers)
		require.Len(t, result, 1)
		require.Equal(t, int64(1), result[0].Provider.ID)
	})

	t.Run("multiple providers same load rate", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1}, Load: &ProviderLoadInfo{LoadRate: 20}},
			{Provider: &BasicProvider{ID: 2}, Load: &ProviderLoadInfo{LoadRate: 20}},
			{Provider: &BasicProvider{ID: 3}, Load: &ProviderLoadInfo{LoadRate: 20}},
		}
		result := FilterByMinLoadRate(providers)
		require.Len(t, result, 3)
	})

	t.Run("filters to min load rate only", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1}, Load: &ProviderLoadInfo{LoadRate: 80}},
			{Provider: &BasicProvider{ID: 2}, Load: &ProviderLoadInfo{LoadRate: 10}},
			{Provider: &BasicProvider{ID: 3}, Load: &ProviderLoadInfo{LoadRate: 50}},
			{Provider: &BasicProvider{ID: 4}, Load: &ProviderLoadInfo{LoadRate: 10}},
		}
		result := FilterByMinLoadRate(providers)
		require.Len(t, result, 2)
		require.Equal(t, int64(2), result[0].Provider.ID)
		require.Equal(t, int64(4), result[1].Provider.ID)
	})

	t.Run("zero load rate", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1}, Load: &ProviderLoadInfo{LoadRate: 0}},
			{Provider: &BasicProvider{ID: 2}, Load: &ProviderLoadInfo{LoadRate: 50}},
			{Provider: &BasicProvider{ID: 3}, Load: &ProviderLoadInfo{LoadRate: 0}},
		}
		result := FilterByMinLoadRate(providers)
		require.Len(t, result, 2)
		require.Equal(t, int64(1), result[0].Provider.ID)
		require.Equal(t, int64(3), result[1].Provider.ID)
	})
}

// TestSelectByLRU covers missing pools, exact recency ties, and OAuth
// preference with varied last-use observations. Its random cases check
// eligible membership, while deterministic cases check the chosen ID or kind.
func TestSelectByLRU(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)
	muchEarlier := now.Add(-2 * time.Hour)

	t.Run("empty slice", func(t *testing.T) {
		result := SelectByLRU(nil, false)
		require.Nil(t, result)
	})

	t.Run("single provider", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{}},
		}
		result := SelectByLRU(providers, false)
		require.NotNil(t, result)
		require.Equal(t, int64(1), result.Provider.ID)
	})

	t.Run("selects least recently used", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, LastUsedAt: &muchEarlier}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 3, LastUsedAt: &earlier}, Load: &ProviderLoadInfo{}},
		}
		result := SelectByLRU(providers, false)
		require.NotNil(t, result)
		require.Equal(t, int64(2), result.Provider.ID)
	})

	t.Run("nil LastUsedAt preferred over non-nil", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, LastUsedAt: nil}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 3, LastUsedAt: &earlier}, Load: &ProviderLoadInfo{}},
		}
		result := SelectByLRU(providers, false)
		require.NotNil(t, result)
		require.Equal(t, int64(2), result.Provider.ID)
	})

	t.Run("multiple nil LastUsedAt random selection", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, LastUsedAt: nil, Type: "session"}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, LastUsedAt: nil, Type: "session"}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 3, LastUsedAt: nil, Type: "session"}, Load: &ProviderLoadInfo{}},
		}
		// Repeated samples must stay within the eligible candidates.
		validIDs := map[int64]bool{1: true, 2: true, 3: true}
		for range 10 {
			result := SelectByLRU(providers, false)
			require.NotNil(t, result)
			require.True(t, validIDs[result.Provider.ID], "selected ID should be one of the candidates")
		}
	})

	t.Run("multiple same LastUsedAt random selection", func(t *testing.T) {
		sameTime := now
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, LastUsedAt: &sameTime}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, LastUsedAt: &sameTime}, Load: &ProviderLoadInfo{}},
		}
		// These samples check membership of an exact recency tie. No variation
		// assertion is made, so either eligible provider may repeat throughout the sample.
		validIDs := map[int64]bool{1: true, 2: true}
		for range 10 {
			result := SelectByLRU(providers, false)
			require.NotNil(t, result)
			require.True(t, validIDs[result.Provider.ID], "selected ID should be one of the candidates")
		}
	})

	t.Run("preferOAuth selects from OAuth providers when multiple nil", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, LastUsedAt: nil, Type: "session"}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, LastUsedAt: nil, Type: capability.ProviderTypeOAuth}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 3, LastUsedAt: nil, Type: capability.ProviderTypeOAuth}, Load: &ProviderLoadInfo{}},
		}
		// OAuth preference restricts the tie to OAuth candidates.
		oauthIDs := map[int64]bool{2: true, 3: true}
		for range 10 {
			result := SelectByLRU(providers, true)
			require.NotNil(t, result)
			require.True(t, oauthIDs[result.Provider.ID], "should select from OAuth providers")
		}
	})

	t.Run("preferOAuth falls back to all when no OAuth", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, LastUsedAt: nil, Type: "session"}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, LastUsedAt: nil, Type: "session"}, Load: &ProviderLoadInfo{}},
		}
		// With no OAuth candidate, the full tied set remains eligible.
		validIDs := map[int64]bool{1: true, 2: true}
		for range 10 {
			result := SelectByLRU(providers, true)
			require.NotNil(t, result)
			require.True(t, validIDs[result.Provider.ID])
		}
	})

	t.Run("preferOAuth only affects same LastUsedAt providers", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, LastUsedAt: &earlier, Type: "session"}, Load: &ProviderLoadInfo{}},
			{Provider: &BasicProvider{ID: 2, LastUsedAt: &now, Type: capability.ProviderTypeOAuth}, Load: &ProviderLoadInfo{}},
		}
		result := SelectByLRU(providers, true)
		require.NotNil(t, result)
		// Different last-used times select the least recently used provider.
		require.Equal(t, int64(1), result.Provider.ID)
	})
}

// TestLayeredFilterIntegration applies priority filtering, load filtering, and
// LRU selection in sequence. Intermediate pool sizes and final IDs must
// reflect each preference, including a pool tied on priority and load.
func TestLayeredFilterIntegration(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)
	muchEarlier := now.Add(-2 * time.Hour)

	t.Run("full layered selection", func(t *testing.T) {
		// Candidates differ in priority, load, and last-used time.
		providers := []BasicCandidate{
			// Priority 1, 50% load.
			{Provider: &BasicProvider{ID: 1, Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 50}},
			// Priority 1, 20% load (lowest).
			{Provider: &BasicProvider{ID: 2, Priority: 1, LastUsedAt: &earlier}, Load: &ProviderLoadInfo{LoadRate: 20}},
			// Priority 1, 20% load (lowest), used earlier.
			{Provider: &BasicProvider{ID: 3, Priority: 1, LastUsedAt: &muchEarlier}, Load: &ProviderLoadInfo{LoadRate: 20}},
			// Priority 2 (lower preference).
			{Provider: &BasicProvider{ID: 4, Priority: 2, LastUsedAt: &muchEarlier}, Load: &ProviderLoadInfo{LoadRate: 0}},
		}

		// IDs 1, 2, and 3 share the best priority.
		step1 := FilterByMinPriority(providers)
		require.Len(t, step1, 3)

		// IDs 2 and 3 share the lowest load.
		step2 := FilterByMinLoadRate(step1)
		require.Len(t, step2, 2)

		// ID 3 has the earliest last-used time, muchEarlier.
		selected := SelectByLRU(step2, false)
		require.NotNil(t, selected)
		require.Equal(t, int64(3), selected.Provider.ID)
	})

	t.Run("all same priority and load rate", func(t *testing.T) {
		providers := []BasicCandidate{
			{Provider: &BasicProvider{ID: 1, Priority: 1, LastUsedAt: &now}, Load: &ProviderLoadInfo{LoadRate: 50}},
			{Provider: &BasicProvider{ID: 2, Priority: 1, LastUsedAt: &earlier}, Load: &ProviderLoadInfo{LoadRate: 50}},
			{Provider: &BasicProvider{ID: 3, Priority: 1, LastUsedAt: &muchEarlier}, Load: &ProviderLoadInfo{LoadRate: 50}},
		}

		step1 := FilterByMinPriority(providers)
		require.Len(t, step1, 3)

		step2 := FilterByMinLoadRate(step1)
		require.Len(t, step2, 3)

		selected := SelectByLRU(step2, false)
		require.NotNil(t, selected)
		require.Equal(t, int64(3), selected.Provider.ID)
	})
}

// TestFilterBySoonestReset_PicksSoonestFutureWindow compares future windows
// ending at different times. Only the soonest-reset provider must remain,
// preserving the optional reset preference before subsequent basic decisions.
func TestFilterBySoonestReset_PicksSoonestFutureWindow(t *testing.T) {
	now := time.Now()
	soon := now.Add(1 * time.Hour)
	later := now.Add(24 * time.Hour)
	providers := []BasicCandidate{
		accWithWindowEnd(1, testTimePtr(later)),
		accWithWindowEnd(2, testTimePtr(soon)),
		accWithWindowEnd(3, testTimePtr(later)),
	}
	got := FilterBySoonestReset(providers, time.Now)
	require.Len(t, got, 1)
	require.Equal(t, int64(2), got[0].Provider.ID, "重置时间最早的提供商被选中")
}

// TestFilterBySoonestReset_IgnoresNilAndExpiredWindows mixes missing, expired,
// and future reset observations. It requires the sole active-window ID to
// remain when any future window supplies a reset preference.
func TestFilterBySoonestReset_IgnoresNilAndExpiredWindows(t *testing.T) {
	now := time.Now()
	expired := now.Add(-1 * time.Hour)
	active := now.Add(2 * time.Hour)
	providers := []BasicCandidate{
		accWithWindowEnd(1, nil),                  // No active window.
		accWithWindowEnd(2, testTimePtr(expired)), // Expired windows are inactive.
		accWithWindowEnd(3, testTimePtr(active)),  // The only active window.
	}
	got := FilterBySoonestReset(providers, time.Now)
	require.Len(t, got, 1)
	require.Equal(t, int64(3), got[0].Provider.ID, "仅保留拥有未来重置时间的提供商")
}

// TestFilterBySoonestReset_NoActiveWindowReturnsAll supplies missing and
// expired reset observations with no future window. Both records must remain
// available so reset filtering does not exhaust a pool lacking active
// observations.
func TestFilterBySoonestReset_NoActiveWindowReturnsAll(t *testing.T) {
	now := time.Now()
	expired := now.Add(-30 * time.Minute)
	providers := []BasicCandidate{
		accWithWindowEnd(1, nil),
		accWithWindowEnd(2, testTimePtr(expired)),
	}
	got := FilterBySoonestReset(providers, time.Now)
	require.Len(t, got, 2, "没有任何提供商拥有活跃窗口时，返回原集合不做过滤")
}

// TestFilterBySoonestReset_TiedSoonestKeepsAll supplies two identical earliest
// future reset times beside a later window. Both earliest IDs must remain
// eligible for a later load or recency decision.
func TestFilterBySoonestReset_TiedSoonestKeepsAll(t *testing.T) {
	now := time.Now()
	end := now.Add(90 * time.Minute)
	providers := []BasicCandidate{
		accWithWindowEnd(1, testTimePtr(end)),
		accWithWindowEnd(2, testTimePtr(end)),
		accWithWindowEnd(3, testTimePtr(now.Add(5*time.Hour))),
	}
	got := FilterBySoonestReset(providers, time.Now)
	require.Len(t, got, 2, "并列最早重置的提供商都保留，交由后续 LRU 决定")
	ids := map[int64]bool{got[0].Provider.ID: true, got[1].Provider.ID: true}
	require.True(t, ids[1] && ids[2])
}

// TestFilterBySoonestReset_SingleOrEmptyUnchanged passes an empty pool and a
// singleton with no reset observation. It checks empty acceptance and
// singleton retention before clock-dependent window filtering is needed.
func TestFilterBySoonestReset_SingleOrEmptyUnchanged(t *testing.T) {
	require.Empty(t, FilterBySoonestReset(nil, time.Now))
	single := []BasicCandidate{accWithWindowEnd(1, nil)}
	require.Len(t, FilterBySoonestReset(single, time.Now), 1)
}

// testTimePtr returns a pointer to a separate copy of the supplied time value.
// Tests can adjust that pointed-to observation while the caller's original
// time variable retains its value.
func testTimePtr(t time.Time) *time.Time { return &t }

// makeAccWithLoad constructs provider metadata and a separate load record for
// basic selection tests. It retains the supplied last-use pointer while
// creating new candidate and load storage with the requested IDs and
// observations.
func makeAccWithLoad(id int64, priority int, loadRate int, lastUsed *time.Time, accType string) BasicCandidate {
	return BasicCandidate{
		Provider: &BasicProvider{
			ID:         id,
			Priority:   priority,
			LastUsedAt: lastUsed,
			Type:       accType,
		},
		Load: &ProviderLoadInfo{
			ProviderID:         id,
			CurrentConcurrency: 0,
			LoadRate:           loadRate,
		},
	}
}

// accWithWindowEnd creates a basic candidate with the supplied reset
// observation and a zero-load record. Its provider retains the end pointer,
// letting tests express missing, expired, and tied future windows.
func accWithWindowEnd(id int64, end *time.Time) BasicCandidate {
	return BasicCandidate{
		Provider: &BasicProvider{
			ID: id,

			SessionWindowEnd: end,
		},
		Load: &ProviderLoadInfo{ProviderID: id},
	}
}

package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestShuffleWithinSortGroups_Empty(t *testing.T) {
	ShuffleWithinSortGroups(nil)
	ShuffleWithinSortGroups([]BasicCandidate{})
}

func TestShuffleWithinSortGroups_SingleElement(t *testing.T) {
	providers := []BasicCandidate{
		{Provider: &BasicProvider{ID: 1, Priority: 1}, Load: &ProviderLoadInfo{LoadRate: 10}},
	}
	ShuffleWithinSortGroups(providers)
	require.Equal(t, int64(1), providers[0].Provider.ID)
}

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

func TestShuffleWithinSortGroups_SameGroup_Shuffled(t *testing.T) {
	now := time.Now()
	// Timestamps in the same second form one shuffle group.
	sameSecond := time.Unix(now.Unix(), 0)
	sameSecond2 := time.Unix(now.Unix(), 500_000_000) // Same second, different nanoseconds.

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

func TestShuffleWithinPriorityAndLastUsed_Empty(t *testing.T) {
	ShuffleWithinPriorityAndLastUsed(nil, false)
	ShuffleWithinPriorityAndLastUsed([]*BasicProvider{}, false)
}

func TestShuffleWithinPriorityAndLastUsed_SingleElement(t *testing.T) {
	providers := []*BasicProvider{{ID: 1, Priority: 1}}
	ShuffleWithinPriorityAndLastUsed(providers, false)
	require.Equal(t, int64(1), providers[0].ID)
}

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

func TestSortProvidersByPriorityAndLastUsed_PreferOAuth(t *testing.T) {
	providers := []*BasicProvider{
		{ID: 1, Priority: 1, LastUsedAt: nil, Type: capability.ProviderTypeAPIKey},
		{ID: 2, Priority: 1, LastUsedAt: nil, Type: capability.ProviderTypeOAuth},
	}
	SortProvidersByPriorityAndLastUsed(providers, true)
	require.Equal(t, int64(2), providers[0].ID, "preferOAuth 时 OAuth 提供商排前面")
}

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

func TestSortProvidersByPriorityAndLastUsed_MixedPriorityAndTime(t *testing.T) {
	now := time.Now()
	providers := []*BasicProvider{
		{ID: 1, Priority: 2, LastUsedAt: nil},
		{ID: 2, Priority: 1, LastUsedAt: testTimePtr(now)},
		{ID: 3, Priority: 1, LastUsedAt: testTimePtr(now.Add(-1 * time.Hour))},
		{ID: 4, Priority: 2, LastUsedAt: testTimePtr(now.Add(-2 * time.Hour))},
	}
	SortProvidersByPriorityAndLastUsed(providers, false)
	// Priority 1 comes first; nil precedes earlier.
	require.Equal(t, int64(3), providers[0].ID, "优先级1 + 更早")
	require.Equal(t, int64(2), providers[1].ID, "优先级1 + 现在")
	// Priority 2 follows; nil precedes a timestamp.
	require.Equal(t, int64(1), providers[2].ID, "优先级2 + nil")
	require.Equal(t, int64(4), providers[3].ID, "优先级2 + 有时间")
}

func TestFilterByMinPriority_Empty(t *testing.T) {
	result := FilterByMinPriority(nil)
	require.Nil(t, result)
}

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

func TestFilterByMinLoadRate_Empty(t *testing.T) {
	result := FilterByMinLoadRate(nil)
	require.Nil(t, result)
}

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

func TestSelectByLRU_Empty(t *testing.T) {
	result := SelectByLRU(nil, false)
	require.Nil(t, result)
}

func TestSelectByLRU_Single(t *testing.T) {
	providers := []BasicCandidate{makeAccWithLoad(1, 1, 10, nil, capability.ProviderTypeAPIKey)}
	result := SelectByLRU(providers, false)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.Provider.ID)
}

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
		// Repeat sampling to check that the first candidate varies.
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

func TestFilterBySoonestReset_SingleOrEmptyUnchanged(t *testing.T) {
	require.Empty(t, FilterBySoonestReset(nil, time.Now))
	single := []BasicCandidate{accWithWindowEnd(1, nil)}
	require.Len(t, FilterBySoonestReset(single, time.Now), 1)
}

func testTimePtr(t time.Time) *time.Time { return &t }

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

func accWithWindowEnd(id int64, end *time.Time) BasicCandidate {
	return BasicCandidate{
		Provider: &BasicProvider{
			ID: id,

			SessionWindowEnd: end,
		},
		Load: &ProviderLoadInfo{ProviderID: id},
	}
}

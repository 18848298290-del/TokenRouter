package provider

import (
	"context"
	"slices"
	"sort"

	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

// SchedulerScoreProviders reads candidate pools for administrative scoring independently of page size.
type SchedulerScoreProviders interface {
	ListProvidersForSchedulerScoreFilter(context.Context, string, string, string, string, int64, string) ([]Record, error)
	ListSchedulableProvidersForAdvancedSchedulerScore(context.Context, *int64, string) ([]Record, error)
}

type SchedulerLoadRequest struct {
	ID             int64
	MaxConcurrency int
}

type SchedulerLoad struct {
	ProviderID                                 int64
	CurrentConcurrency, WaitingCount, LoadRate int
}

// SchedulerScoreOptions supplies load and scoring callbacks using shared scheduler feedback.
type SchedulerScoreOptions struct {
	Load  func(context.Context, []SchedulerLoadRequest) (map[int64]*SchedulerLoad, error)
	Score func(context.Context, *accessview.GroupConfig, []*Record, map[int64]*SchedulerLoad) map[int64]ProviderSchedulerScore
	Warn  func(string, ...any)
}

type SchedulerScoreView struct {
	adminService SchedulerScoreProviders
	options      SchedulerScoreOptions
}

// ProviderSchedulerScore holds scheduling scores displayed to administrators.
type ProviderSchedulerScore struct {
	BaseScore             float64 `json:"base_score"`
	StickyScore           float64 `json:"sticky_score"`
	StickyScoreInfinity   bool    `json:"sticky_score_infinity"`
	StickyWeightedEnabled bool    `json:"sticky_weighted_enabled"`
}

// ProviderSchedulerGroupScore holds a provider's scheduling score within one group.
type ProviderSchedulerGroupScore struct {
	GroupID   *int64 `json:"group_id"`
	GroupName string `json:"group_name,omitempty"`
	ProviderSchedulerScore
}

func NewSchedulerScoreView(reader SchedulerScoreProviders, options SchedulerScoreOptions) *SchedulerScoreView {
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	return &SchedulerScoreView{reader, options}
}

// scoreAdvancedSchedulerPool scores the schedulable providers in a pool.
// A supplied loadMap may include extra providers; nil triggers a batch load read.
func (h *SchedulerScoreView) scoreAdvancedSchedulerPool(ctx context.Context, group *accessview.GroupConfig, providers []Record, loadMap map[int64]*SchedulerLoad) map[int64]ProviderSchedulerScore {
	if len(providers) == 0 {
		return nil
	}

	schedulableProviders := make([]*Record, 0, len(providers))
	for i := range providers {
		provider := &providers[i]
		if !provider.IsSchedulable() {
			continue
		}
		schedulableProviders = append(schedulableProviders, provider)
	}
	if len(schedulableProviders) == 0 {
		return nil
	}

	if loadMap == nil {
		loadMap = h.fetchAdvancedSchedulerLoadMap(ctx, schedulableProviders)
	}

	scores := h.options.Score(ctx, group, schedulableProviders, loadMap)
	result := make(map[int64]ProviderSchedulerScore, len(scores))
	for providerID, score := range scores {
		result[providerID] = ProviderSchedulerScore{
			BaseScore:             score.BaseScore,
			StickyScore:           score.StickyScore,
			StickyScoreInfinity:   score.StickyScoreInfinity,
			StickyWeightedEnabled: score.StickyWeightedEnabled,
		}
	}
	return result
}

// fetchAdvancedSchedulerLoadMap reads candidate loads in one deduplicated batch.
// Read failures are logged and return an empty map; missing load observations receive neutral scores.
func (h *SchedulerScoreView) fetchAdvancedSchedulerLoadMap(ctx context.Context, providers []*Record) map[int64]*SchedulerLoad {
	loadMap := map[int64]*SchedulerLoad{}
	if h.options.Load == nil || len(providers) == 0 {
		return loadMap
	}
	seen := make(map[int64]struct{}, len(providers))
	loadReq := make([]SchedulerLoadRequest, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		if _, ok := seen[provider.ID]; ok {
			continue
		}
		seen[provider.ID] = struct{}{}
		loadReq = append(loadReq, SchedulerLoadRequest{
			ID:             provider.ID,
			MaxConcurrency: provider.EffectiveLoadFactor(),
		})
	}
	if batchLoad, err := h.options.Load(ctx, loadReq); err != nil {
		h.options.Warn("advanced_scheduler_score_load_batch_failed", "error", err)
	} else if batchLoad != nil {
		loadMap = batchLoad
	}
	return loadMap
}

func (h *SchedulerScoreView) buildAdvancedProviderSchedulerScores(
	ctx context.Context,
	providers []Record,
	filterPool []Record,
) (map[int64]*ProviderSchedulerScore, map[int64][]ProviderSchedulerGroupScore) {
	if len(providers) == 0 {
		return nil, nil
	}
	if len(filterPool) == 0 {
		filterPool = providers
	}

	pageProviderIDs := make(map[int64]struct{})
	advancedGroups := make(map[int64]*accessview.GroupConfig)
	for i := range providers {
		provider := &providers[i]
		pageProviderIDs[provider.ID] = struct{}{}
		if len(provider.ProviderGroups) == 0 {
			continue
		}
		for _, providerGroup := range provider.ProviderGroups {
			if providerGroup.GroupID > 0 && providerGroup.Group != nil && providerGroup.Group.SchedulerType == "advanced" {
				advancedGroups[providerGroup.GroupID] = providerGroup.Group
			}
		}
	}
	if len(pageProviderIDs) == 0 {
		return nil, nil
	}

	// Collect group pools before querying loads for the union of the filtered and group pools.
	// Each scoring pool then shares that batch of load observations.
	groupIDList := make([]int64, 0, len(advancedGroups))
	for groupID := range advancedGroups {
		groupIDList = append(groupIDList, groupID)
	}
	slices.Sort(groupIDList)

	groupPools := make(map[int64][]Record, len(groupIDList))
	if h.adminService != nil {
		for _, groupID := range groupIDList {
			gid := groupID
			group := advancedGroups[gid]
			if group == nil {
				continue
			}
			pool, err := h.adminService.ListSchedulableProvidersForAdvancedSchedulerScore(ctx, &gid, "")
			if err != nil {
				h.options.Warn("advanced_scheduler_group_score_pool_failed", "group_id", gid, "error", err)
				continue
			}
			groupPools[gid] = pool
		}
	}

	loadUnion := make([]*Record, 0, len(filterPool))
	collectAdvancedProviders := func(pool []Record) {
		for i := range pool {
			loadUnion = append(loadUnion, &pool[i])
		}
	}
	collectAdvancedProviders(filterPool)
	for _, pool := range groupPools {
		collectAdvancedProviders(pool)
	}
	loadMap := h.fetchAdvancedSchedulerLoadMap(ctx, loadUnion)

	baseScores := make(map[int64]*ProviderSchedulerScore)
	for providerID, score := range h.scoreAdvancedSchedulerPool(ctx, nil, filterPool, loadMap) {
		copiedScore := score
		baseScores[providerID] = &copiedScore
	}

	groupScoresByProvider := make(map[int64][]ProviderSchedulerGroupScore)
	scoreGroupPool := func(groupID *int64, group *accessview.GroupConfig, groupNameByID map[int64]string, pool []Record) {
		if len(pool) == 0 {
			return
		}
		scores := h.scoreAdvancedSchedulerPool(ctx, group, pool, loadMap)
		for providerID, schedulerScore := range scores {
			if _, ok := pageProviderIDs[providerID]; !ok {
				continue
			}
			groupScore := ProviderSchedulerGroupScore{
				GroupID:                groupID,
				ProviderSchedulerScore: schedulerScore,
			}
			if groupID != nil {
				groupScore.GroupName = groupNameByID[*groupID]
			}
			groupScoresByProvider[providerID] = append(groupScoresByProvider[providerID], groupScore)
		}
	}

	for _, groupID := range groupIDList {
		gid := groupID
		pool, ok := groupPools[gid]
		if !ok {
			continue
		}
		groupNameByID := make(map[int64]string)
		for i := range pool {
			provider := &pool[i]
			for _, providerGroup := range provider.ProviderGroups {
				if providerGroup.GroupID != gid {
					continue
				}
				if providerGroup.Group != nil {
					groupNameByID[gid] = providerGroup.Group.Name
				}
			}
		}
		scoreGroupPool(&gid, advancedGroups[gid], groupNameByID, pool)
	}
	// Expose base scores for providers with advanced group scores; basic or ungrouped providers omit this view.
	for providerID := range baseScores {
		if _, ok := groupScoresByProvider[providerID]; !ok {
			delete(baseScores, providerID)
		}
	}

	for providerID := range groupScoresByProvider {
		sort.SliceStable(groupScoresByProvider[providerID], func(i, j int) bool {
			left := groupScoresByProvider[providerID][i]
			right := groupScoresByProvider[providerID][j]
			return *left.GroupID < *right.GroupID
		})
	}
	return baseScores, groupScoresByProvider
}

func (h *SchedulerScoreView) listProviderSchedulerScoreFilterPool(
	ctx context.Context,
	platform, providerType, status, search string,
	groupID int64,
	privacyMode string,
) []Record {
	if h.adminService == nil {
		return nil
	}
	providers, err := h.adminService.ListProvidersForSchedulerScoreFilter(ctx, platform, providerType, status, search, groupID, privacyMode)
	if err != nil {
		h.options.Warn("advanced_scheduler_filter_score_pool_failed", "error", err)
		return nil
	}
	return providers
}

// Build scores the filtered and group pools using shared load data and returns group scores ordered by group ID.
func (h *SchedulerScoreView) Build(ctx context.Context, values []Record, platform, kind, status, search string, groupID int64, privacy string) (map[int64]*ProviderSchedulerScore, map[int64][]ProviderSchedulerGroupScore) {
	pool := h.listProviderSchedulerScoreFilterPool(ctx, platform, kind, status, search, groupID, privacy)
	return h.buildAdvancedProviderSchedulerScores(ctx, values, pool)
}

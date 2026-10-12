package ops

import (
	"context"
	"log"
	"maps"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

const (
	opsProvidersPageSize         = 100
	opsConcurrencyBatchChunkSize = 200
)

// opsProviderStatsRepository reads the provider fields needed for live statistics.
type opsProviderStatsRepository interface {
	ListOpsProvidersForStats(ctx context.Context, platformFilter string, groupIDFilter *int64) ([]ProviderObservation, error)
}

// listAllProvidersForOps uses the statistics query when supported and otherwise uses the paginated interface.
func (s *OpsService) listAllProvidersForOps(ctx context.Context, platformFilter string, groupIDFilter *int64) ([]ProviderObservation, error) {
	if s == nil || s.providerRepo == nil {
		return []ProviderObservation{}, nil
	}
	if repo, ok := s.providerRepo.(opsProviderStatsRepository); ok {
		return repo.ListOpsProvidersForStats(ctx, platformFilter, groupIDFilter)
	}

	out := make([]ProviderObservation, 0, 128)
	page := 1
	groupID := int64(0)
	if groupIDFilter != nil {
		groupID = *groupIDFilter
	}
	for {
		providers, pageInfo, err := s.providerRepo.ListPage(ctx, pagination.PaginationParams{
			Page:     page,
			PageSize: opsProvidersPageSize,
		}, platformFilter, groupID)
		if err != nil {
			return nil, err
		}
		if len(providers) == 0 {
			break
		}

		out = append(out, providers...)
		if pageInfo != nil && int64(len(out)) >= pageInfo.Total {
			break
		}
		if len(providers) < opsProvidersPageSize {
			break
		}

		page++
		if page > 10_000 {
			log.Printf("[Ops] listAllProvidersForOps: aborting after too many pages (platform=%q)", platformFilter)
			break
		}
	}

	return out, nil
}

func (s *OpsService) getProvidersLoadMapBestEffort(ctx context.Context, providers []ProviderObservation) map[int64]*ProviderLoadInfo {
	if s == nil || s.concurrencyService == nil {
		return map[int64]*ProviderLoadInfo{}
	}
	if len(providers) == 0 {
		return map[int64]*ProviderLoadInfo{}
	}

	// De-duplicate IDs (and keep the max concurrency to avoid under-reporting).
	unique := make(map[int64]int, len(providers))
	for _, acc := range providers {
		if acc.ID <= 0 {
			continue
		}
		lf := acc.EffectiveLoadFactor()
		if prev, ok := unique[acc.ID]; !ok || lf > prev {
			unique[acc.ID] = lf
		}
	}

	batch := make([]ProviderWithConcurrency, 0, len(unique))
	for id, maxConc := range unique {
		batch = append(batch, ProviderWithConcurrency{
			ID:             id,
			MaxConcurrency: maxConc,
		})
	}

	out := make(map[int64]*ProviderLoadInfo, len(batch))
	for i := 0; i < len(batch); i += opsConcurrencyBatchChunkSize {
		end := min(i+opsConcurrencyBatchChunkSize, len(batch))
		part, err := s.concurrencyService.GetProvidersLoadBatch(ctx, batch[i:end])
		if err != nil {
			// Missing load entries are displayed as zero when a batch query fails.
			log.Printf("[Ops] GetProvidersLoadBatch failed: %v", err)
			continue
		}
		maps.Copy(out, part)
	}

	return out
}

// GetConcurrencyStats returns real-time concurrency usage aggregated by platform/group/provider.
//
// Optional filters:
// - platformFilter: only include providers in that platform (best-effort reduces DB load)
// - groupIDFilter: only include providers that belong to that group.
func (s *OpsService) GetConcurrencyStats(
	ctx context.Context,
	platformFilter string,
	groupIDFilter *int64,
) (map[string]*PlatformConcurrencyInfo, map[int64]*GroupConcurrencyInfo, map[int64]*ProviderConcurrencyInfo, *time.Time, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, nil, nil, nil, err
	}

	providers, err := s.listAllProvidersForOps(ctx, platformFilter, groupIDFilter)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	collectedAt := time.Now()
	loadMap := s.getProvidersLoadMapBestEffort(ctx, providers)

	platform := make(map[string]*PlatformConcurrencyInfo)
	group := make(map[int64]*GroupConcurrencyInfo)
	provider := make(map[int64]*ProviderConcurrencyInfo)

	for _, acc := range providers {
		if acc.ID <= 0 {
			continue
		}

		var matchedGroup *GroupObservation
		if groupIDFilter != nil && *groupIDFilter > 0 {
			for _, grp := range acc.Groups {
				if grp == nil || grp.ID <= 0 {
					continue
				}
				if grp.ID == *groupIDFilter {
					matchedGroup = grp
					break
				}
			}
			// GroupObservation filter provided: skip providers not in that group.
			if matchedGroup == nil {
				continue
			}
		}

		load := loadMap[acc.ID]
		currentInUse := int64(0)
		waiting := int64(0)
		if load != nil {
			currentInUse = int64(load.CurrentConcurrency)
			waiting = int64(load.WaitingCount)
		}

		// Display the matched group, or the provider's first group when no group filter is set.
		displayGroupID := int64(0)
		displayGroupName := ""
		if matchedGroup != nil {
			displayGroupID = matchedGroup.ID
			displayGroupName = matchedGroup.Name
		} else if len(acc.Groups) > 0 && acc.Groups[0] != nil {
			displayGroupID = acc.Groups[0].ID
			displayGroupName = acc.Groups[0].Name
		}

		if _, ok := provider[acc.ID]; !ok {
			info := &ProviderConcurrencyInfo{
				ProviderID:     acc.ID,
				ProviderName:   acc.Name,
				Platform:       acc.Platform,
				GroupID:        displayGroupID,
				GroupName:      displayGroupName,
				CurrentInUse:   currentInUse,
				MaxCapacity:    int64(acc.Concurrency),
				WaitingInQueue: waiting,
			}
			if info.MaxCapacity > 0 {
				info.LoadPercentage = float64(info.CurrentInUse) / float64(info.MaxCapacity) * 100
			}
			provider[acc.ID] = info
		}

		// Platform aggregation.
		if acc.Platform != "" {
			if _, ok := platform[acc.Platform]; !ok {
				platform[acc.Platform] = &PlatformConcurrencyInfo{
					Platform: acc.Platform,
				}
			}
			p := platform[acc.Platform]
			p.MaxCapacity += int64(acc.Concurrency)
			p.CurrentInUse += currentInUse
			p.WaitingInQueue += waiting
		}

		// A provider contributes to each matching group.
		if matchedGroup != nil {
			grp := matchedGroup
			if _, ok := group[grp.ID]; !ok {
				group[grp.ID] = &GroupConcurrencyInfo{
					GroupID:   grp.ID,
					GroupName: grp.Name,
				}
			}
			g := group[grp.ID]
			if g.GroupName == "" && grp.Name != "" {
				g.GroupName = grp.Name
			}
			g.MaxCapacity += int64(acc.Concurrency)
			g.CurrentInUse += currentInUse
			g.WaitingInQueue += waiting
		} else {
			for _, grp := range acc.Groups {
				if grp == nil || grp.ID <= 0 {
					continue
				}
				if _, ok := group[grp.ID]; !ok {
					group[grp.ID] = &GroupConcurrencyInfo{
						GroupID:   grp.ID,
						GroupName: grp.Name,
					}
				}
				g := group[grp.ID]
				if g.GroupName == "" && grp.Name != "" {
					g.GroupName = grp.Name
				}
				g.MaxCapacity += int64(acc.Concurrency)
				g.CurrentInUse += currentInUse
				g.WaitingInQueue += waiting
			}
		}
	}

	for _, info := range platform {
		if info.MaxCapacity > 0 {
			info.LoadPercentage = float64(info.CurrentInUse) / float64(info.MaxCapacity) * 100
		}
	}
	for _, info := range group {
		if info.MaxCapacity > 0 {
			info.LoadPercentage = float64(info.CurrentInUse) / float64(info.MaxCapacity) * 100
		}
	}

	return platform, group, provider, &collectedAt, nil
}

// listAllActiveUsersForOps returns all active users with their concurrency settings.
func (s *OpsService) listAllActiveUsersForOps(ctx context.Context) ([]UserObservation, error) {
	if s == nil || s.userRepo == nil {
		return []UserObservation{}, nil
	}

	out := make([]UserObservation, 0, 128)
	page := 1
	for {
		users, pageInfo, err := s.userRepo.ListActivePage(ctx, pagination.PaginationParams{
			Page:     page,
			PageSize: opsProvidersPageSize,
		})
		if err != nil {
			return nil, err
		}
		if len(users) == 0 {
			break
		}

		out = append(out, users...)
		if pageInfo != nil && int64(len(out)) >= pageInfo.Total {
			break
		}
		if len(users) < opsProvidersPageSize {
			break
		}

		page++
		if page > 10_000 {
			log.Printf("[Ops] listAllActiveUsersForOps: aborting after too many pages")
			break
		}
	}

	return out, nil
}

// getUsersLoadMapBestEffort returns user load info for the given users.
func (s *OpsService) getUsersLoadMapBestEffort(ctx context.Context, users []UserObservation) map[int64]*UserLoadInfo {
	if s == nil || s.concurrencyService == nil {
		return map[int64]*UserLoadInfo{}
	}
	if len(users) == 0 {
		return map[int64]*UserLoadInfo{}
	}

	// De-duplicate IDs (and keep the max concurrency to avoid under-reporting).
	unique := make(map[int64]int, len(users))
	for _, u := range users {
		if u.ID <= 0 {
			continue
		}
		if prev, ok := unique[u.ID]; !ok || u.Concurrency > prev {
			unique[u.ID] = u.Concurrency
		}
	}

	batch := make([]UserWithConcurrency, 0, len(unique))
	for id, maxConc := range unique {
		batch = append(batch, UserWithConcurrency{
			ID:             id,
			MaxConcurrency: maxConc,
		})
	}

	out := make(map[int64]*UserLoadInfo, len(batch))
	for i := 0; i < len(batch); i += opsConcurrencyBatchChunkSize {
		end := min(i+opsConcurrencyBatchChunkSize, len(batch))
		part, err := s.concurrencyService.GetUsersLoadBatch(ctx, batch[i:end])
		if err != nil {
			// Missing load entries are displayed as zero when a batch query fails.
			log.Printf("[Ops] GetUsersLoadBatch failed: %v", err)
			continue
		}
		maps.Copy(out, part)
	}

	return out
}

// GetUserConcurrencyStats returns live concurrency for active users with running or waiting requests.
func (s *OpsService) GetUserConcurrencyStats(ctx context.Context) (map[int64]*UserConcurrencyInfo, *time.Time, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, nil, err
	}

	users, err := s.listAllActiveUsersForOps(ctx)
	if err != nil {
		return nil, nil, err
	}

	collectedAt := time.Now()
	loadMap := s.getUsersLoadMapBestEffort(ctx, users)

	result := make(map[int64]*UserConcurrencyInfo)

	for _, u := range users {
		if u.ID <= 0 {
			continue
		}

		load := loadMap[u.ID]
		currentInUse := int64(0)
		waiting := int64(0)
		if load != nil {
			currentInUse = int64(load.CurrentConcurrency)
			waiting = int64(load.WaitingCount)
		}

		// Skip users with no concurrency activity
		if currentInUse == 0 && waiting == 0 {
			continue
		}

		info := &UserConcurrencyInfo{
			UserID:         u.ID,
			UserEmail:      u.Email,
			Username:       u.Username,
			CurrentInUse:   currentInUse,
			MaxCapacity:    int64(u.Concurrency),
			WaitingInQueue: waiting,
		}
		if info.MaxCapacity > 0 {
			info.LoadPercentage = float64(info.CurrentInUse) / float64(info.MaxCapacity) * 100
		}
		result[u.ID] = info
	}

	return result, &collectedAt, nil
}

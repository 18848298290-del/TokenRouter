package provider

import (
	"context"
	"slices"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

type ManagementBatchStore interface {
	CreateProvider(context.Context, *CreateProviderInput) (*Record, error)
	GetProvider(context.Context, int64) (*Record, error)
	UpdateProvider(context.Context, int64, *UpdateProviderInput) (*Record, error)
	GetProvidersByIDs(context.Context, []int64) ([]*Record, error)
	DeleteProvider(context.Context, int64) error
}
type ManagementBatchFailure struct {
	ProviderID int64
	Error      string
}
type ManagementBatchWarning struct {
	ProviderID int64
	Warning    string
}
type ManagementBatchResult struct {
	Total, Success, Failed int
	Errors                 []ManagementBatchFailure
	Warnings               []ManagementBatchWarning
}
type ManagementDeleteResult struct {
	Total, Success, Failed int
	SuccessIDs, FailedIDs  []int64
	Errors                 []ManagementBatchFailure
}

// ManagementBatch runs bounded administrative batches and reports per-provider failures alongside successes.
type ManagementBatch struct {
	creation ManagementCreationOptions
	store    ManagementBatchStore
	managed  *ManagedRefreshService
}

func NewManagementBatch(store ManagementBatchStore, managed *ManagedRefreshService, creation ...ManagementCreationOptions) *ManagementBatch {
	options := ManagementCreationOptions{}
	if len(creation) > 0 {
		options = creation[0]
	}
	return &ManagementBatch{store: store, managed: managed, creation: options}
}

// DeleteNormalized accepts normalized positive IDs and deletes selected parent trees with up to five concurrent
// operations.
func (s *ManagementBatch) DeleteNormalized(ctx context.Context, providerIDs []int64) (*ManagementDeleteResult, error) {
	providers, err := s.store.GetProvidersByIDs(ctx, providerIDs)
	if err != nil {
		return nil, err
	}

	requestedIDs := make(map[int64]struct{}, len(providerIDs))
	for _, providerID := range providerIDs {
		requestedIDs[providerID] = struct{}{}
	}
	providersByID := make(map[int64]*Record, len(providers))
	for _, provider := range providers {
		if provider != nil {
			providersByID[provider.ID] = provider
		}
	}

	rootIDs := make([]int64, 0, len(providerIDs))
	dependentIDs := make(map[int64][]int64)
	failedIDs := make([]int64, 0)
	errorsByProvider := make([]ManagementBatchFailure, 0)
	for _, providerID := range providerIDs {
		provider := providersByID[providerID]
		if provider == nil {
			failedIDs = append(failedIDs, providerID)
			errorsByProvider = append(errorsByProvider, ManagementBatchFailure{
				ProviderID: providerID,
				Error:      "provider not found",
			})
			continue
		}

		rootID := providerID
		visited := map[int64]struct{}{providerID: {}}
		for {
			current := providersByID[rootID]
			if current == nil || current.ParentProviderID == nil {
				break
			}
			parentID := *current.ParentProviderID
			if _, selected := requestedIDs[parentID]; !selected {
				break
			}
			if _, exists := providersByID[parentID]; !exists {
				break
			}
			if _, cyclic := visited[parentID]; cyclic {
				rootID = providerID
				break
			}
			visited[parentID] = struct{}{}
			rootID = parentID
		}

		if rootID != providerID {
			dependentIDs[rootID] = append(dependentIDs[rootID], providerID)
			continue
		}
		rootIDs = append(rootIDs, providerID)
	}

	const maxConcurrency = 5
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrency)

	var mu sync.Mutex
	successIDs := make([]int64, 0, len(providerIDs))

	// Record each deletion failure and return nil to let the remaining tasks finish.
	for _, id := range rootIDs {
		providerID := id
		g.Go(func() error {
			err := s.store.DeleteProvider(gctx, providerID)

			mu.Lock()
			defer mu.Unlock()
			affectedIDs := append([]int64{providerID}, dependentIDs[providerID]...)
			if err != nil {
				for _, affectedID := range affectedIDs {
					failedIDs = append(failedIDs, affectedID)
					errorsByProvider = append(errorsByProvider, ManagementBatchFailure{
						ProviderID: affectedID,
						Error:      err.Error(),
					})
				}
				return nil
			}
			successIDs = append(successIDs, affectedIDs...)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	slices.Sort(successIDs)
	slices.Sort(failedIDs)
	sort.Slice(errorsByProvider, func(i, j int) bool {
		return errorsByProvider[i].ProviderID < errorsByProvider[j].ProviderID
	})

	return &ManagementDeleteResult{Total: len(providerIDs), Success: len(successIDs), Failed: len(failedIDs), SuccessIDs: successIDs, FailedIDs: failedIDs, Errors: errorsByProvider}, nil
}

func (s *ManagementBatch) Refresh(ctx context.Context, providerIDs []int64) (*ManagementBatchResult, error) {
	providers, err := s.store.GetProvidersByIDs(ctx, providerIDs)
	if err != nil {
		return nil, err
	}

	// Track loaded IDs so missing providers receive individual failures.
	foundIDs := make(map[int64]bool, len(providers))
	for _, acc := range providers {
		if acc != nil {
			foundIDs[acc.ID] = true
		}
	}

	const maxConcurrency = 10
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrency)

	var mu sync.Mutex
	var successCount, failedCount int
	var errors []ManagementBatchFailure
	var warnings []ManagementBatchWarning

	for _, id := range providerIDs {
		if !foundIDs[id] {
			failedCount++
			errors = append(errors, ManagementBatchFailure{
				ProviderID: id,
				Error:      "provider not found",
			})
		}
	}

	// Record failures in the result and return nil so one refresh cannot cancel the rest.
	for _, provider := range providers {
		acc := provider
		if acc == nil {
			continue
		}
		g.Go(func() error {
			_, warning, err := s.managed.Refresh(gctx, acc)
			mu.Lock()
			if err != nil {
				failedCount++
				errors = append(errors, ManagementBatchFailure{
					ProviderID: acc.ID,
					Error:      err.Error(),
				})
			} else {
				successCount++
				if warning != "" {
					warnings = append(warnings, ManagementBatchWarning{
						ProviderID: acc.ID,
						Warning:    warning,
					})
				}
			}
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &ManagementBatchResult{Total: len(providerIDs), Success: successCount, Failed: failedCount, Errors: errors, Warnings: warnings}, nil
}

func (s *ManagementBatch) ClearError(ctx context.Context, providerIDs []int64) (*ManagementBatchResult, error) {
	const maxConcurrency = 10
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrency)

	var mu sync.Mutex
	var successCount, failedCount int
	var errors []ManagementBatchFailure

	// Record failures in the result and return nil so one reset cannot cancel the rest.
	for _, id := range providerIDs {
		providerID := id
		g.Go(func() error {
			_, err := s.managed.ClearError(gctx, providerID)
			if err != nil {
				mu.Lock()
				failedCount++
				errors = append(errors, ManagementBatchFailure{
					ProviderID: providerID,
					Error:      err.Error(),
				})
				mu.Unlock()
				return nil
			}

			mu.Lock()
			successCount++
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &ManagementBatchResult{Total: len(providerIDs), Success: successCount, Failed: failedCount, Errors: errors}, nil
}

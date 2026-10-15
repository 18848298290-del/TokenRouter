package provider

import (
	"context"
	"slices"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

// management_batch.go runs batches for provider deletion, credential refresh,
// and error clearing while retaining individual outcomes.
// Administrative batches retain per-provider failures while other work
// continues. Refresh and error clearing each use a bounded worker group, but each
// worker records its outcome and returns success to the group so one provider's
// failure does not cancel unrelated tasks. Cancellation still reaches the
// service calls through their shared context.
//
// Deletion first resolves which selected providers belong to another selected
// parent. Each selected tree is deleted through its highest available selected
// ancestor, allowing the store's cascade to remove shadows before their owner.
// Deletion outcomes are expanded back to requested descendants and sorted for
// display. Refresh records missing IDs first, then worker outcomes as each
// acquires the result mutex; error clearing collects outcomes under its own lock.

// ManagementBatchStore supplies provider creation, lookup, editing, and cascade
// deletion for batches.
type ManagementBatchStore interface {
	// CreateProvider validates and saves one nonnil provider configuration. It
	// can normalize the input's maps, and a successful result supplies the created
	// identity for ordered batch reporting.
	CreateProvider(context.Context, *CreateProviderInput) (*Record, error)
	// GetProvider reads a provider before credential-field batch validation.
	// Prevalidation checks both the returned record and error, so a nil record
	// also fails the batch before updates begin.
	GetProvider(context.Context, int64) (*Record, error)
	// UpdateProvider applies a selected edit or internal maintenance patch to one
	// provider. Callers supply nonnil input; raw nonnil patches require a
	// configuration-aware writer that merges selected keys against current storage.
	UpdateProvider(context.Context, int64, *UpdateProviderInput) (*Record, error)
	// GetProvidersByIDs reads the selected records before refresh or cascade
	// deletion.
	GetProvidersByIDs(context.Context, []int64) ([]*Record, error)
	// DeleteProvider removes a provider after deleting its credential shadows.
	// Child lookup or deletion errors stop the cascade before the parent call,
	// so a failed root operation can leave a partially deleted tree. Deletes use
	// separate storage calls whose transaction completion belongs to the
	// underlying store or its outer owner. Batch callers apply that root outcome
	// to every selected descendant assigned to it.
	DeleteProvider(context.Context, int64) error
}

// ManagementBatchFailure pairs one provider identifier with its failed
// operation message.
type ManagementBatchFailure struct {
	ProviderID int64  // failed local provider identifier
	Error      string // failed operation message text
}

// ManagementBatchWarning carries a successful provider operation's additional
// warning message.
type ManagementBatchWarning struct {
	ProviderID int64  // successful local provider identifier
	Warning    string // successful operation warning text
}

// ManagementBatchResult summarizes refresh or recovery outcomes after
// concurrent workers finish.
type ManagementBatchResult struct {
	// Total records requested ID occurrences, while Success and Failed count
	// service outcomes and missing-ID failures. Errors and Warnings are
	// accumulated under the workers' result mutex.
	Total, // requested provider occurrence count
	Success, // successful processed record count
	Failed int // failed provider outcome count
	// Errors begins with missing-provider failures for Refresh, followed by worker
	// failures in mutex acquisition order. ClearError appends each failed call's
	// outcome under that call's result lock.
	Errors []ManagementBatchFailure // individual provider failure messages
	// Warnings contains nonempty messages from successful Refresh calls, appended
	// under that call's result mutex. ClearError leaves this slice nil.
	Warnings []ManagementBatchWarning // successful provider warning messages
}

// ManagementDeleteResult lists requested provider outcomes after selected-tree
// deletion completes.
type ManagementDeleteResult struct {
	// Total counts selected IDs. Success and Failed count their expanded root
	// outcomes, and SuccessIDs, FailedIDs, and Errors are sorted after all root
	// workers finish.
	Total, // requested provider identifier count
	Success, // successfully deleted selection count
	Failed int // failed selected provider count
	SuccessIDs, // ascending successful provider identifiers
	FailedIDs []int64 // ascending failed provider identifiers
	Errors []ManagementBatchFailure // provider-ID ordered failure messages
}

// ManagementBatch coordinates bounded provider administration and
// partial-success reporting.
type ManagementBatch struct {
	creation ManagementCreationOptions // retained batch creation callbacks
	store    ManagementBatchStore      // provider administrative operation dependency
	// managed supplies coordinated refresh and error recovery. Refresh and
	// ClearError require this dependency; creation, deletion, and credential
	// patches use storage directly.
	managed *ManagedRefreshService
}

// NewManagementBatch binds storage, coordinated refresh, and optional creation
// callbacks. The first creation option is retained when supplied; refresh and
// error clearing require managed, while creation and deletion use the store.
func NewManagementBatch(store ManagementBatchStore, managed *ManagedRefreshService, creation ...ManagementCreationOptions) *ManagementBatch {
	options := ManagementCreationOptions{}
	if len(creation) > 0 {
		options = creation[0]
	}
	return &ManagementBatch{store: store, managed: managed, creation: options}
}

// DeleteNormalized deletes selected provider trees with up to five concurrent
// root operations. Callers supply positive, deduplicated IDs. Missing records
// receive individual failures, and selected descendants are assigned to their
// highest selected ancestor present in the lookup result.
//
// Each root deletion outcome is applied to its selected descendants. A cycle
// stops ancestor traversal at the original selected ID; the caller's data still
// determines the resulting roots. Workers record failures rather than
// cancelling other trees, then successful IDs, failed IDs, and failure messages
// are sorted by provider ID.
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

		// Resolve selected ancestors before launching deletes so a selected
		// shadow shares its selected parent's result rather than being deleted
		// independently in parallel.
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
			// Stop traversal when the selected relation cycles. Return to this
			// request's provider ID so malformed ancestry cannot keep root
			// resolution looping indefinitely.
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

	// Bound concurrent parent-tree deletes while each store call cascades shadows.
	const maxConcurrency = 5
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrency)

	var mu sync.Mutex
	successIDs := make([]int64, 0, len(providerIDs))

	// A failed tree deletion is an individual outcome. Returning nil keeps the
	// worker group processing other roots while the mutex protects expanded
	// descendant results.
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

// Refresh loads selected providers and runs coordinated refresh with up to ten
// concurrent workers. Requested IDs absent from the lookup result receive
// individual failures. Loaded records are processed as returned by storage, so
// total requested IDs and processed record counts need not match for duplicate
// inputs.
//
// Workers append errors or warnings under a mutex and return nil to keep other
// refreshes running. Worker entries reflect mutex acquisition order after the
// missing-ID failures collected before launch. The shared context reaches each
// service call, and the result returns after every admitted worker finishes.
func (s *ManagementBatch) Refresh(ctx context.Context, providerIDs []int64) (*ManagementBatchResult, error) {
	providers, err := s.store.GetProvidersByIDs(ctx, providerIDs)
	if err != nil {
		return nil, err
	}

	foundIDs := make(map[int64]bool, len(providers))
	for _, acc := range providers {
		if acc != nil {
			foundIDs[acc.ID] = true
		}
	}

	// Bound concurrent refresh calls for the loaded provider records.
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

	// Refresh errors belong to their provider result. Returning nil avoids
	// group cancellation on one failure while the mutex protects counts,
	// errors, and successful warnings.
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

// ClearError invokes coordinated recovery for each supplied provider ID with up
// to ten concurrent workers. Every occurrence is submitted, so repeated IDs can
// produce repeated operations. Service failures increment the partial result
// rather than cancelling the rest of the batch.
//
// Workers update counts and failure entries under one mutex. The group waits
// for all admitted calls before returning their aggregate, and error entries
// reflect mutex acquisition order. Cancellation reaches each recovery operation
// through the shared group context.
func (s *ManagementBatch) ClearError(ctx context.Context, providerIDs []int64) (*ManagementBatchResult, error) {
	// Bound concurrent recovery calls while retaining every supplied ID occurrence.
	const maxConcurrency = 10
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrency)

	var mu sync.Mutex
	var successCount, failedCount int
	var errors []ManagementBatchFailure

	// Recovery errors belong to their provider result. Returning nil lets other
	// selected IDs finish, while the result mutex protects counts and appended
	// failure messages.
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

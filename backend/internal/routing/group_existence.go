package routing

import (
	"context"
	"errors"
	"fmt"
)

// group_existence.go validates group references through batched existence
// queries or individual reads.
// Group references must resolve before another resource saves its associations.
// Repositories that implement batched existence checks can validate all
// requested IDs through one lookup call, while the common reader supplies a
// compatible per-ID path.

// GroupExistenceLookup reads groups when validating references from other
// resources.
type GroupExistenceLookup interface {
	// GetByID loads the group identified by the requested ID.
	GetByID(context.Context, int64) (*Group, error)
}

// GroupExistenceBatchReader supplies optional batched presence checks for group
// references.
type GroupExistenceBatchReader interface {
	// ExistsByIDs reports saved presence indexed by the requested group IDs.
	// ValidateGroupIDs treats missing or false entries as nonexistent and
	// propagates a returned error before inspecting the map.
	ExistsByIDs(context.Context, []int64) (map[int64]bool, error)
}

// ValidateGroupIDs checks that each supplied group reference exists before its
// caller writes associations. An empty list succeeds even with a nil
// repository; otherwise a missing reader returns an error. The optional batch
// path rejects nonpositive IDs and missing map entries. The per-ID path accepts
// successful GetByID calls and relies on that reader to reject missing or
// unusable IDs.
func ValidateGroupIDs(ctx context.Context, repo GroupExistenceLookup, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	if repo == nil {
		return errors.New("group repository not configured")
	}

	if batchReader, ok := repo.(GroupExistenceBatchReader); ok {
		existsByID, err := batchReader.ExistsByIDs(ctx, groupIDs)
		if err != nil {
			return fmt.Errorf("check groups exists: %w", err)
		}
		for _, groupID := range groupIDs {
			if groupID <= 0 || !existsByID[groupID] {
				return fmt.Errorf("get group: %w", ErrGroupNotFound)
			}
		}
		return nil
	}

	// Repositories lacking the batch extension resolve each reference with their
	// normal lookup, preserving that reader's error for a failed group.
	for _, groupID := range groupIDs {
		if _, err := repo.GetByID(ctx, groupID); err != nil {
			return fmt.Errorf("get group: %w", err)
		}
	}
	return nil
}

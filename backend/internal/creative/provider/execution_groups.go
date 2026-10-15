package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// execution_groups.go defines the group lookup required during creative
// execution selection.
// Execution reads the group again before selecting an upstream provider. The
// group supplies current protocol permissions and model restrictions, so a
// queued task can be refused when its saved group has become unavailable or
// its current policy blocks the model.

// ExecutionGroups reads group protocol settings needed for execution.
type ExecutionGroups interface {
	// GetByIDLite reads current group policy for the saved task group identifier.
	// Lookup failures or absent groups prevent creative provider selection
	// instead of switching the task to a different group.
	GetByIDLite(context.Context, int64) (*routing.Group, error)
}

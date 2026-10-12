package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// ExecutionGroups reads group protocol settings needed for execution.
type ExecutionGroups interface {
	GetByIDLite(context.Context, int64) (*routing.Group, error)
}

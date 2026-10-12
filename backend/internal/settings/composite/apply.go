package composite

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Application applies a committed snapshot to one module.
type Application struct {
	Module string
	Apply  func(context.Context, *Snapshot) error
}

// ApplicationsForUpdate creates update-local readback state shared by the ordered application steps. A nil
// snapshot skips module application.
func ApplicationsForUpdate(load func(context.Context) (*Snapshot, error), steps []Application) []settings.PreparedChange {
	var snapshot *Snapshot
	changes := []settings.PreparedChange{{Module: "system", Apply: func(ctx context.Context) error { var err error; snapshot, err = load(ctx); return err }}}
	for _, step := range steps {
		changes = append(changes, settings.PreparedChange{Module: step.Module, Apply: func(ctx context.Context) error {
			if snapshot == nil {
				return nil
			}
			return step.Apply(ctx, snapshot)
		}})
	}
	return changes
}

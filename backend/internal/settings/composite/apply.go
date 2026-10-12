package composite

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Post-commit modules need one readback value for the settings just saved. The
// first generated callback loads that Snapshot, and each later callback reads the
// same update-local pointer in the supplied module order. This shared state is
// intended for sequential callback execution by UpdateSession.Commit.
//
// A nil readback value makes module callbacks skip, including the usual read
// failure that returns nil with an error. A loader returning a value with an error
// retains that value for later callbacks, because Commit continues after errors.
// Each Application supplies its own effect and failure; these generated callbacks
// coordinate readback availability and leave persisted-data error reporting to
// the update session.

// Application names one module and applies its settings from a shared readback Snapshot.
type Application struct {
	Module string // module reported for application failures
	// Apply receives the update's shared readback pointer after persistence.
	// Callbacks run in sequence and return module errors; they keep the Snapshot
	// stable for later modules unless their registered application rule permits mutation.
	Apply func(context.Context, *Snapshot) error
}

// ApplicationsForUpdate returns update-local callbacks with a shared readback
// Snapshot pointer. The first callback runs load and records both its returned
// value and error; later callbacks follow the supplied step order when invoked
// sequentially by UpdateSession.Commit.
//
// A nil Snapshot skips module application. A nonnil value returned with an
// error remains available because Commit continues through its callbacks, and
// that error is still included in the session's application report. Callers keep
// this generated list in its sequence and execute it within one update; parallel
// application would require synchronization for the captured pointer.
func ApplicationsForUpdate(load func(context.Context) (*Snapshot, error), steps []Application) []settings.PreparedChange {
	// Retain one update-local readback value across callbacks so later modules
	// see the same loaded pointer after the first callback has completed its attempt.
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

package scheduler

import "context"

// RPMCache reads and updates provider request-per-minute counters.
// Anthropic OAuth and SetupToken admission uses these counts.
type RPMCache interface {
	// IncrementRPM increments and returns the current minute's count.
	// The Redis implementation uses server time so instances share a minute window.
	IncrementRPM(ctx context.Context, providerID int64) (count int, err error)

	// GetRPM returns the current minute's count.
	GetRPM(ctx context.Context, providerID int64) (count int, err error)

	// GetRPMBatch returns counts for multiple providers in the current minute.
	GetRPMBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error)
}

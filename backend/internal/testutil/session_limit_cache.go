package testutil

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

var (
	_ scheduler.SessionLimitCache = StubSessionLimitCache{}
	_ billing.WindowCostCache     = StubSessionLimitCache{}
)

// StubSessionLimitCache returns empty session counts and window costs.
type StubSessionLimitCache struct{}

// RegisterSession accepts the session without storing it.
func (c StubSessionLimitCache) RegisterSession(_ context.Context, _ int64, _ string, _ int, _ time.Duration) (bool, error) {
	return true, nil
}

// RefreshSession reports success without storing session state.
func (c StubSessionLimitCache) RefreshSession(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}

// UnregisterSession reports success without storing session state.
func (c StubSessionLimitCache) UnregisterSession(_ context.Context, _ int64, _ string) error {
	return nil
}

// GetActiveSessionCount returns zero.
func (c StubSessionLimitCache) GetActiveSessionCount(_ context.Context, _ int64) (int, error) {
	return 0, nil
}

// GetActiveSessionCountBatch returns a nil count map.
func (c StubSessionLimitCache) GetActiveSessionCountBatch(_ context.Context, _ []int64, _ map[int64]time.Duration) (map[int64]int, error) {
	return nil, nil
}

// IsSessionActive returns false.
func (c StubSessionLimitCache) IsSessionActive(_ context.Context, _ int64, _ string) (bool, error) {
	return false, nil
}

// GetWindowCost reports a cache miss.
func (c StubSessionLimitCache) GetWindowCost(_ context.Context, _ int64) (float64, bool, error) {
	return 0, false, nil
}

// SetWindowCost reports success without storing the cost.
func (c StubSessionLimitCache) SetWindowCost(_ context.Context, _ int64, _ float64) error {
	return nil
}

// GetWindowCostBatch returns a nil cost map.
func (c StubSessionLimitCache) GetWindowCostBatch(_ context.Context, _ []int64) (map[int64]float64, error) {
	return nil, nil
}

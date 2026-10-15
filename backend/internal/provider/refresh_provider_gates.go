package provider

import "sync"

// refresh_provider_gates.go shares per-platform refresh rate and concurrency
// gates across maintenance cycles.
// Periodic scans and administrative reconciliation use the same per-platform
// admission gates. The registry lazily publishes one rate gate and one capacity
// gate for each exact platform string, so the first requested limits remain in
// effect for later cycles.
//
// Both maps are initialized and accessed under one mutex, preventing concurrent
// entry points from publishing independent gates for the same key. Rate and Pool
// return shared gate pointers after unlocking; each gate coordinates its own
// reservations or capacity. Failure streaks are kept in newly constructed
// RefreshProviderState values, allowing cycles to share admission while counting
// their failures independently.

// RefreshProviderGates retains shared platform rate and capacity gates across
// refresh entry points.
type RefreshProviderGates struct {
	// mu protects rates and pools during lazy publication. The zero value is
	// usable, and a platform's first published gate keeps its initial limits
	// while later callers borrow that same pointer.
	mu    sync.Mutex                         // platform map synchronization
	rates map[string]*RefreshRateGate        // shared platform rate schedules
	pools map[string]*RefreshConcurrencyGate // shared platform capacity pools
}

// Rate returns the shared rate gate for the exact platform string, creating it
// with qps on first use. The zero-value registry is usable, and callers borrow
// its published pointer across scans and administrative reconciliation.
//
// The mutex covers initialization, lookup and publication, so the first caller
// that publishes the platform selects its schedule. Later qps values leave that
// gate in place. Callers establish the intended rate before first publication
// because this registry supplies no reconfiguration operation.
func (g *RefreshProviderGates) Rate(platform string, qps int) *RefreshRateGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rates == nil {
		g.rates = make(map[string]*RefreshRateGate)
	}
	if existing := g.rates[platform]; existing != nil {
		return existing
	}
	// Publish the first rate schedule while the registry is locked so concurrent
	// scans and reconciliation cannot obtain separate schedules for this
	// platform.
	value := NewRefreshRateGate(qps)
	g.rates[platform] = value
	return value
}

// Pool returns the shared capacity gate for the exact platform string, creating
// it with concurrency on first use. Scans and reconciliation borrow the same
// pointer so their occupied attempts consume that platform's shared capacity.
//
// Initialization, lookup and publication stay under the registry mutex, making
// the first published limit apply to later callers. Subsequent limits leave the
// existing pool in place. Callers select the desired capacity before the first
// publication; resizing would require a separate resource-lifetime decision
// because existing borrowers still hold that pool.
func (g *RefreshProviderGates) Pool(platform string, concurrency int) *RefreshConcurrencyGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pools == nil {
		g.pools = make(map[string]*RefreshConcurrencyGate)
	}
	if existing := g.pools[platform]; existing != nil {
		return existing
	}
	// Publish the first capacity pool while the registry is locked so both refresh
	// entry points account for occupied slots through the same channel.
	value := NewRefreshConcurrencyGate(concurrency)
	g.pools[platform] = value
	return value
}

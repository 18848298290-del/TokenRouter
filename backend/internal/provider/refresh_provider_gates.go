package provider

import "sync"

// RefreshProviderGates shares per-platform admission gates across periodic and administrative work. The zero
// value is usable.
type RefreshProviderGates struct {
	mu    sync.Mutex
	rates map[string]*RefreshRateGate
	pools map[string]*RefreshConcurrencyGate
}

func (g *RefreshProviderGates) Rate(platform string, qps int) *RefreshRateGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rates == nil {
		g.rates = make(map[string]*RefreshRateGate)
	}
	if existing := g.rates[platform]; existing != nil {
		return existing
	}
	value := NewRefreshRateGate(qps)
	g.rates[platform] = value
	return value
}

func (g *RefreshProviderGates) Pool(platform string, concurrency int) *RefreshConcurrencyGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pools == nil {
		g.pools = make(map[string]*RefreshConcurrencyGate)
	}
	if existing := g.pools[platform]; existing != nil {
		return existing
	}
	value := NewRefreshConcurrencyGate(concurrency)
	g.pools[platform] = value
	return value
}

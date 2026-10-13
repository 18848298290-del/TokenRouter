package provider

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Usage cache entries retain platform results while their consumers decide
// identity and freshness. API responses and local totals contain scalar data
// and are copied by value; Antigravity and Qoder display results use
// CloneUsageInfo so later countdown or presentation edits have independent
// mutable storage.

// OAuthAPIUsageCache retains a Claude response or error with credential
// identity and age.
type OAuthAPIUsageCache struct {
	Identity string               `json:"-"` // queried credential identity digest
	Response *ClaudeUsageResponse // optional Claude quota response
	// Err retains the same error object when entries are copied. Consumers
	// treat that diagnostic as immutable while cached or shared across query
	// results.
	Err       error
	Timestamp time.Time // API query observation time
}

// OAuthWindowStatsCache retains scalar local usage totals with their
// observation time.
type OAuthWindowStatsCache struct {
	Stats     *WindowStats // optional local window totals
	Timestamp time.Time    // local totals observation time
}

// OAuthAntigravityUsageCache retains one identity-associated Antigravity
// display observation and age.
type OAuthAntigravityUsageCache struct {
	Identity  string     `json:"-"` // queried credential identity digest
	UsageInfo *UsageInfo // copied Antigravity display values
	Timestamp time.Time  // quota query observation time
}

// OAuthQoderUsageCache retains one identity-associated Qoder display
// observation and age.
type OAuthQoderUsageCache struct {
	Identity  string     `json:"-"` // queried credential identity digest
	UsageInfo *UsageInfo // copied Qoder display values
	Timestamp time.Time  // quota query observation time
}

// OAuthUsageCache stores copied usage observations and coalesces platform
// queries by supplied keys.
type OAuthUsageCache struct {
	// api, windows, antigravity, qoder, openAIProbe, and grokProbe are independent
	// synchronized stores. Their readers choose identity and age checks; platform
	// display copies are prepared before Store and after Load.
	// api: Claude response entries; windows: local total entries; antigravity:
	// Antigravity display entries; qoder: Qoder display entries; openAIProbe:
	// OpenAI probe timestamps; grokProbe: Grok probe timestamps.
	api, windows, antigravity, qoder, openAIProbe, grokProbe sync.Map
	// apiFlight, antigravityFlight, and qoderFlight coalesce work only within
	// their platform group and supplied key. Their library result is shared, so
	// the platform query consumer validates association and copies mutable
	// display data.
	// apiFlight: Claude query sharing; antigravityFlight: Antigravity query
	// sharing;
	// qoderFlight: Qoder query sharing.
	apiFlight, antigravityFlight, qoderFlight singleflight.Group
}

// NewOAuthUsageCache creates empty maps and query groups ready for usage
// consumers.
func NewOAuthUsageCache() *OAuthUsageCache { return &OAuthUsageCache{} }

// LoadAPI returns a copied API entry and scalar Claude response. Missing,
// wrong-type, and typed-nil entries are misses; callers check identity and age
// themselves. Err retains its immutable shared error object.
func (c *OAuthUsageCache) LoadAPI(key int64) (*OAuthAPIUsageCache, bool) {
	raw, ok := c.api.Load(key)
	if !ok {
		return nil, false
	}
	v, ok := raw.(*OAuthAPIUsageCache)
	if !ok || v == nil {
		return nil, false
	}
	out := *v
	out.Response = clonePointer(v.Response)
	return &out, true
}

// StoreAPI copies entry metadata and the scalar response before publication.
// Nil becomes a typed-nil miss; keep input stable during copying, and treat
// the retained shared Err object as immutable.
func (c *OAuthUsageCache) StoreAPI(key int64, v *OAuthAPIUsageCache) {
	if v == nil {
		c.api.Store(key, (*OAuthAPIUsageCache)(nil))
		return
	}
	out := *v
	out.Response = clonePointer(v.Response)
	c.api.Store(key, &out)
}

// LoadWindow returns copied local totals and their observation timestamp.
// Missing, wrong-type, and typed-nil entries are misses; LocalUsageStatistics
// applies its own age check before attaching those values to a display window.
func (c *OAuthUsageCache) LoadWindow(key int64) (*OAuthWindowStatsCache, bool) {
	raw, ok := c.windows.Load(key)
	if !ok {
		return nil, false
	}
	v, ok := raw.(*OAuthWindowStatsCache)
	if !ok || v == nil {
		return nil, false
	}
	out := *v
	out.Stats = clonePointer(v.Stats)
	return &out, true
}

// StoreWindow copies the supplied scalar local totals entry before
// publication. Nil is stored as a typed-nil miss; keep the input stable while
// it is copied, then callers may edit their own result values.
func (c *OAuthUsageCache) StoreWindow(key int64, v *OAuthWindowStatsCache) {
	if v == nil {
		c.windows.Store(key, (*OAuthWindowStatsCache)(nil))
		return
	}
	out := *v
	out.Stats = clonePointer(v.Stats)
	c.windows.Store(key, &out)
}

// LoadAntigravity returns a copy of the stored nested display values entry.
// Missing, wrong-type, and typed-nil entries are misses; callers separately
// validate identity and freshness before using these independently mutable
// result values.
func (c *OAuthUsageCache) LoadAntigravity(key int64) (*OAuthAntigravityUsageCache, bool) {
	raw, ok := c.antigravity.Load(key)
	if !ok {
		return nil, false
	}
	v, ok := raw.(*OAuthAntigravityUsageCache)
	if !ok || v == nil {
		return nil, false
	}
	out := *v
	out.UsageInfo = CloneUsageInfo(v.UsageInfo)
	return &out, true
}

// StoreAntigravity copies the supplied nested display values entry before
// publication. Nil is stored as a typed-nil miss; keep the input stable while
// it is copied, then callers may edit their own result values.
func (c *OAuthUsageCache) StoreAntigravity(key int64, v *OAuthAntigravityUsageCache) {
	if v == nil {
		c.antigravity.Store(key, (*OAuthAntigravityUsageCache)(nil))
		return
	}
	out := *v
	out.UsageInfo = CloneUsageInfo(v.UsageInfo)
	c.antigravity.Store(key, &out)
}

// LoadQoder returns a copy of the stored nested display values entry. Missing,
// wrong-type, and typed-nil entries are misses; callers separately validate
// identity and freshness before using these independently mutable result
// values.
func (c *OAuthUsageCache) LoadQoder(key int64) (*OAuthQoderUsageCache, bool) {
	raw, ok := c.qoder.Load(key)
	if !ok {
		return nil, false
	}
	v, ok := raw.(*OAuthQoderUsageCache)
	if !ok || v == nil {
		return nil, false
	}
	out := *v
	out.UsageInfo = CloneUsageInfo(v.UsageInfo)
	return &out, true
}

// StoreQoder copies the supplied nested display values entry before
// publication. Nil is stored as a typed-nil miss; keep the input stable while
// it is copied, then callers may edit their own result values.
func (c *OAuthUsageCache) StoreQoder(key int64, v *OAuthQoderUsageCache) {
	if v == nil {
		c.qoder.Store(key, (*OAuthQoderUsageCache)(nil))
		return
	}
	out := *v
	out.UsageInfo = CloneUsageInfo(v.UsageInfo)
	c.qoder.Store(key, &out)
}

// DoAPI delegates same-key query sharing to the API singleflight group. It
// returns the shared value, error, and duplicate indicator; mutable results
// remain borrowed until each consumer copies them.
func (c *OAuthUsageCache) DoAPI(key string, fn func() (any, error)) (any, error, bool) {
	return c.apiFlight.Do(key, fn)
}

// DoAntigravity coalesces same-key queries through the Antigravity
// singleflight group. Returned mutable values are shared across callers, so
// display consumers validate identity and copy results before editing them.
func (c *OAuthUsageCache) DoAntigravity(key string, fn func() (any, error)) (any, error, bool) {
	return c.antigravityFlight.Do(key, fn)
}

// DoQoder coalesces same-key queries through the Qoder singleflight group. Its
// supplied key controls association, and mutable result ownership remains with
// the caller until each waiter makes its copy.
func (c *OAuthUsageCache) DoQoder(key string, fn func() (any, error)) (any, error, bool) {
	return c.qoderFlight.Do(key, fn)
}

// LoadOpenAIProbe reads the last stored OpenAI probe observation by row ID.
// Missing entries return false; the any result is type-checked and aged by the
// caller rather than here.
func (c *OAuthUsageCache) LoadOpenAIProbe(id int64) (any, bool) { return c.openAIProbe.Load(id) }

// StoreOpenAIProbe stores a timestamp for the supplied provider row ID. Probe
// consumers enforce their own retry interval; this map operation itself
// performs no freshness check or expiration.
func (c *OAuthUsageCache) StoreOpenAIProbe(id int64, at time.Time) { c.openAIProbe.Store(id, at) }

// LoadGrokProbe reads the stored Grok probe timestamp by row ID. Missing
// entries return false; its any result requires caller type checking and
// platform retry-age assessment.
func (c *OAuthUsageCache) LoadGrokProbe(id int64) (any, bool) { return c.grokProbe.Load(id) }

// StoreGrokProbe stores the supplied row probe timestamp for later retry
// decisions. Its consumers assess age themselves, and the map retains that
// observation until it is replaced.
func (c *OAuthUsageCache) StoreGrokProbe(id int64, at time.Time) { c.grokProbe.Store(id, at) }

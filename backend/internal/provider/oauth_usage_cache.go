package provider

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type OAuthAPIUsageCache struct {
	Identity  string `json:"-"`
	Response  *ClaudeUsageResponse
	Err       error
	Timestamp time.Time
}
type OAuthWindowStatsCache struct {
	Stats     *WindowStats
	Timestamp time.Time
}
type OAuthAntigravityUsageCache struct {
	Identity  string `json:"-"`
	UsageInfo *UsageInfo
	Timestamp time.Time
}
type OAuthQoderUsageCache struct {
	Identity  string `json:"-"`
	UsageInfo *UsageInfo
	Timestamp time.Time
}

// OAuthUsageCache stores platform usage and shared queries, copying cached display values on reads and writes.
type OAuthUsageCache struct {
	api, windows, antigravity, qoder, openAIProbe, grokProbe sync.Map
	apiFlight, antigravityFlight, qoderFlight                singleflight.Group
}

func NewOAuthUsageCache() *OAuthUsageCache { return &OAuthUsageCache{} }

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

func (c *OAuthUsageCache) StoreAPI(key int64, v *OAuthAPIUsageCache) {
	if v == nil {
		c.api.Store(key, (*OAuthAPIUsageCache)(nil))
		return
	}
	out := *v
	out.Response = clonePointer(v.Response)
	c.api.Store(key, &out)
}

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

func (c *OAuthUsageCache) StoreWindow(key int64, v *OAuthWindowStatsCache) {
	if v == nil {
		c.windows.Store(key, (*OAuthWindowStatsCache)(nil))
		return
	}
	out := *v
	out.Stats = clonePointer(v.Stats)
	c.windows.Store(key, &out)
}

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

func (c *OAuthUsageCache) StoreAntigravity(key int64, v *OAuthAntigravityUsageCache) {
	if v == nil {
		c.antigravity.Store(key, (*OAuthAntigravityUsageCache)(nil))
		return
	}
	out := *v
	out.UsageInfo = CloneUsageInfo(v.UsageInfo)
	c.antigravity.Store(key, &out)
}

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

func (c *OAuthUsageCache) StoreQoder(key int64, v *OAuthQoderUsageCache) {
	if v == nil {
		c.qoder.Store(key, (*OAuthQoderUsageCache)(nil))
		return
	}
	out := *v
	out.UsageInfo = CloneUsageInfo(v.UsageInfo)
	c.qoder.Store(key, &out)
}

func (c *OAuthUsageCache) DoAPI(key string, fn func() (any, error)) (any, error, bool) {
	return c.apiFlight.Do(key, fn)
}

func (c *OAuthUsageCache) DoAntigravity(key string, fn func() (any, error)) (any, error, bool) {
	return c.antigravityFlight.Do(key, fn)
}

func (c *OAuthUsageCache) DoQoder(key string, fn func() (any, error)) (any, error, bool) {
	return c.qoderFlight.Do(key, fn)
}
func (c *OAuthUsageCache) LoadOpenAIProbe(id int64) (any, bool)    { return c.openAIProbe.Load(id) }
func (c *OAuthUsageCache) StoreOpenAIProbe(id int64, at time.Time) { c.openAIProbe.Store(id, at) }
func (c *OAuthUsageCache) LoadGrokProbe(id int64) (any, bool)      { return c.grokProbe.Load(id) }
func (c *OAuthUsageCache) StoreGrokProbe(id int64, at time.Time)   { c.grokProbe.Store(id, at) }

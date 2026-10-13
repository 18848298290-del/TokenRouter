package middleware

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// Panel buckets depend on cached settings, authenticated identity, and
// public-address eligibility. In-memory settings and counter fixtures let the
// cases vary those inputs independently while checking bucket names,
// administrator exemptions, failure availability, and the panel rejection
// envelope.

// panelRateLimitStubRepo stores settings strings behind a mutex for panel cache
// fixtures.
type panelRateLimitStubRepo struct {
	// mu protects values for reads and writes after fixture setup. Reader methods
	// copy maps before returning them so callers can change those results
	// independently.
	mu     sync.Mutex        // settings map mutex
	values map[string]string // stored raw settings strings
}

// fakePanelAllower counts exact panel buckets under a mutex and never expires
// them.
type fakePanelAllower struct {
	// mu protects counts during concurrent Allow calls. Tests configure err before
	// calling the fixture; a configured failure leaves counts unchanged, and
	// successful calls include rejected requests.
	mu     sync.Mutex       // counter observation mutex
	counts map[string]int64 // nonexpiring bucket request totals
	// err is read before counting while the mutex is held. Tests set it during
	// fixture setup to inject a counter failure.
	err error
}

// panelTestIdentity supplies the authenticated subject and role injected before
// panel middleware.
type panelTestIdentity struct {
	userID int64  // authenticated subject user ID
	role   string // authenticated user role name
}

// Get returns a fresh Setting with the requested raw value. It reads under the
// fixture mutex and returns ErrSettingNotFound for absent keys, including an
// uninitialized map.
func (r *panelRateLimitStubRepo) Get(_ context.Context, key string) (*settingscore.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, settingscore.ErrSettingNotFound
	}
	return &settingscore.Setting{Key: key, Value: value}, nil
}

// GetValue reads one raw settings string under the fixture mutex. An absent key
// returns ErrSettingNotFound so the panel settings reader can select its
// ordinary default policy.
func (r *panelRateLimitStubRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return value, nil
}

// Set records a raw settings value under the fixture mutex, allocating the map
// when needed. It accepts each supplied key and value directly for subsequent
// cached settings reads.
func (r *panelRateLimitStubRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	// Serialize this settings replacement with readers so a cache reload observes
	// a complete map entry from this test operation.
	r.values[key] = value
	return nil
}

// GetMultiple returns a newly allocated map of requested keys present in the
// fixture. Missing keys are omitted, and copying under the mutex keeps readers
// independent of later settings writes.
func (r *panelRateLimitStubRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// SetMultiple merges supplied raw settings into the fixture under its mutex,
// allocating storage when needed. Existing unrelated keys remain, and callers
// keep the input map unchanged while it is read.
func (r *panelRateLimitStubRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	// Merge the provided batch while excluding readers. A cached settings reload
	// cannot interleave its map copy with this batch's individual replacements.
	maps.Copy(r.values, settings)
	return nil
}

// GetAll returns an independent map of the fixture's current settings. The
// mutex holds the source stable during copying, so callers can edit the
// returned strings and entries freely.
func (r *panelRateLimitStubRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	maps.Copy(out, r.values)
	return out, nil
}

// Delete removes a fixture setting under the shared map mutex.
func (r *panelRateLimitStubRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Remove the setting while excluding cache readers. Later reads then observe
	// its absence and can use their own default-setting path.
	delete(r.values, key)
	return nil
}

// Allow increments an exact panel bucket under the fixture mutex and returns
// its admission result. A configured error bypasses counting; rejected calls
// receive the supplied window as their delay because the fixture retains counts
// without expiry.
func (f *fakePanelAllower) Allow(_ context.Context, key string, limit int, window time.Duration) (AllowResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return AllowResult{}, f.err
	}
	if f.counts == nil {
		f.counts = make(map[string]int64)
	}
	// Count the current request before comparing its budget, including denials.
	// Keep the increment and returned observation under one mutex for concurrent
	// test calls.
	f.counts[key]++
	count := f.counts[key]
	result := AllowResult{Allowed: count <= int64(limit), Count: count}
	if !result.Allowed {
		result.RetryAfter = window
	}
	return result, nil
}

// newPanelRateLimitTestService constructs the cached panel reader with an
// optional raw settings JSON value. Empty input leaves the settings key absent,
// letting the reader's default behavior participate in the test.
func newPanelRateLimitTestService(t *testing.T, settingsJSON string) *runtimeconfig.PanelSettings {
	t.Helper()
	repo := &panelRateLimitStubRepo{}
	if settingsJSON != "" {
		repo.values = map[string]string{"panel_rate_limit_settings": settingsJSON}
	}
	return runtimeconfig.NewPanelSettings(repo)
}

// newPanelTestRouter creates a test endpoint with optional subject and role
// injection before limiter. A nil identity leaves the request unauthenticated;
// supplied identity data remains available for every request through the
// returned router.
func newPanelTestRouter(limiter gin.HandlerFunc, identity *panelTestIdentity) *gin.Engine {
	router := gin.New()
	if identity != nil {
		router.Use(func(c *gin.Context) {
			c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: identity.userID})
			c.Set(string(authctx.ContextKeyUserRole), identity.role)
			c.Next()
		})
	}
	router.Use(limiter)
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

// performPanelRequest serves a GET against the test route using remoteAddr as
// the peer address. It returns the recorder for status, headers, and
// panel-envelope assertions after middleware has finished.
func performPanelRequest(router *gin.Engine, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestPanelRateLimiterGlobalPerUser serves two authenticated users behind the
// same peer address. One user exhausts its global bucket while the other
// remains admitted with a distinct counter key.
func TestPanelRateLimiterGlobalPerUser(t *testing.T) {
	allower := &fakePanelAllower{}
	p := &PanelRateLimiter{
		limiter:        allower,
		settingService: newPanelRateLimitTestService(t, `{"enabled":true,"user_rpm":2,"heavy_rpm":1,"exempt_admin":true,"public_ip_rpm":0}`),
	}

	userA := newPanelTestRouter(p.Global(), &panelTestIdentity{userID: 1, role: identitycore.RoleUser})
	userB := newPanelTestRouter(p.Global(), &panelTestIdentity{userID: 2, role: identitycore.RoleUser})

	require.Equal(t, http.StatusOK, performPanelRequest(userA, "127.0.0.1:1000").Code)
	require.Equal(t, http.StatusOK, performPanelRequest(userA, "127.0.0.1:1000").Code)
	// User A exhausts its limit.
	third := performPanelRequest(userA, "127.0.0.1:1000")
	require.Equal(t, http.StatusTooManyRequests, third.Code)
	require.NotEmpty(t, third.Header().Get("Retry-After"))
	require.Contains(t, third.Body.String(), "RATE_LIMITED")
	// User B has a separate bucket despite sharing the same source IP.
	require.Equal(t, http.StatusOK, performPanelRequest(userB, "127.0.0.1:1000").Code)

	allower.mu.Lock()
	defer allower.mu.Unlock()
	require.Contains(t, allower.counts, "panel:global:user:1")
	require.Contains(t, allower.counts, "panel:global:user:2")
}

// TestPanelRateLimiterHeavyUsesHeavyRPM gives one user a smaller heavy-query
// budget than the global budget. The heavy middleware rejects the repeated
// query using its own scoped key.
func TestPanelRateLimiterHeavyUsesHeavyRPM(t *testing.T) {
	allower := &fakePanelAllower{}
	p := &PanelRateLimiter{
		limiter:        allower,
		settingService: newPanelRateLimitTestService(t, `{"enabled":true,"user_rpm":100,"heavy_rpm":1,"exempt_admin":true,"public_ip_rpm":0}`),
	}

	router := newPanelTestRouter(p.Heavy(), &panelTestIdentity{userID: 7, role: identitycore.RoleUser})
	require.Equal(t, http.StatusOK, performPanelRequest(router, "127.0.0.1:1000").Code)
	require.Equal(t, http.StatusTooManyRequests, performPanelRequest(router, "127.0.0.1:1000").Code)

	allower.mu.Lock()
	defer allower.mu.Unlock()
	require.Contains(t, allower.counts, "panel:heavy:user:7")
}

// TestPanelRateLimiterAdminExemption toggles the cached administrator exemption
// with the same small user budget. Administrators continue when exempt and
// consume the ordinary budget when that switch is disabled.
func TestPanelRateLimiterAdminExemption(t *testing.T) {
	// Exempt administrators bypass counting.
	p := &PanelRateLimiter{
		limiter:        &fakePanelAllower{},
		settingService: newPanelRateLimitTestService(t, `{"enabled":true,"user_rpm":1,"heavy_rpm":1,"exempt_admin":true,"public_ip_rpm":0}`),
	}
	admin := newPanelTestRouter(p.Global(), &panelTestIdentity{userID: 9, role: identitycore.RoleAdmin})
	for range 5 {
		require.Equal(t, http.StatusOK, performPanelRequest(admin, "127.0.0.1:1000").Code)
	}

	// Disabling the exemption subjects administrators to the same limit.
	p2 := &PanelRateLimiter{
		limiter:        &fakePanelAllower{},
		settingService: newPanelRateLimitTestService(t, `{"enabled":true,"user_rpm":1,"heavy_rpm":1,"exempt_admin":false,"public_ip_rpm":0}`),
	}
	admin2 := newPanelTestRouter(p2.Global(), &panelTestIdentity{userID: 9, role: identitycore.RoleAdmin})
	require.Equal(t, http.StatusOK, performPanelRequest(admin2, "127.0.0.1:1000").Code)
	require.Equal(t, http.StatusTooManyRequests, performPanelRequest(admin2, "127.0.0.1:1000").Code)
}

// TestPanelRateLimiterDisabledOrMissingSubject exercises disabled settings,
// absent subjects, and nil limiter dependencies. Each case leaves the test
// endpoint available and the missing Redis counter does not cause a panic.
func TestPanelRateLimiterDisabledOrMissingSubject(t *testing.T) {
	// The disabled limiter allows requests.
	p := &PanelRateLimiter{
		limiter:        &fakePanelAllower{},
		settingService: newPanelRateLimitTestService(t, `{"enabled":false,"user_rpm":1,"heavy_rpm":1,"exempt_admin":true,"public_ip_rpm":1}`),
	}
	router := newPanelTestRouter(p.Global(), &panelTestIdentity{userID: 3, role: identitycore.RoleUser})
	for range 3 {
		require.Equal(t, http.StatusOK, performPanelRequest(router, "127.0.0.1:1000").Code)
	}

	// Missing authentication subjects bypass user counting.
	p2 := &PanelRateLimiter{
		limiter:        &fakePanelAllower{},
		settingService: newPanelRateLimitTestService(t, `{"enabled":true,"user_rpm":1,"heavy_rpm":1,"exempt_admin":true,"public_ip_rpm":0}`),
	}
	anonymous := newPanelTestRouter(p2.Global(), nil)
	for range 3 {
		require.Equal(t, http.StatusOK, performPanelRequest(anonymous, "127.0.0.1:1000").Code)
	}

	// A nil limiter allows requests.
	var nilLimiter *PanelRateLimiter
	nilRouter := newPanelTestRouter(nilLimiter.Global(), &panelTestIdentity{userID: 3, role: identitycore.RoleUser})
	require.Equal(t, http.StatusOK, performPanelRequest(nilRouter, "127.0.0.1:1000").Code)

	// A missing Redis counter must allow requests without panicking.
	withoutRedis := NewPanelRateLimiter(nil, newPanelRateLimitTestService(t, `{"enabled":true,"user_rpm":1,"heavy_rpm":1,"exempt_admin":false,"public_ip_rpm":1}`))
	withoutRedisRouter := newPanelTestRouter(withoutRedis.Global(), &panelTestIdentity{userID: 3, role: identitycore.RoleUser})
	require.Equal(t, http.StatusOK, performPanelRequest(withoutRedisRouter, "127.0.0.1:1000").Code)
}

// TestPanelRateLimiterFailOpenOnRedisError injects failed counts into
// authenticated and public panel buckets. Both endpoints remain available on
// repeated requests while their counters cannot produce admission observations.
func TestPanelRateLimiterFailOpenOnRedisError(t *testing.T) {
	p := &PanelRateLimiter{
		limiter:        &fakePanelAllower{err: errors.New("redis down")},
		settingService: newPanelRateLimitTestService(t, `{"enabled":true,"user_rpm":1,"heavy_rpm":1,"exempt_admin":true,"public_ip_rpm":1}`),
	}
	router := newPanelTestRouter(p.Global(), &panelTestIdentity{userID: 5, role: identitycore.RoleUser})
	for range 3 {
		require.Equal(t, http.StatusOK, performPanelRequest(router, "127.0.0.1:1000").Code)
	}

	publicRouter := newPanelTestRouter(p.PublicIP(), nil)
	for range 3 {
		require.Equal(t, http.StatusOK, performPanelRequest(publicRouter, "203.0.113.9:1000").Code)
	}
}

// TestPanelRateLimiterPublicIP serves public and local addresses through one
// public-address limiter. Public clients receive separate counters, while
// loopback and private peers bypass counting and never appear in the counter
// map.
func TestPanelRateLimiterPublicIP(t *testing.T) {
	allower := &fakePanelAllower{}
	p := &PanelRateLimiter{
		limiter:        allower,
		settingService: newPanelRateLimitTestService(t, `{"enabled":true,"user_rpm":0,"heavy_rpm":0,"exempt_admin":true,"public_ip_rpm":1}`),
	}
	router := newPanelTestRouter(p.PublicIP(), nil)

	// The second request from this public IP exceeds the limit.
	require.Equal(t, http.StatusOK, performPanelRequest(router, "203.0.113.9:1000").Code)
	require.Equal(t, http.StatusTooManyRequests, performPanelRequest(router, "203.0.113.9:1000").Code)
	// A different public IP has a separate bucket.
	require.Equal(t, http.StatusOK, performPanelRequest(router, "198.51.100.7:1000").Code)

	// Loopback and private proxy addresses bypass counting.
	for range 5 {
		require.Equal(t, http.StatusOK, performPanelRequest(router, "127.0.0.1:1000").Code)
		require.Equal(t, http.StatusOK, performPanelRequest(router, "10.0.0.8:1000").Code)
		require.Equal(t, http.StatusOK, performPanelRequest(router, "172.17.0.1:1000").Code)
		require.Equal(t, http.StatusOK, performPanelRequest(router, "192.168.1.30:1000").Code)
	}

	allower.mu.Lock()
	defer allower.mu.Unlock()
	require.Contains(t, allower.counts, "panel:public:ip:203.0.113.9")
	require.Contains(t, allower.counts, "panel:public:ip:198.51.100.7")
	for key := range allower.counts {
		require.NotContains(t, key, "127.0.0.1")
		require.NotContains(t, key, "10.0.0.8")
		require.NotContains(t, key, "172.17.0.1")
		require.NotContains(t, key, "192.168.1.30")
	}
}

// TestIsPubliclyRoutableClientIP compares global-unicast literals with local,
// invalid, and unspecified addresses. The predicate protects the public-bucket
// classification used by panel limiting, including IPv6 cases.
func TestIsPubliclyRoutableClientIP(t *testing.T) {
	require.True(t, isPubliclyRoutableClientIP("203.0.113.9"))
	require.True(t, isPubliclyRoutableClientIP("2001:db8::1"))
	require.False(t, isPubliclyRoutableClientIP("127.0.0.1"))
	require.False(t, isPubliclyRoutableClientIP("::1"))
	require.False(t, isPubliclyRoutableClientIP("10.1.2.3"))
	require.False(t, isPubliclyRoutableClientIP("172.16.0.1"))
	require.False(t, isPubliclyRoutableClientIP("192.168.0.1"))
	require.False(t, isPubliclyRoutableClientIP("169.254.1.1"))
	require.False(t, isPubliclyRoutableClientIP("fe80::1"))
	require.False(t, isPubliclyRoutableClientIP("fc00::1"))
	require.False(t, isPubliclyRoutableClientIP("0.0.0.0"))
	require.False(t, isPubliclyRoutableClientIP(""))
	require.False(t, isPubliclyRoutableClientIP("not-an-ip"))
}

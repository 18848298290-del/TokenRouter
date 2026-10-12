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

// panelRateLimitStubRepo implements the settings operations used by these tests in memory.
type panelRateLimitStubRepo struct {
	mu     sync.Mutex
	values map[string]string
}

// fakePanelAllower counts requests in memory without expiring windows.
type fakePanelAllower struct {
	mu     sync.Mutex
	counts map[string]int64
	err    error
}

type panelTestIdentity struct {
	userID int64
	role   string
}

func (r *panelRateLimitStubRepo) Get(_ context.Context, key string) (*settingscore.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, settingscore.ErrSettingNotFound
	}
	return &settingscore.Setting{Key: key, Value: value}, nil
}

func (r *panelRateLimitStubRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return value, nil
}

func (r *panelRateLimitStubRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

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

func (r *panelRateLimitStubRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	maps.Copy(r.values, settings)
	return nil
}

func (r *panelRateLimitStubRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	maps.Copy(out, r.values)
	return out, nil
}

func (r *panelRateLimitStubRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func (f *fakePanelAllower) Allow(_ context.Context, key string, limit int, window time.Duration) (AllowResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return AllowResult{}, f.err
	}
	if f.counts == nil {
		f.counts = make(map[string]int64)
	}
	f.counts[key]++
	count := f.counts[key]
	result := AllowResult{Allowed: count <= int64(limit), Count: count}
	if !result.Allowed {
		result.RetryAfter = window
	}
	return result, nil
}

func newPanelRateLimitTestService(t *testing.T, settingsJSON string) *runtimeconfig.PanelSettings {
	t.Helper()
	repo := &panelRateLimitStubRepo{}
	if settingsJSON != "" {
		repo.values = map[string]string{"panel_rate_limit_settings": settingsJSON}
	}
	return runtimeconfig.NewPanelSettings(repo)
}

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

func performPanelRequest(router *gin.Engine, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

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

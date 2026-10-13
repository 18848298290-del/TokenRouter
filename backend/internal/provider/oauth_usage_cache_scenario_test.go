package provider

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestOAuthUsageCachedResultsAreIndependent exercises cached Antigravity and
// Qoder service reads. It mutates returned redirect and quota values, then
// checks later results and the original cached countdown for retained
// observations.
func TestOAuthUsageCachedResultsAreIndependent(t *testing.T) {
	for _, platform := range []string{capability.PlatformAntigravity, capability.PlatformQoder} {
		t.Run(platform, func(t *testing.T) {
			reset := time.Now().Add(time.Hour)
			usage := &UsageInfo{FiveHour: &UsageProgress{RemainingSeconds: 777, ResetsAt: &reset}, ModelForwardingRules: map[string]string{"old": "new"}, QoderQuota: &QoderQuotaInfo{UserQuota: &QoderQuotaProgress{Remaining: 12}}}
			cache := NewOAuthUsageCache()
			svc := NewOAuthUsageService(nil, cache, nil, OAuthUsageOptions{Antigravity: AntigravityUsageOptions{CanFetch: (&AntigravityQuota{}).CanFetch}})
			value := &Record{ID: 981, Platform: platform, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}}
			read := func() (*UsageInfo, error) {
				if platform == capability.PlatformAntigravity {
					return svc.GetAntigravityUsage(context.Background(), value)
				}
				return svc.GetQoderUsage(context.Background(), value, false)
			}
			if platform == capability.PlatformAntigravity {
				cache.StoreAntigravity(value.ID, &OAuthAntigravityUsageCache{Identity: UsageCacheIdentity(value), UsageInfo: usage, Timestamp: time.Now()})
			} else {
				cache.StoreQoder(value.ID, &OAuthQoderUsageCache{Identity: UsageCacheIdentity(value), UsageInfo: usage, Timestamp: time.Now()})
			}
			first, err := read()
			require.NoError(t, err)
			require.NotNil(t, first)
			first.ModelForwardingRules["old"] = "caller"
			first.QoderQuota.UserQuota.Remaining = 0
			second, err := read()
			require.NoError(t, err)
			require.NotNil(t, second)
			require.Equal(t, "new", second.ModelForwardingRules["old"])
			require.Equal(t, float64(12), second.QoderQuota.UserQuota.Remaining)
			require.Equal(t, 777, usage.FiveHour.RemainingSeconds, "倒计时不能原位写入缓存对象")
		})
	}
}

// TestOAuthUsageSharedResultsAreIndependent holds one fetch while two callers
// join its query. It checks one upstream call, independent returned
// redirect/quota values, and an unchanged subsequent cached observation.
func TestOAuthUsageSharedResultsAreIndependent(t *testing.T) {
	for _, platform := range []string{capability.PlatformAntigravity, capability.PlatformQoder} {
		t.Run(platform, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan struct{})
				release := make(chan struct{})
				calls := 0
				fetch := func() (*UsageInfo, error) {
					calls++
					close(entered)
					<-release
					return &UsageInfo{ModelForwardingRules: map[string]string{"old": "new"}, QoderQuota: &QoderQuotaInfo{UserQuota: &QoderQuotaProgress{Remaining: 12}}}, nil
				}
				svc := NewOAuthUsageService(nil, NewOAuthUsageCache(), nil, OAuthUsageOptions{
					Antigravity: AntigravityUsageOptions{
						CanFetch: func(*Record) bool { return true },
						Enrich:   func(*UsageInfo, *Record) {},
						Fetch:    func(context.Context, *Record) (*UsageInfo, error) { return fetch() },
					},
					Qoder: QoderUsageOptions{
						Enrich: func(*UsageInfo, *Record) {},
						Fetch:  func(context.Context, *Record, func() time.Time) (*UsageInfo, error) { return fetch() },
					},
				})
				value := &Record{ID: 981, Platform: platform, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}}
				read := func() (*UsageInfo, error) {
					if platform == capability.PlatformAntigravity {
						return svc.GetAntigravityUsage(context.Background(), value)
					}
					return svc.GetQoderUsage(context.Background(), value, false)
				}
				results := make(chan *UsageInfo, 2)
				errors := make(chan error, 2)
				call := func() {
					result, err := read()
					results <- result
					errors <- err
				}
				go call()
				<-entered
				go call()
				// Wait until both callers have joined the
				// blocked flight before release. This fixes the
				// shared-fetch interleaving so one query feeds
				// independently copied results.
				synctest.Wait()
				require.Equal(t, 1, calls)
				close(release)
				first, second := <-results, <-results
				require.NoError(t, <-errors)
				require.NoError(t, <-errors)
				require.Equal(t, 1, calls)
				first.ModelForwardingRules["old"] = "caller"
				first.QoderQuota.UserQuota.Remaining = 0
				require.Equal(t, "new", second.ModelForwardingRules["old"])
				require.Equal(t, float64(12), second.QoderQuota.UserQuota.Remaining)
				cached, err := read()
				require.NoError(t, err)
				require.Equal(t, "new", cached.ModelForwardingRules["old"])
				require.Equal(t, float64(12), cached.QoderQuota.UserQuota.Remaining)
			})
		})
	}
}

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamkimi "github.com/TokenFlux/TokenRouter/internal/upstream/kimi"
)

// errorPolicyRepoStub captures explicit provider error, temporary suspension,
// and model-limit writes.
type errorPolicyRepoStub struct {
	providercore.HealthStore // unimplemented provider health methods
	// tempCalls counts temporary suspension writes, while setErrCalls counts
	// provider error-state writes in the sequential policy fixture.
	tempCalls, setErrCalls int
	modelRateLimitCalls    []int64 // captured model cooldown provider IDs
}

// jevRateLimitRepoStub captures retry-deadline cooldowns from native decision
// response handling.
type jevRateLimitRepoStub struct {
	providercore.HealthStore           // unimplemented provider health methods
	calls                    int       // observed decision cooldown write count
	reset                    time.Time // captured decision cooldown deadline
}

// modelNotFoundRateLimitCall records one provider-model cooldown observation
// for scope assertions.
type modelNotFoundRateLimitCall struct {
	providerID int64     // target provider record identifier
	scope      string    // target model cooldown key
	resetAt    time.Time // captured model recovery deadline
	reason     string    // first supplied cooldown reason
}

// modelNotFoundProviderRepoStub captures model cooldowns and detects unexpected
// provider-wide suspension writes.
type modelNotFoundProviderRepoStub struct {
	providercore.HealthStore                              // unimplemented provider health methods
	tempCalls                int                          // captured provider suspension write count
	modelRateLimitCalls      []modelNotFoundRateLimitCall // captured model cooldown write arguments
	// modelRateLimitErr returns after SetModelRateLimit captures the attempted
	// scope and deadline. Nil lets the fixture report a successful scoped write.
	modelRateLimitErr error
}

// overloadProviderRepoStub captures overload cooldowns separately from provider
// error-state writes.
type overloadProviderRepoStub struct {
	providercore.HealthStore           // unimplemented provider health methods
	overloadCalls            int       // captured overload pause write count
	errorCalls               int       // captured provider error write count
	lastOverloadID           int64     // latest overloaded provider identifier
	lastOverloadEnd          time.Time // latest overload recovery deadline
}

// errSettingRepo returns a configured settings-read error to exercise
// production defaults.
type errSettingRepo struct {
	cooldownSettingsStore       // embedded ordinary settings fixture
	readErr               error // configured settings lookup error result
}

// unauthorizedHealthStore captures authentication recovery and credential-owner
// lookup for health assertions.
type unauthorizedHealthStore struct {
	providercore.HealthStore // unimplemented provider health methods
	// providersByID retains configured credential-owner pointers as lookup input.
	// GetByID delegates copying to CloneRecord; tests keep stored records stable
	// during authentication recovery.
	providersByID          map[int64]*providercore.Record // stored credential-owner fixtures by ID
	setErrorCalls          int                            // captured authentication error write count
	tempCalls              int                            // captured temporary suspension write count
	updateCredentialsCalls int                            // captured credential patch write count
	updateExtraCalls       int                            // captured Extra patch write count
	// lastCredentials and lastExtraUpdates each own a copied top-level patch.
	// Nested values remain shared with the corresponding writer input while
	// sequential assertions inspect the latest captured call.
	lastCredentials  map[string]any // latest shallow-copied credential patch
	lastExtraUpdates map[string]any // latest shallow-copied Extra patch
	lastErrorMsg     string         // latest authentication failure diagnostic
	lastTempUntil    time.Time      // latest temporary suspension deadline
	lastTempReason   string         // latest temporary suspension reason
	lastErrorID      int64          // latest error-state target identifier
	lastTempID       int64          // latest suspension target identifier
}

// unauthorizedTokenRecorder retains invalidated provider pointers and supplies
// configured cache failures.
type unauthorizedTokenRecorder struct {
	// providers retains invalidated record pointers, including calls that return
	// err. Tests keep these captures stable while inspecting credential-owner
	// identity.
	providers []*providercore.Record
	err       error // configured token invalidation error result
}

// SetRateLimited counts the native decision cooldown and retains its reset
// deadline.
func (r *jevRateLimitRepoStub) SetRateLimited(_ context.Context, _ int64, reset time.Time) error {
	r.calls++
	r.reset = reset
	return nil
}

// TestJevHealthUsesRetryAfter compares 429 and overload responses with ordinary
// and pool records. Ordinary handling must use the supplied retry interval
// while pool handling skips local cooldown writes.
func TestJevHealthUsesRetryAfter(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, 529} {
		for _, pool := range []bool{false, true} {
			repo := &jevRateLimitRepoStub{}
			observer := &UpstreamHealth{Core: providercore.NewHealthService(repo, nil, providercore.HealthOptions{})}
			value := &providercore.Record{ID: 9, Platform: providercore.PlatformJev, Type: providercore.ProviderTypeAPIKey, Credentials: map[string]any{"pool_mode": pool}}
			before := time.Now()
			decision := observer.ApplyUpstreamError(context.Background(), value, HealthObservation{Status: status, Headers: http.Header{"Retry-After": []string{"17"}}})
			require.False(t, decision.StopScheduling)
			if pool {
				require.Zero(t, repo.calls)
			} else {
				require.Equal(t, 1, repo.calls)
				require.WithinDuration(t, before.Add(17*time.Second), repo.reset, time.Second)
			}
		}
	}
}

// TestJevOverloadHonorsTemporaryRule supplies maintenance text without
// Retry-After on a Jev overload. The configured temporary rule must stop
// scheduling and record its suspension before generic overload handling.
func TestJevOverloadHonorsTemporaryRule(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	observer := newErrorPolicyObserver(repo)
	value := &providercore.Record{
		ID: 9, Platform: providercore.PlatformJev, Type: providercore.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{
				"error_code": float64(529), "keywords": []any{"maintenance"}, "duration_minutes": float64(30),
			}},
		},
	}
	decision := observer.ApplyUpstreamError(context.Background(), value, HealthObservation{Status: 529, Body: []byte("Service maintenance")})
	require.True(t, decision.StopScheduling)
	require.Equal(t, 1, repo.tempCalls)
}

// TestIsCNProviderConcurrencyLimit403_ExactClassification checks Kimi's
// recognized message, trimmed form, near matches, and other platforms. Only the
// exact platform-supported concurrency wording must classify as that
// recoverable limit.
func TestIsCNProviderConcurrencyLimit403_ExactClassification(t *testing.T) {
	kimi := &providercore.Record{Platform: capability.PlatformKimi}

	require.True(t, CNConcurrencyLimit403(kimi, upstreamkimi.ConcurrentRequestLimitMessage))
	require.True(t, CNConcurrencyLimit403(kimi, "  "+upstreamkimi.ConcurrentRequestLimitMessage+"\n"))

	for name, tc := range map[string]struct {
		provider *providercore.Record // provider record under evaluation
		message  string               // supplied platform diagnostic text
	}{
		"permission denied":              {kimi, "You do not have permission to access this resource."},
		"generic concurrency wording":    {kimi, "concurrent request limit reached"},
		"near match missing punctuation": {kimi, "You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again"},
		"other CN provider":              {&providercore.Record{Platform: capability.PlatformZhipu}, upstreamkimi.ConcurrentRequestLimitMessage},
		"non CN provider":                {&providercore.Record{Platform: capability.PlatformOpenAI}, upstreamkimi.ConcurrentRequestLimitMessage},
		"nil provider":                   {nil, upstreamkimi.ConcurrentRequestLimitMessage},
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, CNConcurrencyLimit403(tc.provider, tc.message))
		})
	}
}

// TestCheckErrorPolicy combines custom codes, temporary rules, overload
// defaults, and pool bypass cases. The core policy result must retain their
// configured precedence, including Antigravity's repeated-401 temporary
// handling.
func TestCheckErrorPolicy(t *testing.T) {
	tests := []struct {
		name       string                         // health policy scenario label
		provider   *providercore.Record           // provider record under evaluation
		statusCode int                            // observed upstream HTTP response code
		body       []byte                         // supplied upstream error body bytes
		expected   providercore.ErrorPolicyResult // expected configured error policy result
	}{
		{
			name: "no_policy_oauth_returns_none",
			provider: &providercore.Record{
				ID:       1,
				Type:     capability.ProviderTypeOAuth,
				Platform: capability.PlatformAntigravity,
				// no custom error codes or temporary rules
			},
			statusCode: 500,
			body:       []byte(`"error"`),
			expected:   providercore.ErrorPolicyNone,
		},
		{
			name: "custom_error_codes_hit_returns_matched",
			provider: &providercore.Record{
				ID:       2,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformAntigravity,
				Credentials: map[string]any{
					"custom_error_codes_enabled": true,
					"custom_error_codes":         []any{float64(429), float64(500)},
				},
			},
			statusCode: 500,
			body:       []byte(`"error"`),
			expected:   providercore.ErrorPolicyCustomMatched,
		},
		{
			name: "custom_error_codes_miss_returns_skipped",
			provider: &providercore.Record{
				ID:       3,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformAntigravity,
				Credentials: map[string]any{
					"custom_error_codes_enabled": true,
					"custom_error_codes":         []any{float64(429), float64(500)},
				},
			},
			statusCode: 503,
			body:       []byte(`"error"`),
			expected:   providercore.ErrorPolicyCustomSkipped,
		},
		{
			name: "custom_error_codes_excluding_529_skip_global_cooldown",
			provider: &providercore.Record{
				ID:       33,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"custom_error_codes_enabled": true,
					"custom_error_codes":         []any{float64(429)},
				},
			},
			statusCode: 529,
			body:       []byte(`{"error":{"message":"overloaded"}}`),
			expected:   providercore.ErrorPolicyCustomSkipped,
		},
		{
			name: "pool_mode_skips_global_529_cooldown",
			provider: &providercore.Record{
				ID:       34,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode": true,
				},
			},
			statusCode: 529,
			body:       []byte(`{"error":{"message":"overloaded"}}`),
			expected:   providercore.ErrorPolicyPoolBypassed,
		},
		{
			name: "ordinary_provider_uses_global_529_cooldown",
			provider: &providercore.Record{
				ID:       35,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
			},
			statusCode: 529,
			body:       []byte(`{"error":{"message":"overloaded"}}`),
			expected:   providercore.ErrorPolicyCustomMatched,
		},
		{
			name: "custom_error_codes_including_529_take_precedence",
			provider: &providercore.Record{
				ID:       36,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"custom_error_codes_enabled": true,
					"custom_error_codes":         []any{float64(529)},
				},
			},
			statusCode: 529,
			body:       []byte(`{"error":{"message":"overloaded"}}`),
			expected:   providercore.ErrorPolicyCustomMatched,
		},
		{
			name: "temp_unschedulable_hit_returns_temp_unscheduled",
			provider: &providercore.Record{
				ID:       4,
				Type:     capability.ProviderTypeOAuth,
				Platform: capability.PlatformAntigravity,
				Credentials: map[string]any{
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       float64(503),
							"keywords":         []any{"overloaded"},
							"duration_minutes": float64(10),
							"description":      "overloaded rule",
						},
					},
				},
			},
			statusCode: 503,
			body:       []byte(`overloaded service`),
			expected:   providercore.ErrorPolicyTempUnscheduled,
		},
		{
			name: "temp_unschedulable_401_first_hit_returns_temp_unscheduled",
			provider: &providercore.Record{
				ID:       14,
				Type:     capability.ProviderTypeOAuth,
				Platform: capability.PlatformAntigravity,
				Credentials: map[string]any{
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       float64(401),
							"keywords":         []any{"unauthorized"},
							"duration_minutes": float64(10),
						},
					},
				},
			},
			statusCode: 401,
			body:       []byte(`unauthorized`),
			expected:   providercore.ErrorPolicyTempUnscheduled,
		},
		{
			// Antigravity 401 handling uses temp_unschedulable_rules without escalation,
			// so the second hit also returns TempUnscheduled.
			name: "temp_unschedulable_401_second_hit_antigravity_stays_temp",
			provider: &providercore.Record{
				ID:                      15,
				Type:                    capability.ProviderTypeOAuth,
				Platform:                capability.PlatformAntigravity,
				TempUnschedulableReason: `{"status_code":401,"until_unix":1735689600}`,
				Credentials: map[string]any{
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       float64(401),
							"keywords":         []any{"unauthorized"},
							"duration_minutes": float64(10),
						},
					},
				},
			},
			statusCode: 401,
			body:       []byte(`unauthorized`),
			expected:   providercore.ErrorPolicyTempUnscheduled,
		},
		{
			name: "temp_unschedulable_body_miss_returns_none",
			provider: &providercore.Record{
				ID:       5,
				Type:     capability.ProviderTypeOAuth,
				Platform: capability.PlatformAntigravity,
				Credentials: map[string]any{
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       float64(503),
							"keywords":         []any{"overloaded"},
							"duration_minutes": float64(10),
							"description":      "overloaded rule",
						},
					},
				},
			},
			statusCode: 503,
			body:       []byte(`random msg`),
			expected:   providercore.ErrorPolicyNone,
		},
		{
			name: "custom_error_codes_override_temp_unschedulable",
			provider: &providercore.Record{
				ID:       6,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformAntigravity,
				Credentials: map[string]any{
					"custom_error_codes_enabled": true,
					"custom_error_codes":         []any{float64(503)},
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       float64(503),
							"keywords":         []any{"overloaded"},
							"duration_minutes": float64(10),
							"description":      "overloaded rule",
						},
					},
				},
			},
			statusCode: 503,
			body:       []byte(`overloaded`),
			expected:   providercore.ErrorPolicyCustomMatched, // custom codes take precedence
		},
		{
			name: "pool_mode_custom_error_codes_hit_returns_matched",
			provider: &providercore.Record{
				ID:       7,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":                  true,
					"custom_error_codes_enabled": true,
					"custom_error_codes":         []any{float64(401), float64(403)},
				},
			},
			statusCode: 401,
			body:       []byte(`unauthorized`),
			expected:   providercore.ErrorPolicyCustomMatched,
		},
		{
			name: "pool_mode_without_custom_error_codes_returns_bypassed",
			provider: &providercore.Record{
				ID:       8,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode": true,
				},
			},
			statusCode: 401,
			body:       []byte(`unauthorized`),
			expected:   providercore.ErrorPolicyPoolBypassed,
		},
		{
			name: "pool_mode_temp_unschedulable_hit_returns_temp_unscheduled",
			provider: &providercore.Record{
				ID:       9,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":                  true,
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       float64(http.StatusServiceUnavailable),
							"keywords":         []any{"unavailable"},
							"duration_minutes": float64(30),
						},
					},
				},
			},
			statusCode: http.StatusServiceUnavailable,
			body:       []byte(`Service temporarily unavailable`),
			expected:   providercore.ErrorPolicyTempUnscheduled,
		},
		{
			name: "pool_mode_repeated_401_explicit_rule_stays_temp_unscheduled",
			provider: &providercore.Record{
				ID:                      11,
				Type:                    capability.ProviderTypeAPIKey,
				Platform:                capability.PlatformOpenAI,
				TempUnschedulableReason: `{"status_code":401,"until_unix":1735689600}`,
				Credentials: map[string]any{
					"pool_mode":                  true,
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       float64(http.StatusUnauthorized),
							"keywords":         []any{"unauthorized"},
							"duration_minutes": float64(30),
						},
					},
				},
			},
			statusCode: http.StatusUnauthorized,
			body:       []byte(`unauthorized`),
			expected:   providercore.ErrorPolicyTempUnscheduled,
		},
		{
			name: "pool_mode_temp_unschedulable_miss_returns_bypassed",
			provider: &providercore.Record{
				ID:       10,
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":                  true,
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       float64(http.StatusServiceUnavailable),
							"keywords":         []any{"maintenance"},
							"duration_minutes": float64(30),
						},
					},
				},
			},
			statusCode: http.StatusServiceUnavailable,
			body:       []byte(`Service temporarily unavailable`),
			expected:   providercore.ErrorPolicyPoolBypassed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &errorPolicyRepoStub{}
			svc := newErrorPolicyObserver(repo)

			result := svc.CheckErrorPolicy(context.Background(), tt.provider, HealthObservation{Status: tt.statusCode, Body: tt.body})
			require.Equal(t, tt.expected, result, "unexpected ErrorPolicyResult")
		})
	}
}

// TestHandleUpstreamError_PoolModePolicies compares unconfigured pools,
// custom-code pools, and matching or missed temporary rules. Decisions must
// write only the explicitly applicable state and otherwise keep the pool
// bypass.
func TestHandleUpstreamError_PoolModePolicies(t *testing.T) {
	t.Run("pool_mode_without_custom_error_codes_still_skips", func(t *testing.T) {
		repo := &errorPolicyRepoStub{}
		svc := newErrorPolicyObserver(repo)
		provider := &providercore.Record{
			ID:       30,
			Type:     capability.ProviderTypeAPIKey,
			Platform: capability.PlatformOpenAI,
			Credentials: map[string]any{
				"pool_mode": true,
			},
		}

		shouldDisable := svc.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Headers: http.Header{}, Body: []byte("unauthorized")}).StopScheduling

		require.False(t, shouldDisable)
		require.Equal(t, 0, repo.setErrCalls)
		require.Equal(t, 0, repo.tempCalls)
	})

	t.Run("pool_mode_with_custom_error_codes_uses_local_error_policy", func(t *testing.T) {
		repo := &errorPolicyRepoStub{}
		svc := newErrorPolicyObserver(repo)
		provider := &providercore.Record{
			ID:       31,
			Type:     capability.ProviderTypeAPIKey,
			Platform: capability.PlatformOpenAI,
			Credentials: map[string]any{
				"pool_mode":                  true,
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(401)},
			},
		}

		shouldDisable := svc.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Headers: http.Header{}, Body: []byte("unauthorized")}).StopScheduling

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrCalls)
		require.Equal(t, 0, repo.tempCalls)
	})

	t.Run("pool_mode_explicit_temp_rule_stops_scheduling", func(t *testing.T) {
		repo := &errorPolicyRepoStub{}
		svc := newErrorPolicyObserver(repo)
		provider := &providercore.Record{
			ID:       32,
			Type:     capability.ProviderTypeAPIKey,
			Platform: capability.PlatformOpenAI,
			Credentials: map[string]any{
				"pool_mode":                  true,
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusServiceUnavailable),
						"keywords":         []any{"unavailable"},
						"duration_minutes": float64(30),
					},
				},
			},
		}

		shouldDisable := svc.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: http.StatusServiceUnavailable, Headers: http.Header{}, Body: []byte("Service temporarily unavailable")}).StopScheduling

		require.True(t, shouldDisable)
		require.Equal(t, 0, repo.setErrCalls)
		require.Equal(t, 1, repo.tempCalls)
	})

	t.Run("pool_mode_temp_rule_miss_still_skips", func(t *testing.T) {
		repo := &errorPolicyRepoStub{}
		svc := newErrorPolicyObserver(repo)
		provider := &providercore.Record{
			ID:       33,
			Type:     capability.ProviderTypeAPIKey,
			Platform: capability.PlatformOpenAI,
			Credentials: map[string]any{
				"pool_mode":                  true,
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusServiceUnavailable),
						"keywords":         []any{"maintenance"},
						"duration_minutes": float64(30),
					},
				},
			},
		}

		shouldDisable := svc.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: http.StatusServiceUnavailable, Headers: http.Header{}, Body: []byte("Service temporarily unavailable")}).StopScheduling

		require.False(t, shouldDisable)
		require.Equal(t, 0, repo.setErrCalls)
		require.Equal(t, 0, repo.tempCalls)
	})
}

// TestHandleUpstreamError_CustomCodesAlwaysStopScheduling matches configured
// 400, 429, and overload codes in pool mode. The custom policy must stop
// scheduling, reject same-provider retry, and record one error write.
func TestHandleUpstreamError_CustomCodesAlwaysStopScheduling(t *testing.T) {
	tests := []struct {
		name       string // health policy scenario label
		statusCode int    // observed upstream HTTP response code
	}{
		{name: "bad_request", statusCode: http.StatusBadRequest},
		{name: "rate_limit", statusCode: http.StatusTooManyRequests},
		{name: "overloaded", statusCode: 529},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &errorPolicyRepoStub{}
			svc := newErrorPolicyObserver(repo)
			provider := &providercore.Record{
				ID:       int64(1000 + tt.statusCode),
				Type:     capability.ProviderTypeAPIKey,
				Platform: capability.PlatformOpenAI,
				Credentials: map[string]any{
					"pool_mode":                  true,
					"custom_error_codes_enabled": true,
					"custom_error_codes":         []any{float64(tt.statusCode)},
				},
			}

			decision := svc.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: tt.statusCode, Headers: http.Header{}, Body: []byte(`{"error":{"message":"configured failure"}}`)})

			require.Equal(t, providercore.ErrorPolicyCustomMatched, decision.Policy)
			require.True(t, decision.StopScheduling)
			require.False(t, decision.RetryableOnSameProvider(provider, tt.statusCode))
			require.Equal(t, 1, repo.setErrCalls)
			require.Equal(t, 0, repo.tempCalls)
		})
	}
}

// TestUpstreamErrorDecision_PoolRetryStatusPromotesFailover configures a pool
// retry code for an unprocessable response. The bypass decision must allow
// retry and failover while provider and model health writers remain unused.
func TestUpstreamErrorDecision_PoolRetryStatusPromotesFailover(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	svc := newErrorPolicyObserver(repo)
	provider := &providercore.Record{
		ID:       20422,
		Type:     capability.ProviderTypeAPIKey,
		Platform: capability.PlatformOpenAI,
		Credentials: map[string]any{
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(http.StatusUnprocessableEntity)},
		},
	}

	decision := svc.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: http.StatusUnprocessableEntity, Headers: http.Header{}, Body: []byte(`{"error":{"message":"unprocessable"}}`)})

	require.Equal(t, providercore.ErrorPolicyPoolBypassed, decision.Policy)
	require.True(t, decision.ShouldFailover(provider, http.StatusUnprocessableEntity, false))
	require.True(t, decision.RetryableOnSameProvider(provider, http.StatusUnprocessableEntity))
	require.Zero(t, repo.setErrCalls)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestUpstreamErrorDecision_UsesSeparateEntryDefaults compares ordinary,
// pool-bypass, matched, and skipped decisions with entry-specific defaults.
// Explicit policy must keep its precedence over those caller defaults.
func TestUpstreamErrorDecision_UsesSeparateEntryDefaults(t *testing.T) {
	provider := &providercore.Record{
		ID:          20423,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true},
	}

	require.False(t, (providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}).ShouldFailoverWithDefaults(provider, http.StatusBadGateway, false, true))
	require.True(t, (providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyPoolBypassed}).ShouldFailoverWithDefaults(provider, http.StatusBadGateway, false, true))
	require.True(t, (providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyCustomMatched}).ShouldFailoverWithDefaults(provider, http.StatusUnprocessableEntity, false, false))
	require.False(t, (providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyCustomSkipped}).ShouldFailoverWithDefaults(provider, http.StatusBadGateway, true, true))
}

// TestRateLimitService_HandleUpstreamError_ModelNotFoundUsesModelRateLimit
// observes an explicit model-not-found response with a model name. Handling
// must stop the attempt and store that model's standard cooldown without
// provider-wide suspension.
func TestRateLimitService_HandleUpstreamError_ModelNotFoundUsesModelRateLimit(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`), "gpt-5.4")).StopScheduling

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, provider.ID, call.providerID)
	require.Equal(t, "gpt-5.4", call.scope)
	require.Equal(t, providercore.ModelNotFoundReason, call.reason)
	require.WithinDuration(t, time.Now().Add(providercore.ModelNotFoundCooldown), call.resetAt, 5*time.Second)
}

// TestRateLimitService_PoolModeModelNotFoundSkipsDefaultModelPause observes a
// model-not-found response from a pool record. The bypass decision must leave
// provider and model cooldown captures empty.
func TestRateLimitService_PoolModeModelNotFoundSkipsDefaultModelPause(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := &providercore.Record{
		ID:          102,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"pool_mode": true},
	}

	decision := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`), "gpt-5.4"))

	require.Equal(t, providercore.ErrorPolicyPoolBypassed, decision.Policy)
	require.False(t, decision.StopScheduling)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestRateLimitService_HandleUpstreamError_ModelNotFoundWriteFailureDoesNotTempUnschedule
// fails the model-not-found cooldown write. The error must remain handled while
// no broader provider suspension is substituted for that failed scoped write.
func TestRateLimitService_HandleUpstreamError_ModelNotFoundWriteFailureDoesNotTempUnschedule(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{modelRateLimitErr: errors.New("write failed")}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`), "gpt-5.4")).StopScheduling

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

// TestRateLimitService_HandleUpstreamError_Bare404UsesModelScopedTempUnschedulableWhenModelKnown
// matches a configured not-found rule with a model name. Storage must target
// that model and retain the matched status and keyword in its reason.
func TestRateLimitService_HandleUpstreamError_Bare404UsesModelScopedTempUnschedulableWhenModelKnown(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"endpoint not found"}}`), "gpt-5.4")).StopScheduling

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, provider.ID, call.providerID)
	require.Equal(t, "gpt-5.4", call.scope)
	require.WithinDuration(t, time.Now().Add(10*time.Minute), call.resetAt, 5*time.Second)

	var state providercore.TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(call.reason), &state))
	require.Equal(t, http.StatusNotFound, state.StatusCode)
	require.Equal(t, "not found", state.MatchedKeyword)
}

// TestRateLimitService_HandleUpstreamError_Bare404WithoutModelKeepsProviderTempUnschedulable
// matches the same rule without a model observation. Handling must use
// provider-wide temporary suspension and leave model cooldown captures empty.
func TestRateLimitService_HandleUpstreamError_Bare404WithoutModelKeepsProviderTempUnschedulable(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"endpoint not found"}}`))).StopScheduling

	require.True(t, handled)
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestRateLimitService_HandleUpstreamError_ModelTempWriteFailureNeverWidensToProvider
// fails temporary model cooldown persistence for a known request model. The
// attempt must remain handled while provider-wide suspension stays unused, so
// the write failure cannot widen its stored scope.
func TestRateLimitService_HandleUpstreamError_ModelTempWriteFailureNeverWidensToProvider(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{modelRateLimitErr: errors.New("write failed")}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"endpoint not found"}}`), "gpt-5.4")).StopScheduling

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

// TestRateLimitService_HandleTempUnschedulable_PoolModeAppliesExplicitModelRule
// invokes a matching explicit temporary rule on a pool. The write must retain
// the supplied model scope without provider-wide suspension.
func TestRateLimitService_HandleTempUnschedulable_PoolModeAppliesExplicitModelRule(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()
	provider.Credentials["pool_mode"] = true

	handled := svc.Core.HandleTempUnschedulable(context.Background(), provider, http.StatusNotFound, []byte(`{"error":{"message":"endpoint not found"}}`), "gpt-5.4")

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
}

// TestRateLimitService_HandleTempUnschedulable_PoolModeCustomPolicyUsesModelScope
// supplies both pool and matching custom policy to direct temporary handling.
// The explicit rule must still write the known model scope.
func TestRateLimitService_HandleTempUnschedulable_PoolModeCustomPolicyUsesModelScope(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()
	provider.Credentials["pool_mode"] = true
	provider.Credentials["custom_error_codes_enabled"] = true
	provider.Credentials["custom_error_codes"] = []any{float64(http.StatusNotFound)}

	handled := svc.Core.HandleTempUnschedulable(context.Background(), provider, http.StatusNotFound, []byte(`{"error":{"message":"endpoint not found"}}`), "gpt-5.4")

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
}

// TestRateLimitService_HandleUpstreamError_CustomPolicyExclusionSkipsAllState
// excludes the observed 404 from configured custom codes. Handling must leave
// the stop flag false and skip provider and model temporary writes.
func TestRateLimitService_HandleUpstreamError_CustomPolicyExclusionSkipsAllState(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()
	provider.Credentials["custom_error_codes_enabled"] = true
	provider.Credentials["custom_error_codes"] = []any{float64(http.StatusServiceUnavailable)}

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"endpoint not found"}}`), "gpt-5.4")).StopScheduling

	require.False(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestRateLimitService_HandleTempUnschedulable_AuthenticationFailureStaysProviderScoped
// invokes an authentication temporary rule with a model argument. The resulting
// write must target the provider, leaving model cooldown state unused.
func TestRateLimitService_HandleTempUnschedulable_AuthenticationFailureStaysProviderScoped(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAIModelNotFoundTempProvider()
	provider.TempUnschedulableReason = "legacy non-JSON reason"
	provider.Credentials["temp_unschedulable_rules"] = []any{
		map[string]any{
			"error_code":       float64(http.StatusUnauthorized),
			"keywords":         []any{"unauthorized"},
			"duration_minutes": float64(10),
		},
	}

	handled := svc.Core.HandleTempUnschedulable(context.Background(), provider, http.StatusUnauthorized, []byte(`{"error":{"message":"unauthorized"}}`), "gpt-5.4")

	require.True(t, handled)
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelUsesModelRateLimit
// observes a subscription-gated text model on OAuth. Handling must store its
// dedicated model cooldown and reason while skipping provider-wide suspension.
func TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelUsesModelRateLimit(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAICodexPlanGatedOAuthProvider()

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusBadRequest, http.Header{}, []byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT provider."}`), "gpt-5.6-sol")).StopScheduling

	require.True(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, provider.ID, call.providerID)
	require.Equal(t, "gpt-5.6-sol", call.scope)
	require.Equal(t, providercore.CodexPlanGatedModelReason, call.reason)
	require.WithinDuration(t, time.Now().Add(providercore.CodexPlanGatedModelCooldown), call.resetAt, 5*time.Second)
}

// TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelUsesFinalUpstreamModel
// supplies an already mapped model with another matching mapping key. The
// stored scope must retain that upstream name instead of following another
// alias step.
func TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelUsesFinalUpstreamModel(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAICodexPlanGatedOAuthProvider()
	// Supply a final upstream ID that also names another mapping key. A second
	// mapping pass would move this observed denial onto the wrong model's
	// cooldown.
	provider.Credentials["model_mapping"] = map[string]any{
		"gpt-5.6-sol":       "external-model-v1",
		"external-model-v1": "unexpected-remap",
	}

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusBadRequest, http.Header{}, []byte(`{"detail":"The 'external-model-v1' model is not supported when using Codex with a ChatGPT provider."}`), "external-model-v1")).StopScheduling

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "external-model-v1", repo.modelRateLimitCalls[0].scope)
}

// TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelIgnoresAPIKeyProvider
// supplies the OAuth-specific denial body to an API-key record. Handling must
// return false and leave model cooldown captures empty.
func TestRateLimitService_HandleUpstreamError_CodexPlanGatedModelIgnoresAPIKeyProvider(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAICodexPlanGatedOAuthProvider()
	provider.Type = capability.ProviderTypeAPIKey

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusBadRequest, http.Header{}, []byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT provider."}`), "gpt-5.6-sol")).StopScheduling

	require.False(t, handled)
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelSkipsCooldown
// supplies image model names on a text endpoint. The decision must stop
// scheduling for this attempt while model and provider-wide cooldown writers
// remain unused.
func TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelSkipsCooldown(t *testing.T) {
	for _, model := range []string{"gpt-image-1", "gpt-image-1.5", "gpt-image-2"} {
		t.Run(model, func(t *testing.T) {
			repo := &modelNotFoundProviderRepoStub{}
			svc := newModelHealthObserver(repo)
			provider := openAICodexPlanGatedOAuthProvider()

			handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusBadRequest, http.Header{}, []byte(`{"detail":"The '`+model+`' model is not supported when using Codex with a ChatGPT provider."}`), model)).StopScheduling

			require.True(t, handled, "当前文本端点尝试仍应切换提供商")
			require.Empty(t, repo.modelRateLimitCalls, "文本端点错配不应冷却提供商的图片模型")
			require.Zero(t, repo.tempCalls)
		})
	}
}

// TestRateLimitService_HandleUpstreamError_CodexPlanGatedTextModelStillCoolsDown
// supplies a gated text model to OAuth handling. The response must remain
// handled and retain the dedicated subscription-gating model reason.
func TestRateLimitService_HandleUpstreamError_CodexPlanGatedTextModelStillCoolsDown(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAICodexPlanGatedOAuthProvider()

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusBadRequest, http.Header{}, []byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT provider."}`), "gpt-5.6-sol")).StopScheduling

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1, "非图片套餐门控模型应保留原有冷却")
	require.Equal(t, providercore.CodexPlanGatedModelReason, repo.modelRateLimitCalls[0].reason)
}

// TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelKeepsCooldownOnImagesEndpoint
// marks the actual dedicated Images endpoint. The same model denial must store
// an image-model cooldown with the subscription-gating reason.
func TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelKeepsCooldownOnImagesEndpoint(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAICodexPlanGatedOAuthProvider()

	handled := svc.ApplyUpstreamError(requeststate.WithOpenAIImagesEndpoint(context.Background()), provider, healthTestObservation(requeststate.WithOpenAIImagesEndpoint(context.Background()), http.StatusBadRequest, http.Header{}, []byte(`{"detail":"The 'gpt-image-2' model is not supported when using Codex with a ChatGPT provider."}`), "gpt-image-2")).StopScheduling

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1, "专用 Images 端点上的能力拒绝必须保留冷却")
	require.Equal(t, "gpt-image-2", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, providercore.CodexPlanGatedModelReason, repo.modelRateLimitCalls[0].reason)
}

// TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelSkipsCooldownOnIntentOnly
// supplies image-generation intent without the dedicated endpoint marker. The
// denial must remain handled while no image-model cooldown is stored.
func TestRateLimitService_HandleUpstreamError_CodexPlanGatedImageModelSkipsCooldownOnIntentOnly(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAICodexPlanGatedOAuthProvider()

	handled := svc.ApplyUpstreamError(requeststate.WithOpenAIImageGenerationIntent(context.Background()), provider, healthTestObservation(requeststate.WithOpenAIImageGenerationIntent(context.Background()), http.StatusBadRequest, http.Header{}, []byte(`{"detail":"The 'gpt-image-2' model is not supported when using Codex with a ChatGPT provider."}`), "gpt-image-2")).StopScheduling

	require.True(t, handled)
	require.Empty(t, repo.modelRateLimitCalls, "生图意图不等于专用 Images 入口")
}

// TestRateLimitService_HandleUpstreamError_ModelNotFoundImageModelStillCoolsDown
// observes explicit model-not-found for an image model. Its standard not-found
// cooldown must still be stored regardless of the text-endpoint gating
// exception.
func TestRateLimitService_HandleUpstreamError_ModelNotFoundImageModelStillCoolsDown(t *testing.T) {
	repo := &modelNotFoundProviderRepoStub{}
	svc := newModelHealthObserver(repo)
	provider := openAICodexPlanGatedOAuthProvider()

	handled := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"The model 'gpt-image-2' does not exist","code":"model_not_found"}}`), "gpt-image-2")).StopScheduling

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1, "图片模型的 404 model_not_found 仍应冷却")
	require.Equal(t, providercore.ModelNotFoundReason, repo.modelRateLimitCalls[0].reason)
}

// TestRateLimitService_HandleUpstreamError_403PreservesOriginalUpstreamMessage
// supplies a concrete forbidden message to OAuth health. Error state must
// retain that upstream diagnostic while avoiding the generic permission
// explanation.
func TestRateLimitService_HandleUpstreamError_403PreservesOriginalUpstreamMessage(t *testing.T) {
	repo := &unauthorizedHealthStore{}
	svc := newUnauthorizedObserver(repo, nil)
	provider := &providercore.Record{
		ID:       201,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
	}

	shouldDisable := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), 403, http.Header{}, []byte(`{"error":{"message":"workspace forbidden by policy","type":"invalid_request_error"}}`))).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "workspace forbidden by policy")
	require.NotContains(t, repo.lastErrorMsg, "provider may be suspended or lack permissions")
}

// TestRateLimitService_HandleUpstreamError_403FallsBackToRawBody supplies
// forbidden details without a message string. The stored error must retain
// access-denied and blocked-IP evidence from the body.
func TestRateLimitService_HandleUpstreamError_403FallsBackToRawBody(t *testing.T) {
	repo := &unauthorizedHealthStore{}
	svc := newUnauthorizedObserver(repo, nil)
	provider := &providercore.Record{
		ID:       202,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
	}

	shouldDisable := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), 403, http.Header{}, []byte(`{"error":{"type":"access_denied","details":{"reason":"ip_blocked"}}}`))).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, `"access_denied"`)
	require.Contains(t, repo.lastErrorMsg, `"ip_blocked"`)
	require.NotContains(t, repo.lastErrorMsg, "provider may be suspended or lack permissions")
}

// TestHandle529_EnabledFromDB_PausesProvider configures an enabled
// fifteen-minute overload cooldown through runtime settings. Health must write
// one provider pause whose deadline follows that duration.
func TestHandle529_EnabledFromDB_PausesProvider(t *testing.T) {
	providerRepo := &overloadProviderRepoStub{}
	settingRepo := newCooldownSettingsStore()
	data, err := json.Marshal(providercore.OverloadCooldownSettings{Enabled: true, CooldownMinutes: 15})
	require.NoError(t, err)
	settingRepo.data[providercore.SettingKeyOverloadCooldownSettings] = string(data)

	settingSvc := providercore.NewRuntimeSettings(settingRepo, errCooldownSettingMissing)
	svc := newOverloadObserver(providerRepo, providercore.HealthOptions{OverloadSettings: settingSvc.GetOverloadCooldownSettings})

	provider := &providercore.Record{ID: 42, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}
	before := time.Now()
	svc.Core.ApplyOverload(context.Background(), provider)

	require.Equal(t, 1, providerRepo.overloadCalls)
	require.Equal(t, int64(42), providerRepo.lastOverloadID)
	require.WithinDuration(t, before.Add(15*time.Minute), providerRepo.lastOverloadEnd, 2*time.Second)
}

// TestHandle529_DisabledFromDB_SkipsProvider disables overload handling despite
// a configured duration. The overload observer must skip provider pause writes
// when that enabled flag is false.
func TestHandle529_DisabledFromDB_SkipsProvider(t *testing.T) {
	providerRepo := &overloadProviderRepoStub{}
	settingRepo := newCooldownSettingsStore()
	data, err := json.Marshal(providercore.OverloadCooldownSettings{Enabled: false, CooldownMinutes: 15})
	require.NoError(t, err)
	settingRepo.data[providercore.SettingKeyOverloadCooldownSettings] = string(data)

	settingSvc := providercore.NewRuntimeSettings(settingRepo, errCooldownSettingMissing)
	svc := newOverloadObserver(providerRepo, providercore.HealthOptions{OverloadSettings: settingSvc.GetOverloadCooldownSettings})

	provider := &providercore.Record{ID: 42, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}
	svc.Core.ApplyOverload(context.Background(), provider)

	require.Equal(t, 0, providerRepo.overloadCalls, "should NOT pause when disabled")
}

// TestHandle529_NilSettingService_FallsBackToConfig omits runtime settings and
// supplies a configured twenty-minute duration. Health must store the overload
// deadline from that configuration value.
func TestHandle529_NilSettingService_FallsBackToConfig(t *testing.T) {
	providerRepo := &overloadProviderRepoStub{}
	minutes := 20
	svc := newOverloadObserver(providerRepo, providercore.HealthOptions{OverloadMinutes: minutes})
	// The settings reader is deliberately absent.

	provider := &providercore.Record{ID: 77, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}
	before := time.Now()
	svc.Core.ApplyOverload(context.Background(), provider)

	require.Equal(t, 1, providerRepo.overloadCalls)
	require.WithinDuration(t, before.Add(20*time.Minute), providerRepo.lastOverloadEnd, 2*time.Second)
}

// TestHandle529_NilSettingService_ZeroConfig_DefaultsTen omits both the runtime
// settings reader and positive overload configuration. Health must write one
// provider pause ending ten minutes after the observation.
func TestHandle529_NilSettingService_ZeroConfig_DefaultsTen(t *testing.T) {
	providerRepo := &overloadProviderRepoStub{}
	svc := newOverloadObserver(providerRepo, providercore.HealthOptions{})

	provider := &providercore.Record{ID: 88, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}
	before := time.Now()
	svc.Core.ApplyOverload(context.Background(), provider)

	require.Equal(t, 1, providerRepo.overloadCalls)
	require.WithinDuration(t, before.Add(10*time.Minute), providerRepo.lastOverloadEnd, 2*time.Second)
}

// TestHandle529_DBReadError_FallsBackToConfig injects a settings-read deadline
// error with a seven-minute configuration value. Health must still store one
// overload pause using the configured interval.
func TestHandle529_DBReadError_FallsBackToConfig(t *testing.T) {
	providerRepo := &overloadProviderRepoStub{}
	errRepo := &errSettingRepo{readErr: context.DeadlineExceeded}
	errRepo.data = make(map[string]string)

	minutes := 7
	settingSvc := providercore.NewRuntimeSettings(errRepo, errCooldownSettingMissing)
	svc := newOverloadObserver(providerRepo, providercore.HealthOptions{OverloadMinutes: minutes, OverloadSettings: settingSvc.GetOverloadCooldownSettings})

	provider := &providercore.Record{ID: 99, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}
	before := time.Now()
	svc.Core.ApplyOverload(context.Background(), provider)

	require.Equal(t, 1, providerRepo.overloadCalls)
	require.WithinDuration(t, before.Add(7*time.Minute), providerRepo.lastOverloadEnd, 2*time.Second)
}

// TestHandleUpstreamError_529RespectsProviderPolicies supplies overload to a
// pool and an excluding custom-code filter. Both decisions must leave provider
// overload and error writers unused.
func TestHandleUpstreamError_529RespectsProviderPolicies(t *testing.T) {
	tests := []struct {
		name        string         // health policy scenario label
		credentials map[string]any // supplied provider policy configuration
	}{
		{
			name:        "pool mode",
			credentials: map[string]any{"pool_mode": true},
		},
		{
			name: "custom code filter excludes 529",
			credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(429)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &overloadProviderRepoStub{}
			svc := newOverloadObserver(repo, providercore.HealthOptions{})
			provider := &providercore.Record{
				ID:          101,
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeAPIKey,
				Credentials: tt.credentials,
			}

			shouldDisable := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), 529, nil, []byte(`{"error":{"message":"overloaded"}}`))).StopScheduling

			require.False(t, shouldDisable)
			require.Zero(t, repo.overloadCalls)
			require.Zero(t, repo.errorCalls)
		})
	}
}

// TestHandleUpstreamError_529CustomCodeDisablesInsteadOfOverloadCooldown
// explicitly matches overload in custom policy. Handling must stop scheduling
// and write error state while leaving generic overload cooldown unused.
func TestHandleUpstreamError_529CustomCodeDisablesInsteadOfOverloadCooldown(t *testing.T) {
	repo := &overloadProviderRepoStub{}
	svc := newOverloadObserver(repo, providercore.HealthOptions{})
	provider := &providercore.Record{
		ID:       102,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(529)},
		},
	}

	shouldDisable := svc.ApplyUpstreamError(context.Background(), provider, healthTestObservation(context.Background(), 529, nil, []byte(`{"error":{"message":"overloaded"}}`))).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.errorCalls)
	require.Zero(t, repo.overloadCalls)
}

// TestUpdateSessionWindow_UsesResetHeader initializes a missing window from an
// observed Unix reset. Stored start and end must span five hours ending at that
// exact reset with the supplied status.
func TestUpdateSessionWindow_UsesResetHeader(t *testing.T) {
	// The reset header gives the window end as a Unix timestamp.
	// That value takes precedence over the hour-truncated estimate.
	resetUnix := time.Now().Add(3 * time.Hour).Unix()
	wantEnd := time.Unix(resetUnix, 0)
	wantStart := wantEnd.Add(-5 * time.Hour)

	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{ID: 42} // missing window requires initialization
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", resetUnix))

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(headers))

	if len(repo.sessionWindowCalls) != 1 {
		t.Fatalf("expected 1 UpdateSessionWindow call, got %d", len(repo.sessionWindowCalls))
	}

	call := repo.sessionWindowCalls[0]
	if call.ID != 42 {
		t.Errorf("expected provider ID 42, got %d", call.ID)
	}
	if call.End == nil || !call.End.Equal(wantEnd) {
		t.Errorf("expected window end %v, got %v", wantEnd, call.End)
	}
	if call.Start == nil || !call.Start.Equal(wantStart) {
		t.Errorf("expected window start %v, got %v", wantStart, call.Start)
	}
	if call.Status != "allowed" {
		t.Errorf("expected status 'allowed', got %q", call.Status)
	}
}

// TestUpdateSessionWindow_FallbackPredictionWhenNoResetHeader supplies status
// without a reset on a missing window. The stored estimate must begin at the
// current hour and end five hours later.
func TestUpdateSessionWindow_FallbackPredictionWhenNoResetHeader(t *testing.T) {
	// A missing reset header uses an hour-truncated estimate.
	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{ID: 10} // no existing window
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed_warning")
	// Omit anthropic-ratelimit-unified-5h-reset.

	// Calculate the expected window before invoking the service.
	now := time.Now()
	expectedStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	expectedEnd := expectedStart.Add(5 * time.Hour)

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(headers))

	if len(repo.sessionWindowCalls) != 1 {
		t.Fatalf("expected 1 UpdateSessionWindow call, got %d", len(repo.sessionWindowCalls))
	}

	call := repo.sessionWindowCalls[0]
	if call.End == nil {
		t.Fatal("expected window end to be set (fallback prediction)")
	}
	// The estimated window starts at the current hour and lasts five hours.

	if !call.End.Equal(expectedEnd) {
		t.Errorf("expected fallback end %v, got %v", expectedEnd, *call.End)
	}
	if call.Start == nil || !call.Start.Equal(expectedStart) {
		t.Errorf("expected fallback start %v, got %v", expectedStart, call.Start)
	}
}

// TestUpdateSessionWindow_CorrectsStalePrediction supplies a real reset
// differing from a stored estimated endpoint. Window persistence must replace
// that estimate with the exact observed end.
func TestUpdateSessionWindow_CorrectsStalePrediction(t *testing.T) {
	// A reset header can correct an existing estimated SessionWindowEnd.
	// Use different predicted and observed deadlines to exercise that update.
	staleEnd := time.Now().Add(2 * time.Hour)             // predicted deadline
	realResetUnix := time.Now().Add(4 * time.Hour).Unix() // observed reset deadline
	wantEnd := time.Unix(realResetUnix, 0)

	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{
		ID:               55,
		SessionWindowEnd: &staleEnd,
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", realResetUnix))

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(headers))

	if len(repo.sessionWindowCalls) != 1 {
		t.Fatalf("expected 1 UpdateSessionWindow call, got %d", len(repo.sessionWindowCalls))
	}

	call := repo.sessionWindowCalls[0]
	if call.End == nil || !call.End.Equal(wantEnd) {
		t.Errorf("expected corrected end %v, got %v", wantEnd, call.End)
	}
}

// TestUpdateSessionWindow_NoUpdateWhenHeaderMatchesStored supplies the same
// reset already stored on the record. The captured update must leave start and
// end nil while still carrying the observed status.
func TestUpdateSessionWindow_NoUpdateWhenHeaderMatchesStored(t *testing.T) {
	// Matching stored and observed reset times require no window timestamp update.
	futureUnix := time.Now().Add(3 * time.Hour).Unix()
	existingEnd := time.Unix(futureUnix, 0)

	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{
		ID:               77,
		SessionWindowEnd: &existingEnd,
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", futureUnix)) // same as stored

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(headers))

	if len(repo.sessionWindowCalls) != 1 {
		t.Fatalf("expected 1 UpdateSessionWindow call, got %d", len(repo.sessionWindowCalls))
	}

	call := repo.sessionWindowCalls[0]
	// Nil timestamps mean the stored window stays unchanged.
	if call.Start != nil || call.End != nil {
		t.Errorf("expected nil start/end (no window change needed), got start=%v end=%v", call.Start, call.End)
	}
	// The status still updates.
	if call.Status != "allowed" {
		t.Errorf("expected status 'allowed', got %q", call.Status)
	}
}

// TestUpdateSessionWindow_ClearsUtilizationOnWindowReset initializes a missing
// window with observed utilization. Extra writes must first clear prior
// utilization and then store the new measurement.
func TestUpdateSessionWindow_ClearsUtilizationOnWindowReset(t *testing.T) {
	// Initializing a window clears previous utilization before storing the observation.
	resetUnix := time.Now().Add(3 * time.Hour).Unix()

	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{ID: 33} // missing window requires initialization
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", resetUnix))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.15")

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(headers))

	// Expect one write to clear utilization followed by one to store the observation.
	if len(repo.updateExtraCalls) != 2 {
		t.Fatalf("expected 2 UpdateExtra calls, got %d", len(repo.updateExtraCalls))
	}

	// Clear utilization with a nil value.
	clearCall := repo.updateExtraCalls[0]
	if clearCall.Updates["session_window_utilization"] != nil {
		t.Errorf("expected utilization cleared to nil, got %v", clearCall.Updates["session_window_utilization"])
	}

	// Store the observed utilization.
	storeCall := repo.updateExtraCalls[1]
	if val, ok := storeCall.Updates["session_window_utilization"].(float64); !ok || val != 0.15 {
		t.Errorf("expected utilization stored as 0.15, got %v", storeCall.Updates["session_window_utilization"])
	}
}

// TestUpdateSessionWindow_NoClearUtilizationOnCorrection corrects a window
// estimate while retaining its accounting period. One Extra write must store
// the new utilization, with no preceding clearing patch.
func TestUpdateSessionWindow_NoClearUtilizationOnCorrection(t *testing.T) {
	// Correcting an estimate within the same window keeps utilization until the new observation is stored.
	staleEnd := time.Now().Add(2 * time.Hour)
	realResetUnix := time.Now().Add(4 * time.Hour).Unix()

	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{
		ID:               66,
		SessionWindowEnd: &staleEnd,
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", realResetUnix))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.30")

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(headers))

	// A single write stores utilization; no clearing write is needed.
	if len(repo.updateExtraCalls) != 1 {
		t.Fatalf("expected 1 UpdateExtra call (no clear), got %d", len(repo.updateExtraCalls))
	}

	if val, ok := repo.updateExtraCalls[0].Updates["session_window_utilization"].(float64); !ok || val != 0.30 {
		t.Errorf("expected utilization 0.30, got %v", repo.updateExtraCalls[0].Updates["session_window_utilization"])
	}
}

// TestUpdateSessionWindow_SamplesFable7dOiHeaders reuses an existing window
// while observing Fable-only weekly headers. One Extra patch must retain both
// passive utilization and its reset timestamp.
func TestUpdateSessionWindow_SamplesFable7dOiHeaders(t *testing.T) {
	// Passive sampling includes utilization and reset time for the Fable-only 7d_oi window.
	existingEnd := time.Now().Add(3 * time.Hour)
	resetOIUnix := time.Now().Add(80 * time.Hour).Unix()

	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{ID: 90, SessionWindowEnd: &existingEnd} // reuse the window without initialization
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-7d_oi-utilization", "0.87")
	headers.Set("anthropic-ratelimit-unified-7d_oi-reset", fmt.Sprintf("%d", resetOIUnix))

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(headers))

	if len(repo.updateExtraCalls) != 1 {
		t.Fatalf("expected 1 UpdateExtra call, got %d", len(repo.updateExtraCalls))
	}
	updates := repo.updateExtraCalls[0].Updates
	if val, ok := updates["passive_usage_7d_oi_utilization"].(float64); !ok || val != 0.87 {
		t.Errorf("expected passive_usage_7d_oi_utilization=0.87, got %v", updates["passive_usage_7d_oi_utilization"])
	}
	if val, ok := updates["passive_usage_7d_oi_reset"].(int64); !ok || val != resetOIUnix {
		t.Errorf("expected passive_usage_7d_oi_reset=%d, got %v", resetOIUnix, updates["passive_usage_7d_oi_reset"])
	}
}

// TestUpdateSessionWindow_ClearsFable7dOiOnWindowReset initializes a new
// five-hour window without fresh Fable measurements. Its clearing patch must
// contain nil values for both previous Fable utilization and reset.
func TestUpdateSessionWindow_ClearsFable7dOiOnWindowReset(t *testing.T) {
	// A 5h window reset clears passive 7d_oi observations along with 7d data.
	resetUnix := time.Now().Add(3 * time.Hour).Unix()

	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{ID: 91} // initialize a missing window
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", resetUnix))

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(headers))

	if len(repo.updateExtraCalls) != 1 {
		t.Fatalf("expected 1 UpdateExtra (clear) call, got %d", len(repo.updateExtraCalls))
	}
	clearUpdates := repo.updateExtraCalls[0].Updates
	for _, key := range []string{"passive_usage_7d_oi_utilization", "passive_usage_7d_oi_reset"} {
		if val, present := clearUpdates[key]; !present || val != nil {
			t.Errorf("expected %s cleared to nil on window reset, got present=%v val=%v", key, present, val)
		}
	}
}

// TestUpdateSessionWindow_NoStatusHeader supplies an empty header map to an
// existing provider. Session-window handling must produce no window write when
// no status observation is present.
func TestUpdateSessionWindow_NoStatusHeader(t *testing.T) {
	// Missing status headers leave the stored window unchanged.
	repo := &sessionWindowMockRepo{}
	svc := newSessionWindowService(repo)

	provider := &providercore.Record{ID: 1}

	svc.UpdateSessionWindow(context.Background(), provider, SessionWindowObservation(http.Header{}))

	if len(repo.sessionWindowCalls) != 0 {
		t.Errorf("expected no calls when status header absent, got %d", len(repo.sessionWindowCalls))
	}
}

// TestRateLimitService_HandleUpstreamError_OAuth401SetsTempUnschedulable checks
// refreshable Gemini and Antigravity authentication failures. Handling must
// request temporary suspension and token invalidation, with Antigravity also
// setting its force-refresh marker.
func TestRateLimitService_HandleUpstreamError_OAuth401SetsTempUnschedulable(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		repo := &unauthorizedHealthStore{}
		invalidator := &unauthorizedTokenRecorder{}
		service := newUnauthorizedObserver(repo, invalidator)
		provider := &providercore.Record{
			ID:       100,
			Platform: capability.PlatformGemini,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"refresh_token":              "rt-100",
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       401,
						"keywords":         []any{"unauthorized"},
						"duration_minutes": 30,
						"description":      "custom rule",
					},
				},
			},
		}

		shouldDisable := service.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

		require.True(t, shouldDisable)
		require.Equal(t, 0, repo.setErrorCalls)
		require.Equal(t, 1, repo.tempCalls)
		require.Len(t, invalidator.providers, 1)
	})

	t.Run("antigravity_401_sets_temp_unschedulable", func(t *testing.T) {
		repo := &unauthorizedHealthStore{}
		invalidator := &unauthorizedTokenRecorder{}
		service := newUnauthorizedObserver(repo, invalidator)
		provider := &providercore.Record{
			ID:       100,
			Platform: capability.PlatformAntigravity,
			Type:     capability.ProviderTypeOAuth,
			Status:   billing.StatusActive,
			Credentials: map[string]any{
				"access_token":  "expired-at",
				"refresh_token": "rt-100",
			},
		}

		shouldDisable := service.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

		require.True(t, shouldDisable)
		require.Equal(t, 0, repo.setErrorCalls, "Antigravity OAuth 401 must keep status=active so refresh worker can recover it")
		require.Equal(t, 1, repo.tempCalls)
		require.Equal(t, int64(100), repo.lastTempID)
		require.Contains(t, repo.lastTempReason, "invalid or expired credentials")
		require.Equal(t, 1, repo.updateExtraCalls)
		require.Equal(t, true, repo.lastExtraUpdates[providercore.AntigravityForceTokenRefreshExtraKey])
		require.Equal(t, "401_invalid", repo.lastExtraUpdates[providercore.AntigravityForceTokenRefreshReasonExtraKey])
		require.Equal(t, true, provider.Extra[providercore.AntigravityForceTokenRefreshExtraKey])
		require.Len(t, invalidator.providers, 1)
		require.Equal(t, int64(100), invalidator.providers[0].ID)
	})
}

// TestRateLimitService_HandleUpstreamError_SparkShadow401RedirectsToParent
// supplies a shadow that has no refresh token. Its parent must receive
// temporary suspension and cached-token invalidation while no permanent shadow
// error is written.
func TestRateLimitService_HandleUpstreamError_SparkShadow401RedirectsToParent(t *testing.T) {
	repo := &unauthorizedHealthStore{}
	repo.providersByID = map[int64]*providercore.Record{}
	invalidator := &unauthorizedTokenRecorder{}
	service := newUnauthorizedObserver(repo, invalidator)

	const parentID = int64(500)
	mother := &providercore.Record{
		ID:          parentID,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt-mother"},
	}
	repo.providersByID[parentID] = mother

	shadowParent := parentID
	shadow := &providercore.Record{
		ID:               501,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &shadowParent,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		// The shadow has no refresh_token of its own.
	}

	shouldDisable := service.ApplyUpstreamError(context.Background(), shadow, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "spark shadow must not be permanently disabled on a parent-token 401")
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, parentID, repo.lastTempID, "temp-unschedulable must target the credential owner (parent)")
	require.Len(t, invalidator.providers, 1)
	require.Equal(t, parentID, invalidator.providers[0].ID, "token cache invalidation must target the parent")
}

// TestRateLimitService_HandleUpstreamError_OAuth401InvalidatorError injects
// failure in cached-token invalidation for a refreshable OAuth record. Handling
// must still suspend temporarily and leave credential persistence unused.
func TestRateLimitService_HandleUpstreamError_OAuth401InvalidatorError(t *testing.T) {
	repo := &unauthorizedHealthStore{}
	invalidator := &unauthorizedTokenRecorder{err: errors.New("boom")}
	service := newUnauthorizedObserver(repo, invalidator)
	provider := &providercore.Record{
		ID:       101,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt-101",
		},
	}

	shouldDisable := service.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, 0, repo.updateCredentialsCalls)
	require.Len(t, invalidator.providers, 1)
}

// TestRateLimitService_HandleUpstreamError_NonOAuth401 supplies API-key
// authentication failure with an available token invalidator. Handling must
// write provider error state and leave OAuth token invalidation unused.
func TestRateLimitService_HandleUpstreamError_NonOAuth401(t *testing.T) {
	repo := &unauthorizedHealthStore{}
	invalidator := &unauthorizedTokenRecorder{}
	service := newUnauthorizedObserver(repo, invalidator)
	provider := &providercore.Record{
		ID:       102,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
	}

	shouldDisable := service.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Empty(t, invalidator.providers)
}

// TestRateLimitService_HandleUpstreamError_OAuth401DoesNotOverwriteCredentials
// supplies a refreshable OpenAI request snapshot. Health must set temporary
// suspension while credential and force-refresh Extra writers remain unused.
func TestRateLimitService_HandleUpstreamError_OAuth401DoesNotOverwriteCredentials(t *testing.T) {
	repo := &unauthorizedHealthStore{}
	service := newUnauthorizedObserver(repo, nil)
	provider := &providercore.Record{
		ID:       103,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "token",
			"refresh_token": "rt-103",
		},
	}

	shouldDisable := service.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.updateCredentialsCalls, "401 handler must not write credentials back from the request-start snapshot")
	require.Equal(t, 0, repo.updateExtraCalls, "OpenAI 401 must not set Antigravity force-refresh marker")
	require.Equal(t, 1, repo.tempCalls, "401 handler should still set temp-unschedulable cooldown")
	require.Nil(t, repo.lastCredentials, "no credentials should have been persisted")
}

// TestRateLimitService_HandleUpstreamError_OAuth401NoRefreshTokenSetsError
// checks absent and blank refresh tokens across OAuth platforms. Those failures
// must write permanent error state instead of a recoverable temporary cooldown.
func TestRateLimitService_HandleUpstreamError_OAuth401NoRefreshTokenSetsError(t *testing.T) {
	t.Run("openai_no_refresh_token", func(t *testing.T) {
		repo := &unauthorizedHealthStore{}
		invalidator := &unauthorizedTokenRecorder{}
		service := newUnauthorizedObserver(repo, invalidator)
		provider := &providercore.Record{
			ID:       2881,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "expired-at",
			},
		}

		shouldDisable := service.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls)
		require.Equal(t, 0, repo.tempCalls)
		require.Equal(t, 0, repo.updateCredentialsCalls)
		require.Contains(t, repo.lastErrorMsg, "refresh_token missing")
		require.Len(t, invalidator.providers, 1)
	})

	t.Run("blank_refresh_token_treated_as_missing", func(t *testing.T) {
		repo := &unauthorizedHealthStore{}
		service := newUnauthorizedObserver(repo, nil)
		provider := &providercore.Record{
			ID:       2882,
			Platform: capability.PlatformGemini,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token":  "expired-at",
				"refresh_token": "   ",
			},
		}

		shouldDisable := service.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls)
		require.Equal(t, 0, repo.tempCalls)
	})

	t.Run("antigravity_no_refresh_token_sets_error", func(t *testing.T) {
		repo := &unauthorizedHealthStore{}
		invalidator := &unauthorizedTokenRecorder{}
		service := newUnauthorizedObserver(repo, invalidator)
		provider := &providercore.Record{
			ID:       2883,
			Platform: capability.PlatformAntigravity,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"access_token": "expired-at",
			},
		}

		shouldDisable := service.ApplyUpstreamError(context.Background(), provider, HealthObservation{Status: 401, Body: []byte("unauthorized")}).StopScheduling

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls, "Antigravity OAuth without refresh_token cannot self-recover")
		require.Equal(t, 0, repo.tempCalls)
		require.Contains(t, repo.lastErrorMsg, "refresh_token missing")
		require.Len(t, invalidator.providers, 1)
	})
}

// SetTempUnschedulable counts a provider temporary-rule write for policy
// assertions.
func (r *errorPolicyRepoStub) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempCalls++
	return nil
}

// SetError counts a provider error-state write for explicit policy assertions.
func (r *errorPolicyRepoStub) SetError(context.Context, int64, string) error {
	r.setErrCalls++
	return nil
}

// SetModelRateLimit retains the provider ID receiving a model cooldown write.
func (r *errorPolicyRepoStub) SetModelRateLimit(_ context.Context, id int64, _ string, _ time.Time, _ ...string) error {
	r.modelRateLimitCalls = append(r.modelRateLimitCalls, id)
	return nil
}

// newErrorPolicyObserver binds captured writes to production error-policy
// health rules.
func newErrorPolicyObserver(repo *errorPolicyRepoStub) *UpstreamHealth {
	return &UpstreamHealth{Core: providercore.NewHealthService(repo, nil, providercore.HealthOptions{})}
}

// SetTempUnschedulable counts provider-wide writes to detect unwanted
// model-scope widening.
func (r *modelNotFoundProviderRepoStub) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.tempCalls++
	return nil
}

// SetModelRateLimit captures model scope, deadline, and first optional reason,
// then returns the configured error. Missing reasons leave empty text; failed
// storage remains captured so tests can inspect attempted scope.
func (r *modelNotFoundProviderRepoStub) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := modelNotFoundRateLimitCall{
		providerID: id,
		scope:      scope,
		resetAt:    resetAt,
	}
	if len(reason) > 0 {
		call.reason = reason[0]
	}
	r.modelRateLimitCalls = append(r.modelRateLimitCalls, call)
	return r.modelRateLimitErr
}

// openAIModelNotFoundTempProvider creates an active API-key record with a
// ten-minute 404 keyword rule. Its fresh credentials allow tests to change pool
// and custom-code choices while retaining that temporary-rule fixture.
func openAIModelNotFoundTempProvider() *providercore.Record {
	return &providercore.Record{
		ID:          101,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusNotFound),
					"keywords":         []any{"not found"},
					"duration_minutes": float64(10),
				},
			},
		},
	}
}

// openAICodexPlanGatedOAuthProvider creates an active schedulable OpenAI OAuth
// record for model gating tests.
func openAICodexPlanGatedOAuthProvider() *providercore.Record {
	return &providercore.Record{
		ID:          202,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{},
	}
}

// newModelHealthObserver binds production health, model classification, and
// rate-limit parsing to the fixture. Image-model recognition uses the gateway
// media predicate, keeping endpoint-intent assertions on the same model
// classification path.
func newModelHealthObserver(repo *modelNotFoundProviderRepoStub) *UpstreamHealth {
	core := providercore.NewHealthService(repo, nil, providercore.HealthOptions{})
	models := &ModelHealth{Health: core, IsImageModel: media.IsGPTImageGenerationModel}
	return &UpstreamHealth{Core: core, Models: models, Limits: &RateLimitObserver{Health: core}}
}

// healthTestObservation binds response data to the first optional model and
// request-state endpoint and thinking fields. ModelProvided records argument
// presence even for an empty name; EffectiveModel uses its trimmed value.
func healthTestObservation(ctx context.Context, status int, headers http.Header, body []byte, models ...string) HealthObservation {
	input := HealthObservation{Status: status, Headers: headers, Body: body, ModelProvided: len(models) > 0, ImagesEndpoint: requeststate.OpenAIImagesEndpointFromContext(ctx)}
	if len(models) > 0 {
		input.Model = models[0]
		input.EffectiveModel = strings.TrimSpace(models[0])
	}
	if thinking, ok := requeststate.ThinkingEnabledFromContext(ctx); ok {
		input.Thinking = &thinking
	}
	return input
}

// SetError counts error-state writes separately from captured overload pauses.
func (r *overloadProviderRepoStub) SetError(_ context.Context, _ int64, _ string) error {
	r.errorCalls++
	return nil
}

// SetOverloaded counts the pause and captures its provider ID and deadline.
func (r *overloadProviderRepoStub) SetOverloaded(_ context.Context, id int64, until time.Time) error {
	r.overloadCalls++
	r.lastOverloadID = id
	r.lastOverloadEnd = until
	return nil
}

// newOverloadObserver supplies production overload rules with the configured
// store and options.
func newOverloadObserver(repo providercore.HealthStore, options providercore.HealthOptions) *UpstreamHealth {
	return &UpstreamHealth{Core: providercore.NewHealthService(repo, nil, options)}
}

// GetValue returns empty setting text and the configured read error. It ignores
// the requested key, allowing overload tests to inspect configuration handling
// after that injected read result.
func (s *errSettingRepo) GetValue(context.Context, string) (string, error) { return "", s.readErr }

// newSessionWindowService binds production session handling and recovery to the
// same capture repository. Window-limit clearing delegates to RecoveryService,
// letting tests inspect both observation writes and recovery calls.
func newSessionWindowService(repo *sessionWindowMockRepo) *providercore.HealthService {
	recovery := providercore.NewRecoveryService(repo, nil, providercore.RecoveryOptions{})
	return providercore.NewHealthService(repo, nil, providercore.HealthOptions{SessionWindows: repo, ClearWindowRateLimit: recovery.ClearRateLimit})
}

// newUnauthorizedObserver binds production authentication health to the fixture
// store and optional invalidator. A nil recorder leaves token invalidation
// unset, while the store also supplies session-window write detection.
func newUnauthorizedObserver(repo *unauthorizedHealthStore, invalidator *unauthorizedTokenRecorder) *UpstreamHealth {
	options := providercore.HealthOptions{SessionWindows: repo}
	if invalidator != nil {
		options.InvalidateUnauthorizedToken = invalidator.InvalidateToken
	}
	return &UpstreamHealth{Core: providercore.NewHealthService(repo, nil, options)}
}

// GetByID returns a CloneRecord result for the requested credential owner.
// Missing map entries return nil without an error, allowing the health core to
// handle an absent parent record.
func (s *unauthorizedHealthStore) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	return providercore.CloneRecord(s.providersByID[id]), nil
}

// SetError captures the requested provider ID and authentication diagnostic and
// counts the write.
func (s *unauthorizedHealthStore) SetError(_ context.Context, id int64, message string) error {
	s.setErrorCalls++
	s.lastErrorID = id
	s.lastErrorMsg = message
	return nil
}

// SetTempUnschedulable captures provider identity, cooldown deadline, and
// reason and counts the write.
func (s *unauthorizedHealthStore) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	s.tempCalls++
	s.lastTempID = id
	s.lastTempUntil = until
	s.lastTempReason = reason
	return nil
}

// UpdateExtra shallow-copies the supplied Extra patch and counts its write.
// Nested values remain shared, and the fixture captures the latest patch
// without merging a stored record.
func (s *unauthorizedHealthStore) UpdateExtra(_ context.Context, _ int64, fields map[string]any) error {
	s.updateExtraCalls++
	s.lastExtraUpdates = maps.Clone(fields)
	return nil
}

// UpdateCredentials shallow-copies the supplied credential patch and counts the
// write. Nested values remain shared, letting assertions detect an unexpected
// authentication-state credential update.
func (s *unauthorizedHealthStore) UpdateCredentials(_ context.Context, _ int64, fields map[string]any) error {
	s.updateCredentialsCalls++
	s.lastCredentials = maps.Clone(fields)
	return nil
}

// UpdateSessionWindow panics when authentication tests unexpectedly reach
// session-window persistence.
func (*unauthorizedHealthStore) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	panic("unexpected window update")
}

// InvalidateToken retains the supplied provider pointer and returns the
// configured invalidation error. Capture happens even on error, so tests can
// inspect the credential owner requested by recovery.
func (s *unauthorizedTokenRecorder) InvalidateToken(_ context.Context, value *providercore.Record) error {
	s.providers = append(s.providers, value)
	return s.err
}

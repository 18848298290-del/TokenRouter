package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	settingstestkit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

const (
	// An invalid Responses subpath can return HTML 403 before reaching the OpenAI API (#5334).
	openAI403HTMLBody = "<!DOCTYPE html>\n<html><head><title>403 Forbidden</title></head>" +
		"<body><h1>403 Forbidden</h1></body></html>"

	teamLinkedDeactivatedBody = `{"detail":{"code":"deactivated_workspace","message":"This workspace has been deactivated."}}`

	grokQuotaSnapshotExtraKey        = "grok_usage_snapshot"
	grokRateLimitFallbackCooldown    = 2 * time.Minute
	grokRateLimitRepeatCooldown      = 10 * time.Minute
	grokRateLimitSustainedCooldown   = 30 * time.Minute
	grokRateLimitMaxAdaptiveCooldown = time.Hour
	grokRateLimitBackoffQuietPeriod  = time.Hour
	grokSpendingLimitProbeCooldown   = 10 * time.Minute
)

// dbFallbackRepoStub returns a configured database record from GetByID on a cache miss.
type dbFallbackRepoStub struct {
	gatewaytestkit.ErrorPolicyStore

	dbProvider *gatewayprovider.ExecutionProvider // Returned by GetByID when non-nil.
}

// countingOpenAI403CounterCache records consecutive-403 counter updates.
type countingOpenAI403CounterCache struct {
	gatewaytestkit.ForbiddenCounter

	increments int
}

type openAI403TestHarness struct {
	svc *provideradapter.UpstreamHealth

	repo     *gatewaytestkit.HealthStoreRecorder
	counter  *countingOpenAI403CounterCache
	blocker  *gatewaytestkit.RuntimeBlockRecorder
	provider *gatewayprovider.ExecutionProvider
}

type anthropicWindowLimitRepo struct {
	gatewaytestkit.HealthStoreBase
	rateLimitCalls          int
	tempUnschedCalls        int
	lastRateLimitReset      time.Time
	modelRateLimitCalls     int
	lastModelRateLimitScope string
	lastModelRateLimitReset time.Time
	sessionWindowCalls      int
	lastWindowStart         *time.Time
	lastWindowEnd           *time.Time
	lastWindowStatus        string
	lastExtraUpdates        map[string]any
}

type teamLinkedProviderRepoStub struct {
	gatewaytestkit.HealthStoreBase
	teamProviders []gatewayprovider.ExecutionProvider
	listErr       error
	listCalls     int
	setErrorIDs   []int64
	setErrorMsgs  map[int64]string
	failSetError  map[int64]error
}

type fableSchedulingThresholdRepoStub struct {
	gatewaytestkit.HealthStoreRecorder

	modelCalls      int
	lastModelScope  string
	lastModelReset  time.Time
	lastModelReason string
}

type grokQuotaProviderRepo struct {
	gatewaytestkit.HealthStoreBase
	providersByID         map[int64]*gatewayprovider.ExecutionProvider
	updates               map[int64]map[string]any
	updateCalls           int
	rateLimitedCalls      int
	lastRateLimitedID     int64
	lastRateLimitResetAt  time.Time
	tempUnschedCalls      int
	lastTempUnschedID     int64
	lastTempUnschedUntil  time.Time
	lastTempUnschedReason string
	recoveryClearCalls    int
	recoveryObservedAt    time.Time
	recoveryObservedReset time.Time
	recoveryClearResult   bool
}

// grokPoolPolicyProviderRepo records provider-state writes from Grok pool error policy.
type grokPoolPolicyProviderRepo struct {
	*grokQuotaProviderRepo
	setErrorCalls            int
	overloadedCalls          int
	modelRateLimitCalls      int
	lastModelRateLimitScope  string
	lastModelRateLimitReason string
}

type grokHealthTestClock struct{ nanos atomic.Int64 }

func TestHandle403_OtherCNProviderWithKimiConcurrencyMessageUsesNormalPolicy(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	counter := &gatewaytestkit.ForbiddenCounter{Counts: []int64{providercore.OpenAI403DisableThresholdDefault}}
	blocker := &gatewaytestkit.RuntimeBlockRecorder{}
	service := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{ForbiddenCounter: counter, Block: func(v *providercore.Record, until time.Time, reason string) {
		blocker.BlockProviderScheduling(gatewayprovider.NewExecutionProvider(v), until, reason)
	}}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 405, Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey}}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."}}`), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.SetErrorCalls, "non-Kimi CN provider must retain the normal permanent-error policy")
	require.Equal(t, 0, repo.TempCalls)
	require.Empty(t, counter.Counts, "normal CN 403 policy must consume the counter result")
	require.Equal(t, []string{"auth_error"}, blocker.Reasons, "the Kimi-specific runtime block must not apply")
}

func TestHandle403_CNProviderConcurrencyLimitAlwaysUsesTemporaryCooldown(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	counter := &gatewaytestkit.ForbiddenCounter{Counts: []int64{providercore.OpenAI403DisableThresholdDefault}}
	blocker := &gatewaytestkit.RuntimeBlockRecorder{}
	service := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{ForbiddenCounter: counter, Block: func(v *providercore.Record, until time.Time, reason string) {
		blocker.BlockProviderScheduling(gatewayprovider.NewExecutionProvider(v), until, reason)
	}}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 403, Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey}}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."}}`), nil)).StopScheduling

	require.True(t, shouldDisable, "the request must still fail over to another provider")
	require.Equal(t, 0, repo.SetErrorCalls)
	require.Equal(t, 1, repo.TempCalls)
	require.Contains(t, repo.LastTempReason, providercore.CNConcurrencyLimitReason)
	require.Equal(t, []int64{providercore.OpenAI403DisableThresholdDefault}, counter.Counts, "transient concurrency 403 must bypass the permanent-error counter")
	require.Len(t, blocker.Providers, 1)
	require.Equal(t, providercore.CNConcurrencyLimitReason, blocker.Reasons[0])
	require.True(t, blocker.Until[0].After(time.Now()))
}

func TestHandle403_KimiConcurrencyLimitRepositoryFailureKeepsRuntimeBlock(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{TempErr: errors.New("repository unavailable")}
	counter := &gatewaytestkit.ForbiddenCounter{Counts: []int64{providercore.OpenAI403DisableThresholdDefault}}
	blocker := &gatewaytestkit.RuntimeBlockRecorder{}
	service := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{ForbiddenCounter: counter, Block: func(v *providercore.Record, until time.Time, reason string) {
		blocker.BlockProviderScheduling(gatewayprovider.NewExecutionProvider(v), until, reason)
	}}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 406, Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey}}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."}}`), nil)).StopScheduling

	require.True(t, shouldDisable, "the current request must fail over even when persistence fails")
	require.Equal(t, 1, repo.TempCalls, "the temporary cooldown should still be persisted when possible")
	require.Equal(t, 0, repo.SetErrorCalls, "persistence failure must not fall back to permanent provider error")
	require.Equal(t, []int64{providercore.OpenAI403DisableThresholdDefault}, counter.Counts, "persistence failure must not enter the permanent-error counter path")
	require.Len(t, blocker.Providers, 1, "the in-memory runtime block must survive repository failure")
	// Compare business fields and routes after excluding clock functions from the assertion.
	expectedProvider, observedProvider := *provider, *blocker.Providers[0]
	expectedProvider.Record.Now, observedProvider.Record.Now = nil, nil
	expectedProvider.Record.LoadLocation, observedProvider.Record.LoadLocation = nil, nil
	require.Equal(t, expectedProvider, observedProvider)
	require.Equal(t, providercore.CNConcurrencyLimitReason, blocker.Reasons[0])
	require.True(t, blocker.Until[0].After(time.Now()))
}

func TestHandle403_CNProviderNearMatchRetainsNormalPermanentErrorPolicy(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	counter := &gatewaytestkit.ForbiddenCounter{Counts: []int64{providercore.OpenAI403DisableThresholdDefault}}
	service := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{ForbiddenCounter: counter}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 404, Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey}}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"You've reached your concurrent request limit. Please contact support."}}`), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.SetErrorCalls, "non-exact 403 must retain existing permission/auth protection")
	require.Equal(t, 0, repo.TempCalls)
}

func (r *dbFallbackRepoStub) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	if r.dbProvider != nil && r.dbProvider.Record.ID == id {
		return r.dbProvider, nil
	}
	return nil, nil // not found, no error
}

func TestCheckErrorPolicy_401_DBFallback_Escalates(t *testing.T) {
	// The cached record lacks the temporary reason, but the database records an earlier 401.
	// A second 401 escalates the non-Antigravity provider to a permanent error.
	// Antigravity follows its own error policy.
	t.Run("gemini_escalates", func(t *testing.T) {
		repo := &dbFallbackRepoStub{
			dbProvider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 20,
					TempUnschedulableReason: `{"status_code":401,"until_unix":1735689600}`,
				},
			},
		}
		svc := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{}, nil)

		provider := &gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 20,
				Type:                    capability.ProviderTypeOAuth,
				Platform:                capability.PlatformGemini,
				TempUnschedulableReason: "",
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
		}

		result := svc.CheckErrorPolicy(context.Background(), gatewayprovider.ExecutionRecord(provider), gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusUnauthorized, nil, []byte(`unauthorized`), nil))
		require.Equal(t, providercore.ErrorPolicyNone, result, "gemini 401 with DB fallback showing previous 401 should escalate")
	})

	t.Run("antigravity_stays_temp", func(t *testing.T) {
		repo := &dbFallbackRepoStub{
			dbProvider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 20,
					TempUnschedulableReason: `{"status_code":401,"until_unix":1735689600}`,
				},
			},
		}
		svc := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{}, nil)

		provider := &gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 20,
				Type:                    capability.ProviderTypeOAuth,
				Platform:                capability.PlatformAntigravity,
				TempUnschedulableReason: "",
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
		}

		result := svc.CheckErrorPolicy(context.Background(), gatewayprovider.ExecutionRecord(provider), gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusUnauthorized, nil, []byte(`unauthorized`), nil))
		require.Equal(t, providercore.ErrorPolicyTempUnscheduled, result, "antigravity 401 skips escalation, stays temp-unscheduled")
	})
}

func TestCheckErrorPolicy_401_DBFallback_NoDBRecord_FirstHit(t *testing.T) {
	// With no earlier 401 in either source, the first hit temporarily pauses scheduling.
	repo := &dbFallbackRepoStub{
		dbProvider: &gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 21,
				TempUnschedulableReason: "",
			}, // DB also empty
		},
	}
	svc := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 21,
			Type:                    capability.ProviderTypeOAuth,
			Platform:                capability.PlatformAntigravity,
			TempUnschedulableReason: "",
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
	}

	result := svc.CheckErrorPolicy(context.Background(), gatewayprovider.ExecutionRecord(provider), gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusUnauthorized, nil, []byte(`unauthorized`), nil))
	require.Equal(t, providercore.ErrorPolicyTempUnscheduled, result, "401 first hit with no DB record should temp-unschedule")
}

func TestCheckErrorPolicy_401_DBFallback_DBError_FirstHit(t *testing.T) {
	// A missing cached reason and absent database record are treated as the first hit.
	repo := &dbFallbackRepoStub{
		dbProvider: nil, // GetByID returns nil, nil
	}
	svc := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 22,
			Type:                    capability.ProviderTypeOAuth,
			Platform:                capability.PlatformAntigravity,
			TempUnschedulableReason: "",
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
	}

	result := svc.CheckErrorPolicy(context.Background(), gatewayprovider.ExecutionRecord(provider), gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusUnauthorized, nil, []byte(`unauthorized`), nil))
	require.Equal(t, providercore.ErrorPolicyTempUnscheduled, result, "401 first hit with DB not found should temp-unschedule")
}

func TestRateLimitService_HandleUpstreamError_OpenAIOAuth403UsesTempUnschedulable(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	counter := &gatewaytestkit.ForbiddenCounter{Counts: []int64{1}}
	settingRepo := settingstestkit.NewMemory()
	data, err := json.Marshal(providercore.OpenAI403CooldownSettings{
		Enabled:                 true,
		CooldownMinutes:         7,
		ErrorOnThresholdEnabled: true,
		ThresholdCount:          providercore.OpenAI403DisableThresholdDefault,
		ThresholdWindowMinutes:  providercore.OpenAI403CounterWindowMinutesDefault,
	})
	require.NoError(t, err)
	settingRepo.Data[providercore.SettingKeyOpenAI403CooldownSettings] = string(data)
	service := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{ForbiddenCounter: counter},

		newExecutionReadersFixture(settingRepo, &config.Config{}))
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 104,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
		},
	}

	before := time.Now()
	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"temporary forbidden"}}`), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.SetErrorCalls)
	require.Equal(t, 1, repo.TempCalls)
	require.WithinDuration(t, before.Add(7*time.Minute), repo.LastTempUntil, 2*time.Second)
	require.Contains(t, repo.LastTempReason, "temporary forbidden")
	require.Contains(t, repo.LastTempReason, "(1/3)")
}

func TestRateLimitService_HandleUpstreamError_OpenAIOAuth403DisabledUsesSetError(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	counter := &gatewaytestkit.ForbiddenCounter{Counts: []int64{1}}
	settingRepo := settingstestkit.NewMemory()
	data, err := json.Marshal(providercore.OpenAI403CooldownSettings{Enabled: false, CooldownMinutes: 7})
	require.NoError(t, err)
	settingRepo.Data[providercore.SettingKeyOpenAI403CooldownSettings] = string(data)
	service := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{ForbiddenCounter: counter},

		newExecutionReadersFixture(settingRepo, &config.Config{}))
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 105,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
		},
	}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"temporary forbidden"}}`), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.SetErrorCalls)
	require.Equal(t, 0, repo.TempCalls)
	require.Contains(t, repo.LastErrorMsg, "temporary forbidden")
}

func TestRateLimitService_HandleUpstreamError_OpenAIOAuth403WithoutCounterUsesSetError(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	service := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 106,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
		},
	}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"temporary forbidden"}}`), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.SetErrorCalls)
	require.Equal(t, 0, repo.TempCalls)
	require.Contains(t, repo.LastErrorMsg, "temporary forbidden")
}

func TestRateLimitService_HandleUpstreamError_NonOpenAIOAuth403UsesSetError(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	service := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 107,
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
		},
	}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"forbidden"}}`), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.SetErrorCalls)
	require.Equal(t, 0, repo.TempCalls)
	require.Contains(t, repo.LastErrorMsg, "Access forbidden (403)")
}

func (s *countingOpenAI403CounterCache) IncrementOpenAI403Count(ctx context.Context, providerID int64, window int) (int64, error) {
	s.increments++
	return s.ForbiddenCounter.IncrementOpenAI403Count(ctx, providerID, window)
}

func newOpenAI403TestHarness(t *testing.T, providerID int64, counts ...int64) *openAI403TestHarness {
	t.Helper()
	repo := &gatewaytestkit.HealthStoreRecorder{}
	counter := &countingOpenAI403CounterCache{ForbiddenCounter: gatewaytestkit.ForbiddenCounter{Counts: counts}}
	blocker := &gatewaytestkit.RuntimeBlockRecorder{}
	svc := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{ForbiddenCounter: counter, Block: func(v *providercore.Record, until time.Time, reason string) {
		blocker.BlockProviderScheduling(gatewayprovider.NewExecutionProvider(v), until, reason)
	}}, nil)

	return &openAI403TestHarness{
		svc:      svc,
		repo:     repo,
		counter:  counter,
		blocker:  blocker,
		provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: providerID, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}},
	}
}

func (h *openAI403TestHarness) handle(body string) bool {
	return gatewayprovider.ApplyExecutionHealth(context.Background(), h.svc, h.provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(body), nil)).StopScheduling
}

func (h *openAI403TestHarness) requireNoProviderPenalty(t *testing.T) {
	t.Helper()
	require.Equal(t, 0, h.repo.SetErrorCalls, "端点级 403 不得永久禁用提供商")
	require.Equal(t, 0, h.repo.TempCalls, "端点级 403 不得把提供商设为临时不可调度")
	require.Empty(t, h.blocker.Providers, "端点级 403 不得触发调度阻断通知")
	require.Equal(t, 0, h.counter.increments, "端点级 403 不得递增连续 403 计数")
}

// TestHandleUpstreamError_OpenAIHTML403DoesNotPenalizeProvider covers common HTML response forms.
func TestHandleUpstreamError_OpenAIHTML403DoesNotPenalizeProvider(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"doctype_prefixed", openAI403HTMLBody},
		{"bare_html_tag", "<html><body>403 Forbidden</body></html>"},
		{"leading_whitespace_and_uppercase", "\n\t  <!DOCTYPE HTML><html><body>Forbidden</body></html>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpenAI403TestHarness(t, 501, 1)

			shouldDisable := h.handle(tc.body)

			require.False(t, shouldDisable, "HTML 403 不得判定提供商应下线")
			h.requireNoProviderPenalty(t)
		})
	}
}

func TestHandleUpstreamErrorCNProviderHTML403DoesNotPenalizeProvider(t *testing.T) {
	for _, platform := range []string{capability.PlatformKimi, capability.PlatformZhipu, capability.PlatformDeepseek} {
		t.Run(platform, func(t *testing.T) {
			h := newOpenAI403TestHarness(t, 507, 1)
			h.provider.Record.Platform = platform
			h.provider.Record.Type = capability.ProviderTypeAPIKey

			require.False(t, h.handle(openAI403HTMLBody))
			h.requireNoProviderPenalty(t)
		})
	}
}

func TestHandleUpstreamErrorCNProviderStructured403UsesCumulativeCooldown(t *testing.T) {
	for _, platform := range []string{capability.PlatformKimi, capability.PlatformZhipu, capability.PlatformDeepseek} {
		t.Run(platform, func(t *testing.T) {
			h := newOpenAI403TestHarness(t, 508, 1)
			h.provider.Record.Platform = platform
			h.provider.Record.Type = capability.ProviderTypeAPIKey

			require.True(t, h.handle(`{"error":{"message":"forbidden"}}`))
			require.Equal(t, 1, h.counter.increments)
			require.Equal(t, 1, h.repo.TempCalls)
			require.Zero(t, h.repo.SetErrorCalls)
			require.Contains(t, h.repo.LastTempReason, "(1/3)")
		})
	}
}

// TestHandleUpstreamError_OpenAIHTML403RepeatedNeverEscalates checks that repeated HTML errors do not advance the disable counter.
func TestHandleUpstreamError_OpenAIHTML403RepeatedNeverEscalates(t *testing.T) {
	h := newOpenAI403TestHarness(t, 502, 1, 2, 3, 4, 5)

	for i := range providercore.OpenAI403DisableThresholdDefault + 2 {
		require.False(t, h.handle(openAI403HTMLBody), "第 %d 次 HTML 403 仍不得判定提供商应下线", i+1)
	}

	h.requireNoProviderPenalty(t)
}

// TestHandleUpstreamError_OpenAIStructured403StillPenalizes checks penalties for structured provider failures.
func TestHandleUpstreamError_OpenAIStructured403StillPenalizes(t *testing.T) {
	t.Run("first_hit_temp_unschedulable", func(t *testing.T) {
		h := newOpenAI403TestHarness(t, 503, 1)

		require.True(t, h.handle(`{"error":{"message":"Your provider is not authorized"}}`))
		require.Equal(t, 1, h.counter.increments)
		require.Equal(t, 1, h.repo.TempCalls)
		require.Equal(t, 0, h.repo.SetErrorCalls)
		require.Contains(t, h.repo.LastTempReason, "Your provider is not authorized")
		require.Len(t, h.blocker.Providers, 1)
	})

	t.Run("threshold_disables", func(t *testing.T) {
		h := newOpenAI403TestHarness(t, 504, int64(providercore.OpenAI403DisableThresholdDefault))

		require.True(t, h.handle(`{"error":{"message":"workspace forbidden by policy"}}`))
		require.Equal(t, 1, h.repo.SetErrorCalls)
		require.Contains(t, h.repo.LastErrorMsg, "workspace forbidden by policy")
	})

	t.Run("plain_text_body_unchanged", func(t *testing.T) {
		h := newOpenAI403TestHarness(t, 505, 1)

		require.True(t, h.handle("Forbidden"))
		require.Equal(t, 1, h.repo.TempCalls)
	})
}

// TestHandleUpstreamError_HTML403OnOtherPlatformsUnchanged checks that the HTML exemption is OpenAI-specific.
func TestHandleUpstreamError_HTML403OnOtherPlatformsUnchanged(t *testing.T) {
	for _, platform := range []string{capability.PlatformAnthropic, capability.PlatformGemini} {
		t.Run(platform, func(t *testing.T) {
			repo := &gatewaytestkit.HealthStoreRecorder{}
			svc := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{}, nil)

			provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 506, Platform: platform, Type: capability.ProviderTypeAPIKey}}

			shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), svc, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(openAI403HTMLBody), nil)).StopScheduling

			require.True(t, shouldDisable)
			require.Equal(t, 1, repo.SetErrorCalls, "其他平台保持原有 SetError 行为")
		})
	}
}

func TestRateLimitService_HandleUpstreamError_OpenAI403FirstHitTempUnschedulable(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	counter := &gatewaytestkit.ForbiddenCounter{Counts: []int64{1}}
	blocker := &gatewaytestkit.RuntimeBlockRecorder{}
	service := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{ForbiddenCounter: counter, Block: func(v *providercore.Record, until time.Time, reason string) {
		blocker.BlockProviderScheduling(gatewayprovider.NewExecutionProvider(v), until, reason)
	}}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 301,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
		},
	}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"temporary edge rejection"}}`), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.SetErrorCalls)
	require.Equal(t, 1, repo.TempCalls)
	require.Contains(t, repo.LastTempReason, "temporary edge rejection")
	require.Contains(t, repo.LastTempReason, "(1/3)")
	require.Len(t, blocker.Providers, 1)
	require.Equal(t, provider.Record.ID, blocker.Providers[0].Record.ID)
	require.Equal(t, "openai_403_temp", blocker.Reasons[0])
	require.True(t, blocker.Until[0].After(time.Now()))
}

func TestRateLimitService_HandleUpstreamError_OpenAI403ThresholdDisables(t *testing.T) {
	repo := &gatewaytestkit.HealthStoreRecorder{}
	counter := &gatewaytestkit.ForbiddenCounter{Counts: []int64{3}}
	service := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{ForbiddenCounter: counter}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 302,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
		},
	}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), service, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"workspace forbidden by policy"}}`), nil)).StopScheduling

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.SetErrorCalls)
	require.Equal(t, 0, repo.TempCalls)
	require.Contains(t, repo.LastErrorMsg, "workspace forbidden by policy")
	require.Contains(t, repo.LastErrorMsg, "consecutive_403=3/3")
}

func (r *anthropicWindowLimitRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitCalls++
	r.lastRateLimitReset = resetAt
	return nil
}

func (r *anthropicWindowLimitRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.tempUnschedCalls++
	return nil
}

func (r *anthropicWindowLimitRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, resetAt time.Time, _ ...string) error {
	r.modelRateLimitCalls++
	r.lastModelRateLimitScope = scope
	r.lastModelRateLimitReset = resetAt
	return nil
}

func (r *anthropicWindowLimitRepo) UpdateSessionWindow(_ context.Context, _ int64, start, end *time.Time, status string) error {
	r.sessionWindowCalls++
	r.lastWindowStart = start
	r.lastWindowEnd = end
	r.lastWindowStatus = status
	return nil
}

func (r *anthropicWindowLimitRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.lastExtraUpdates = updates
	return nil
}

func TestHandleUpstreamError_AnthropicWindowLimitPreemptsTempUnschedRule(t *testing.T) {
	resetAt := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.02")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(resetAt.Unix(), 10))

	repo := &anthropicWindowLimitRepo{}
	svc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 42,
			Type:     capability.ProviderTypeOAuth,
			Platform: capability.PlatformAnthropic,
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusTooManyRequests),
						"keywords":         []any{"rate limit"},
						"duration_minutes": float64(10),
					},
				},
			},
		},
	}

	gatewayprovider.ApplyExecutionHealth(context.Background(), svc, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusTooManyRequests, headers, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your provider's rate limit. Please try again later."}}`), nil))

	require.Zero(t, repo.tempUnschedCalls, "官方 Anthropic 窗口限流不应被本地临时不可调度规则缩短")
	require.Equal(t, 1, repo.rateLimitCalls)
	require.Equal(t, resetAt, repo.lastRateLimitReset)
	require.Equal(t, 1, repo.sessionWindowCalls)
	require.NotNil(t, repo.lastWindowStart)
	require.NotNil(t, repo.lastWindowEnd)
	require.Equal(t, resetAt, *repo.lastWindowEnd)
	require.Equal(t, resetAt.Add(-5*time.Hour), *repo.lastWindowStart)
	require.Equal(t, "rejected", repo.lastWindowStatus)
}

// fable429Headers builds a 429 fixture with the Fable-specific 7d_oi window rejected
// while the five-hour and seven-day provider windows remain allowed.
func fable429Headers(reset5h, resetOI time.Time) http.Header {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.41")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(resetOI.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-7d-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.56")
	headers.Set("anthropic-ratelimit-unified-7d_oi-reset", strconv.FormatInt(resetOI.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-7d_oi-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-7d_oi-surpassed-threshold", "1.0")
	headers.Set("anthropic-ratelimit-unified-7d_oi-utilization", "1.0")
	headers.Set("anthropic-ratelimit-unified-fallback-percentage", "0.5")
	headers.Set("anthropic-ratelimit-unified-overage-disabled-reason", "org_level_disabled")
	headers.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-representative-claim", "seven_day_overage_included")
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(resetOI.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-status", "rejected")
	return headers
}

func TestHandleUpstreamError_Anthropic7dOiOnlyMarksModelRateLimit(t *testing.T) {
	now := time.Now()
	reset5h := now.Add(2 * time.Hour).Truncate(time.Second)
	resetOI := now.Add(80 * time.Hour).Truncate(time.Second)
	headers := fable429Headers(reset5h, resetOI)

	repo := &anthropicWindowLimitRepo{}
	svc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 42,
			Type:     capability.ProviderTypeOAuth,
			Platform: capability.PlatformAnthropic,
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusTooManyRequests),
						"keywords":         []any{"rate limit"},
						"duration_minutes": float64(10),
					},
				},
			},
		},
	}

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), svc, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusTooManyRequests, headers, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your provider's rate limit. Please try again later."}}`), []string{"claude-fable-5"})).StopScheduling

	require.False(t, shouldDisable)
	require.Zero(t, repo.rateLimitCalls, "7d_oi (Fable-only) window must not mark the whole provider rate limited")
	require.Zero(t, repo.tempUnschedCalls, "7d_oi window must not trigger local temp-unsched rules")
	require.Zero(t, repo.sessionWindowCalls, "7d_oi window must not rewrite the 5h session window as rejected")
	require.Equal(t, 1, repo.modelRateLimitCalls)
	require.Equal(t, providercore.AnthropicFableRateLimitKey, repo.lastModelRateLimitScope)
	require.Equal(t, resetOI, repo.lastModelRateLimitReset)

	// Passive sampling still updates Fable usage while the response reports a rate limit.
	require.NotNil(t, repo.lastExtraUpdates)
	require.Equal(t, 1.0, repo.lastExtraUpdates["passive_usage_7d_oi_utilization"])
	require.Equal(t, resetOI.Unix(), repo.lastExtraUpdates["passive_usage_7d_oi_reset"])
	require.Equal(t, 0.41, repo.lastExtraUpdates["session_window_utilization"])
}

func TestHandleUpstreamError_Anthropic5hWindowStillWinsOver7dOi(t *testing.T) {
	// A rejected five-hour window sets the provider cooldown using its reset and also records the Fable model limit.
	now := time.Now()
	reset5h := now.Add(2 * time.Hour).Truncate(time.Second)
	resetOI := now.Add(80 * time.Hour).Truncate(time.Second)
	headers := fable429Headers(reset5h, resetOI)
	headers.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")

	repo := &anthropicWindowLimitRepo{}
	svc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 42, Type: capability.ProviderTypeOAuth, Platform: capability.PlatformAnthropic}}

	gatewayprovider.ApplyExecutionHealth(context.Background(), svc, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusTooManyRequests, headers, nil, []string{"claude-fable-5"}))

	require.Equal(t, 1, repo.rateLimitCalls, "exhausted 5h window must still rate limit the provider")
	require.Equal(t, reset5h, repo.lastRateLimitReset)
	require.Equal(t, 1, repo.modelRateLimitCalls)
	require.Equal(t, providercore.AnthropicFableRateLimitKey, repo.lastModelRateLimitScope)
}

func TestHandleUpstreamError_AnthropicProviderWindowStillWinsOver7dOi(t *testing.T) {
	// A rejected seven-day provider window sets provider and Fable model cooldowns.
	now := time.Now()
	reset5h := now.Add(2 * time.Hour).Truncate(time.Second)
	resetOI := now.Add(80 * time.Hour).Truncate(time.Second)
	headers := fable429Headers(reset5h, resetOI)
	headers.Set("anthropic-ratelimit-unified-7d-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.02")

	repo := &anthropicWindowLimitRepo{}
	svc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 42, Type: capability.ProviderTypeOAuth, Platform: capability.PlatformAnthropic}}

	gatewayprovider.ApplyExecutionHealth(context.Background(), svc, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusTooManyRequests, headers, nil, []string{"claude-fable-5"}))

	require.Equal(t, 1, repo.rateLimitCalls, "exhausted 7d window must still rate limit the provider")
	require.Equal(t, resetOI, repo.lastRateLimitReset)
	require.Equal(t, 1, repo.modelRateLimitCalls, "Fable model rate limit should also be recorded")
	require.Equal(t, providercore.AnthropicFableRateLimitKey, repo.lastModelRateLimitScope)
}

func TestHandleUpstreamError_Anthropic429Without7dOiKeepsLegacyBehavior(t *testing.T) {
	// Without 7d_oi or a rejected provider window, a 429 uses the earlier reset for the provider cooldown.
	now := time.Now()
	reset5h := now.Add(2 * time.Hour).Truncate(time.Second)
	reset7d := now.Add(80 * time.Hour).Truncate(time.Second)

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.41")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(reset7d.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-7d-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.56")

	repo := &anthropicWindowLimitRepo{}
	svc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 42, Type: capability.ProviderTypeOAuth, Platform: capability.PlatformAnthropic}}

	gatewayprovider.ApplyExecutionHealth(context.Background(), svc, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusTooManyRequests, headers, nil, []string{"claude-fable-5"}))

	require.Zero(t, repo.modelRateLimitCalls, "no 7d_oi signal → no model rate limit")
	require.Equal(t, 1, repo.rateLimitCalls)
	require.Equal(t, reset5h, repo.lastRateLimitReset, "legacy path picks the sooner reset")
	require.Equal(t, 1, repo.sessionWindowCalls)
}

// newUpstreamHealthForTest constructs the observer from test configuration.
func newUpstreamHealthForTest(store gatewayprovider.ExecutionProviderStore, cfg *config.Config, cache providercore.TempUnschedCache, options providercore.HealthOptions, readers *gatewayprovider.RuntimeReaders) *provideradapter.UpstreamHealth {
	if cfg != nil {
		options.UnauthorizedCooldownMinutes = cfg.RateLimit.OAuth401CooldownMinutes
		options.OverloadMinutes = cfg.RateLimit.OverloadCooldownMinutes
		options.CNIntervalMinutes = cfg.Gateway.CNProviders.IntervalMinutes
	}
	return gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: store, Cache: cache, Options: options, Readers: readers})
}

// newExecutionReadersFixture wraps the settings stub in the settings cache before binding readers.
func newExecutionReadersFixture(repo settings.Repository, _ *config.Config) *gatewayprovider.RuntimeReaders {
	if repo != nil {
		repo = settings.New(repo)
	}
	return gatewaytestkit.RuntimeReaders(repo)
}

func TestRateLimitService_TempUnschedulableContextPreservesModelForPoolDependency(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	provider := gatewaytestkit.ModelNotFoundProvider()
	provider.Record.Credentials["pool_mode"] = true
	ctx := requeststate.WithHealthModel(context.Background(), []string{"gpt-5.4"})

	// With no model argument, pool health observation uses the contextual canonical model for cooldown scope.
	observation := gatewayprovider.HealthObservationFromContext(ctx, http.StatusNotFound, http.Header{},
		[]byte(`{"error":{"message":"endpoint not found"}}`), nil)
	handled := gatewayprovider.ApplyExecutionHealth(ctx, svc, provider, observation).StopScheduling

	require.True(t, handled)
	require.Zero(t, repo.TempCalls)
	require.Len(t, repo.ModelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.ModelRateLimitCalls[0].Scope)
}

func TestRateLimitService_ModelTempUnschedulableIsolatesSchedulerByModel(t *testing.T) {
	repo := &gatewaytestkit.ModelHealthStore{}
	svc := newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)

	provider := gatewaytestkit.ModelNotFoundProvider()
	provider.Record.Credentials["model_mapping"] = map[string]any{
		"public-a":   "upstream-a",
		"upstream-a": "upstream-b",
	}

	handled := gatewayprovider.ApplyExecutionHealth(context.Background(), svc, provider, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"endpoint not found"}}`), []string{"upstream-a"})).StopScheduling

	require.True(t, handled)
	require.Len(t, repo.ModelRateLimitCalls, 1)
	call := repo.ModelRateLimitCalls[0]
	require.Equal(t, "upstream-a", call.Scope, "canonical upstream model must not be mapped a second time")

	provider.Record.Extra = map[string]any{
		"model_rate_limits": map[string]any{
			call.Scope: map[string]any{
				"rate_limit_reset_at": call.ResetAt.UTC().Format(time.RFC3339),
			},
		},
	}

	require.False(t, gatewayprovider.ExecutionModelPolicy(provider).Schedulable(context.Background(), "public-a"))
	require.True(t, gatewayprovider.ExecutionModelPolicy(provider).Schedulable(context.Background(), "gpt-5.6-sol"))
}

// ListByPlatform returns active providers on the requested platform.
func (r *teamLinkedProviderRepoStub) ListByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]gatewayprovider.ExecutionProvider, 0, len(r.teamProviders))
	for _, acc := range r.teamProviders {
		if acc.Record.Platform == platform && acc.Record.Status == billing.StatusActive {
			out = append(out, acc)
		}
	}
	return out, nil
}

func (r *teamLinkedProviderRepoStub) SetError(ctx context.Context, id int64, errorMsg string) error {
	if err, ok := r.failSetError[id]; ok {
		return err
	}
	r.setErrorIDs = append(r.setErrorIDs, id)
	if r.setErrorMsgs == nil {
		r.setErrorMsgs = make(map[int64]string)
	}
	r.setErrorMsgs[id] = errorMsg
	return nil
}

func newTeamLinkedProvider(id int64, teamID string) gatewayprovider.ExecutionProvider {
	return gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Credentials: map[string]any{"chatgpt_account_id": teamID},
		},
	}
}

// newTeamLinkedFixture includes trigger 1, teammates 2 and 6, another team 3, API Key 4, shadow 5, and already-failed teammate 7.
func newTeamLinkedFixture() []gatewayprovider.ExecutionProvider {
	parentID := int64(1)
	shadow := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5,
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeOAuth,
			Status:           billing.StatusActive,
			ParentProviderID: &parentID,
		},
	}
	apikey := newTeamLinkedProvider(4, "team-A")
	apikey.Record.Type = capability.ProviderTypeAPIKey
	erroredSibling := newTeamLinkedProvider(7, "team-A")
	erroredSibling.Record.Status = providercore.StatusError
	return []gatewayprovider.ExecutionProvider{
		newTeamLinkedProvider(1, "team-A"),
		newTeamLinkedProvider(2, "team-A"),
		newTeamLinkedProvider(3, "team-B"),
		apikey,
		shadow,
		newTeamLinkedProvider(6, "team-A"),
		erroredSibling,
	}
}

func newTeamLinkedTestService(repo *teamLinkedProviderRepoStub) (*provideradapter.UpstreamHealth,
	*gatewaytestkit.RuntimeBlockRecorder,
) {
	blocker := &gatewaytestkit.RuntimeBlockRecorder{}
	rl := newUpstreamHealthForTest(repo, &config.Config{}, nil, providercore.HealthOptions{Block: func(v *providercore.Record, until time.Time, reason string) {
		blocker.BlockProviderScheduling(gatewayprovider.NewExecutionProvider(v), until, reason)
	}}, nil)

	return rl, blocker
}

func TestTeamLinkedError_FanoutMarksSameTeamProviders(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{teamProviders: newTeamLinkedFixture()}
	rl, blocker := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedProvider(1, "team-A")

	shouldDisable := gatewayprovider.ApplyExecutionHealth(context.Background(), rl, &trigger, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusPaymentRequired, http.Header{}, []byte(teamLinkedDeactivatedBody), nil)).StopScheduling

	require.True(t, shouldDisable)
	// Fan-out marks teammates 2 and 6 first; ordinary 402 handling then marks trigger 1.
	require.Equal(t, []int64{2, 6, 1}, repo.setErrorIDs)
	require.Contains(t, repo.setErrorMsgs[2], "team-linked error triggered by provider #1")
	require.Contains(t, repo.setErrorMsgs[6], "team-linked error triggered by provider #1")
	require.Contains(t, repo.setErrorMsgs[1], "Workspace deactivated (402)")
	require.NotContains(t, repo.setErrorMsgs[1], "team-linked")
	// Teammates are blocked in memory before persistence; the trigger uses auth_error.
	require.Equal(t, []string{providercore.OpenAITeamLinkedErrorBlockReason, providercore.OpenAITeamLinkedErrorBlockReason, "auth_error"}, blocker.Reasons)
	require.Equal(t, int64(2), blocker.Providers[0].Record.ID)
	require.Equal(t, int64(6), blocker.Providers[1].Record.ID)
	require.Equal(t, int64(1), blocker.Providers[2].Record.ID)
}

func TestTeamLinkedError_GenericPaymentErrorDoesNotFanout(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{teamProviders: newTeamLinkedFixture()}
	rl, _ := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedProvider(1, "team-A")

	gatewayprovider.ApplyExecutionHealth(context.Background(), rl, &trigger, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"insufficient balance"}}`), nil))

	require.Equal(t, []int64{1}, repo.setErrorIDs)
	require.Contains(t, repo.setErrorMsgs[1], "Payment required (402)")
	require.Zero(t, repo.listCalls)
}

func TestTeamLinkedError_DedupWithinTTL(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{teamProviders: newTeamLinkedFixture()}
	rl, _ := newTeamLinkedTestService(repo)
	first := newTeamLinkedProvider(1, "team-A")
	second := newTeamLinkedProvider(2, "team-A")

	gatewayprovider.ApplyExecutionHealth(context.Background(), rl, &first, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusPaymentRequired, http.Header{}, []byte(teamLinkedDeactivatedBody), nil))
	gatewayprovider.ApplyExecutionHealth(context.Background(), rl, &second, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusPaymentRequired, http.Header{}, []byte(teamLinkedDeactivatedBody), nil))

	// Deduplication suppresses repeated fan-out; ordinary 402 handling still marks trigger 2.
	require.Equal(t, []int64{2, 6, 1, 2}, repo.setErrorIDs)
	require.Equal(t, 1, repo.listCalls)
}

func TestTeamLinkedError_APIKeyTriggerDoesNotFanout(t *testing.T) {
	repo := &teamLinkedProviderRepoStub{teamProviders: newTeamLinkedFixture()}
	rl, _ := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedProvider(4, "team-A")
	trigger.Record.Type = capability.ProviderTypeAPIKey

	gatewayprovider.ApplyExecutionHealth(context.Background(), rl, &trigger, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusPaymentRequired, http.Header{}, []byte(teamLinkedDeactivatedBody), nil))

	require.Equal(t, []int64{4}, repo.setErrorIDs)
	require.Zero(t, repo.listCalls)
}

func TestRateLimitService_ApplyProviderSchedulingThreshold_SetsTempUnschedulable(t *testing.T) {
	settingsRepo := settingstestkit.NewMemory()
	settingsRepo.Data[providercore.SettingKeyProviderSchedulingThresholds] = `{"openai":80}`

	providerRepo := &gatewaytestkit.HealthStoreRecorder{}
	rl := newUpstreamHealthForTest(providerRepo, &config.Config{}, nil, providercore.HealthOptions{},

		newExecutionReadersFixture(settingsRepo, &config.Config{}))

	until := time.Now().UTC().Add(6 * time.Hour)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1001,
			Platform:    capability.PlatformOpenAI,
			Status:      billing.StatusActive,
			Schedulable: true,
			Extra: map[string]any{
				"codex_7d_used_percent": 91.5,
				"codex_7d_reset_at":     until.Format(time.RFC3339),
			},
		},
	}

	blocked := gatewayprovider.ApplyExecutionSchedulingThreshold(context.Background(), rl, provider)

	require.True(t, blocked)
	require.Equal(t, 1, providerRepo.TempCalls)
	require.NotNil(t, provider.Record.TempUnschedulableUntil)
	require.WithinDuration(t, until, *provider.Record.TempUnschedulableUntil, time.Second)
	require.True(t, providercore.IsProviderSchedulingThresholdReason(providerRepo.LastTempReason))

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(providerRepo.LastTempReason), &payload))
	require.Equal(t, capability.PlatformOpenAI, payload["platform"])
	require.Equal(t, "7d", payload["window"])
	require.Equal(t, float64(80), payload["threshold_percent"])
	require.Equal(t, float64(91.5), payload["used_percent"])
	require.Contains(t, payload["error_message"], "91.5% used >= 80%")
}

func TestRateLimitService_ApplyProviderSchedulingThreshold_UsesProviderOverrideInReason(t *testing.T) {
	settingsRepo := settingstestkit.NewMemory()
	settingsRepo.Data[providercore.SettingKeyProviderSchedulingThresholds] = `{"openai":90}`

	providerRepo := &gatewaytestkit.HealthStoreRecorder{}
	rl := newUpstreamHealthForTest(providerRepo, &config.Config{}, nil, providercore.HealthOptions{},

		newExecutionReadersFixture(settingsRepo, &config.Config{}))

	until := time.Now().UTC().Add(6 * time.Hour)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1003,
			Platform:    capability.PlatformOpenAI,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"provider_scheduling_threshold": 80,
			},
			Extra: map[string]any{
				"codex_7d_used_percent": 85.5,
				"codex_7d_reset_at":     until.Format(time.RFC3339),
			},
		},
	}

	blocked := gatewayprovider.ApplyExecutionSchedulingThreshold(context.Background(), rl, provider)

	require.True(t, blocked)
	require.Equal(t, 1, providerRepo.TempCalls)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(providerRepo.LastTempReason), &payload))
	require.Equal(t, float64(80), payload["threshold_percent"])
	require.Equal(t, float64(85.5), payload["used_percent"])
	require.Contains(t, payload["error_message"], "85.5% used >= 80%")
}

func (r *fableSchedulingThresholdRepoStub) SetModelRateLimit(_ context.Context, _ int64, scope string, resetAt time.Time, reason ...string) error {
	r.modelCalls++
	r.lastModelScope = scope
	r.lastModelReset = resetAt
	if len(reason) > 0 {
		r.lastModelReason = reason[0]
	}
	return nil
}

func TestRateLimitService_ApplyProviderSchedulingThreshold_FableOnlyLimitsFableModels(t *testing.T) {
	settingsRepo := settingstestkit.NewMemory()
	settingsRepo.Data[providercore.SettingKeyProviderSchedulingThresholds] = `{"anthropic":100}`

	providerRepo := &fableSchedulingThresholdRepoStub{}
	rl := newUpstreamHealthForTest(providerRepo, &config.Config{}, nil, providercore.HealthOptions{},

		newExecutionReadersFixture(settingsRepo, &config.Config{}))

	until := time.Now().UTC().Add(4 * 24 * time.Hour).Truncate(time.Second)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1004,
			Platform:    capability.PlatformAnthropic,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"provider_scheduling_threshold": 60,
			},
			Extra: map[string]any{
				"passive_usage_7d_utilization":    0.40,
				"passive_usage_7d_reset":          float64(time.Now().UTC().Add(3 * 24 * time.Hour).Unix()),
				"passive_usage_7d_oi_utilization": 0.61,
				"passive_usage_7d_oi_reset":       float64(until.Unix()),
			},
		},
	}

	blocked := gatewayprovider.ApplyExecutionSchedulingThreshold(context.Background(), rl, provider)

	require.False(t, blocked, "the Fable-only window must not pause the whole provider")
	require.Zero(t, providerRepo.TempCalls)
	require.Equal(t, 1, providerRepo.modelCalls)
	require.Equal(t, providercore.AnthropicFableRateLimitKey, providerRepo.lastModelScope)
	require.WithinDuration(t, until, providerRepo.lastModelReset, time.Second)
	require.True(t, providercore.IsProviderSchedulingThresholdReason(providerRepo.lastModelReason))
	require.False(t, gatewayprovider.ExecutionModelPolicy(provider).Schedulable(context.Background(), "claude-fable-5"))
	require.False(t, gatewayprovider.ExecutionModelPolicy(provider).Schedulable(context.Background(), "claude-fable-5[1m]"))
	require.True(t, gatewayprovider.ExecutionModelPolicy(provider).Schedulable(context.Background(), "claude-opus-4-8"))
	require.True(t, gatewayprovider.ExecutionModelPolicy(provider).Schedulable(context.Background(), "claude-sonnet-4-6"))

	blocked = gatewayprovider.ApplyExecutionSchedulingThreshold(context.Background(), rl, provider)

	require.False(t, blocked)
	require.Equal(t, 1, providerRepo.modelCalls, "an active model limit should not be persisted twice")
}

func TestRateLimitService_ApplyProviderSchedulingThreshold_SkipsDuplicateTempUnschedulable(t *testing.T) {
	settingsRepo := settingstestkit.NewMemory()
	settingsRepo.Data[providercore.SettingKeyProviderSchedulingThresholds] = `{"openai":80}`

	providerRepo := &gatewaytestkit.HealthStoreRecorder{}
	rl := newUpstreamHealthForTest(providerRepo, &config.Config{}, nil, providercore.HealthOptions{},

		newExecutionReadersFixture(settingsRepo, &config.Config{}))

	until := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	existingReason := providercore.BuildDetailedProviderSchedulingThresholdReason(providercore.ProviderSchedulingThresholdReasonInput{
		Platform:         capability.PlatformOpenAI,
		Window:           "7d",
		ThresholdPercent: 80,
		UsedPercent:      91.5,
		Until:            until,
		Now:              until.Add(-time.Hour),
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1002,
			Platform:                capability.PlatformOpenAI,
			Status:                  billing.StatusActive,
			Schedulable:             true,
			TempUnschedulableUntil:  &until,
			TempUnschedulableReason: existingReason,
			Extra: map[string]any{
				"codex_7d_used_percent": 91.5,
				"codex_7d_reset_at":     until.Format(time.RFC3339),
			},
		},
	}

	blocked := gatewayprovider.ApplyExecutionSchedulingThreshold(context.Background(), rl, provider)

	require.True(t, blocked)
	require.Equal(t, 0, providerRepo.TempCalls)
	require.Equal(t, existingReason, provider.Record.TempUnschedulableReason)
	require.NotNil(t, provider.Record.TempUnschedulableUntil)
	require.True(t, until.Equal(*provider.Record.TempUnschedulableUntil))
}

func TestRateLimitService_ApplyProviderSchedulingThreshold_UnsupportedPlatformDoesNotBlock(t *testing.T) {
	settingsRepo := settingstestkit.NewMemory()
	settingsRepo.Data[providercore.SettingKeyProviderSchedulingThresholds] = `{"openai":80}`

	providerRepo := &gatewaytestkit.HealthStoreRecorder{}
	rl := newUpstreamHealthForTest(providerRepo, &config.Config{}, nil, providercore.HealthOptions{},

		newExecutionReadersFixture(settingsRepo, &config.Config{}))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 2002,
			Platform:    "kiro",
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"provider_scheduling_threshold": 1,
			},
			Extra: map[string]any{
				"kiro_sched_utilization": 99.0,
				"kiro_sched_reset_at":    time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
			},
		},
	}

	blocked := gatewayprovider.ApplyExecutionSchedulingThreshold(context.Background(), rl, provider)

	require.False(t, blocked)
	require.Equal(t, 0, providerRepo.TempCalls)
	require.Nil(t, provider.Record.TempUnschedulableUntil)
	require.Empty(t, provider.Record.TempUnschedulableReason)
}

func (r *grokQuotaProviderRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.updateCalls++
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	if r.providersByID != nil {
		provider := r.providersByID[id]
		if provider == nil {
			return nil
		}
		if provider.Record.Extra == nil {
			provider.Record.Extra = make(map[string]any)
		}
		maps.Copy(provider.Record.Extra, updates)
	}
	return nil
}

func (r *grokQuotaProviderRepo) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.lastRateLimitedID = id
	r.lastRateLimitResetAt = resetAt
	return nil
}

func (r *grokQuotaProviderRepo) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	return r.SetRateLimited(ctx, id, resetAt)
}

func (r *grokQuotaProviderRepo) ClearRateLimitIfObserved(_ context.Context, _ int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	r.recoveryClearCalls++
	r.recoveryObservedAt = observedLimitedAt
	r.recoveryObservedReset = observedResetAt
	return r.recoveryClearResult, nil
}

func (r *grokQuotaProviderRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls++
	r.lastTempUnschedID = id
	r.lastTempUnschedUntil = until
	r.lastTempUnschedReason = reason
	return nil
}

func (r *grokPoolPolicyProviderRepo) SetError(_ context.Context, _ int64, _ string) error {
	r.setErrorCalls++
	return nil
}

func (r *grokPoolPolicyProviderRepo) SetOverloaded(_ context.Context, _ int64, _ time.Time) error {
	r.overloadedCalls++
	return nil
}

func (r *grokPoolPolicyProviderRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, _ time.Time, reason ...string) error {
	r.modelRateLimitCalls++
	r.lastModelRateLimitScope = scope
	if len(reason) > 0 {
		r.lastModelRateLimitReason = reason[0]
	}
	return nil
}

// newGrokPoolProvider returns a Grok API Key provider with pool mode enabled.
func newGrokPoolProvider(id int64) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"pool_mode": true},
		},
	}
}

// newGrokHealthForTest binds production health components for provider-state assertions.
func newGrokHealthForTest(store provideradapter.GrokHealthStore, throttle *providercore.WriteThrottle) *provideradapter.GrokHealth {
	return &provideradapter.GrokHealth{Store: store, Throttle: throttle, Runtime: providercore.NewRuntimeBlockState(time.Now), ModelTransient: providercore.NewModelTransientState(0), NormalizeModel: func(value *providercore.Record, model string) string {
		return (gatewayprovider.ModelPolicy{Record: value}).NormalizeOpenAI(model)
	}}
}

func newGrokPoolHealthForTest(value *gatewayprovider.ExecutionProvider) (*provideradapter.GrokHealth, *grokPoolPolicyProviderRepo) {
	repo := &grokPoolPolicyProviderRepo{grokQuotaProviderRepo: &grokQuotaProviderRepo{providersByID: map[int64]*gatewayprovider.ExecutionProvider{value.Record.ID: value}}}
	health := newGrokHealthForTest(repo, nil)
	health.Health = gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo, Options: providercore.HealthOptions{Block: health.Runtime.BlockProviderScheduling}})
	return health, repo
}

func (r *grokQuotaProviderRepo) GetByID(_ context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	return r.providersByID[id], nil
}

func handleGrokHealthForTest(health *provideradapter.GrokHealth, ctx context.Context, value *gatewayprovider.ExecutionProvider, status int, headers http.Header, body []byte, models ...string) bool {
	return gatewayprovider.ApplyGrokExecutionHealth(ctx, health, value, status, headers, body, "", models...).StopScheduling
}

func handleGrokHealthWithTeamForTest(health *provideradapter.GrokHealth, team string, ctx context.Context, value *gatewayprovider.ExecutionProvider, status int, headers http.Header, body []byte, models ...string) bool {
	return gatewayprovider.ApplyGrokExecutionHealth(ctx, health, value, status, headers, body, team, models...).StopScheduling
}

func (c *grokHealthTestClock) Now() time.Time {
	if n := c.nanos.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Now()
}

func (c *grokHealthTestClock) Set(now time.Time) { c.nanos.Store(now.UnixNano()) }

func bindGrokHealthClockForTest(health *provideradapter.GrokHealth) *grokHealthTestClock {
	clock := &grokHealthTestClock{}
	health.Runtime = providercore.NewRuntimeBlockState(clock.Now)
	return clock
}

func bindExpiredGrokHealthForTest(health *provideradapter.GrokHealth, id int64, expired time.Time) {
	clock := bindGrokHealthClockForTest(health)
	clock.Set(expired.Add(-time.Minute))
	health.Runtime.Block(id, expired, "原过期快照夹具")
	clock.nanos.Store(0)
}

func grokInt64PtrForTest(v int64) *int64 { return &v }

func TestHandleGrokProviderUpstreamErrorPoolModeSkipsDefaultLocalState(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		headers    http.Header
		body       []byte
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized},
		{name: "payment required", statusCode: http.StatusPaymentRequired},
		{name: "forbidden", statusCode: http.StatusForbidden, body: []byte(`{"error":{"message":"access denied"}}`)},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, headers: http.Header{"Retry-After": []string{"60"}}},
		{name: "server error", statusCode: http.StatusInternalServerError},
		{name: "overloaded", statusCode: 529},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newGrokPoolProvider(int64(620 + index))
			svc, repo := newGrokPoolHealthForTest(provider)

			shouldDisable := handleGrokHealthForTest(svc,
				context.Background(),
				provider,
				tt.statusCode,
				tt.headers,
				tt.body,
				"grok-4.5",
			)

			require.False(t, shouldDisable)
			require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
			require.Zero(t, repo.rateLimitedCalls)
			require.Zero(t, repo.tempUnschedCalls)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.overloadedCalls)
			require.Zero(t, repo.modelRateLimitCalls)
			require.Nil(t, provider.Record.RateLimitResetAt)
			require.Nil(t, provider.Record.TempUnschedulableUntil)

			if tt.statusCode == http.StatusTooManyRequests {
				require.Equal(t, 1, repo.updateCalls)
				stored, ok := provider.Record.Extra[grokQuotaSnapshotExtraKey].(*grok.QuotaSnapshot)
				require.True(t, ok)
				require.NotNil(t, stored.RetryAfterSeconds)
				require.Equal(t, 60, *stored.RetryAfterSeconds)
			}
		})
	}
}

func TestUpdateGrokUsageSnapshotPoolModeExhaustedSuccessIsObservationOnly(t *testing.T) {
	provider := newGrokPoolProvider(626)
	svc, repo := newGrokPoolHealthForTest(provider)
	resetAt := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	headers := http.Header{
		"X-Ratelimit-Limit-Requests":     []string{"10"},
		"X-Ratelimit-Remaining-Requests": []string{"0"},
		"X-Ratelimit-Reset-Requests":     []string{fmt.Sprintf("%d", resetAt.Unix())},
	}

	svc.ObserveResponse(context.Background(), provider.View(), headers, http.StatusOK, "")

	require.Equal(t, 1, repo.updateCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	stored, ok := provider.Record.Extra[grokQuotaSnapshotExtraKey].(*grok.QuotaSnapshot)
	require.True(t, ok)
	require.NotNil(t, stored.Requests)
	require.NotNil(t, stored.Requests.Remaining)
	require.Zero(t, *stored.Requests.Remaining)
}

func TestHandleGrokProviderUpstreamErrorPoolModeKeepsExplicitPolicies(t *testing.T) {
	t.Run("custom error code still disables provider", func(t *testing.T) {
		provider := newGrokPoolProvider(627)
		provider.Record.Credentials["custom_error_codes_enabled"] = true
		provider.Record.Credentials["custom_error_codes"] = []any{float64(http.StatusUnauthorized)}
		svc, repo := newGrokPoolHealthForTest(provider)

		shouldDisable := handleGrokHealthForTest(svc,
			context.Background(),
			provider,
			http.StatusUnauthorized,
			nil,
			[]byte(`{"error":{"message":"invalid api key"}}`),
			"grok-4.5",
		)

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls)
		require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	})

	t.Run("custom non-failover code still disables provider", func(t *testing.T) {
		provider := newGrokPoolProvider(633)
		provider.Record.Credentials["custom_error_codes_enabled"] = true
		provider.Record.Credentials["custom_error_codes"] = []any{float64(http.StatusUnprocessableEntity)}
		svc, repo := newGrokPoolHealthForTest(provider)

		decision := gatewayprovider.ApplyGrokExecutionHealth(context.Background(), svc, provider, http.StatusUnprocessableEntity, nil, []byte(`{"error":{"message":"configured"}}`), "", "grok-4.5")

		require.Equal(t, providercore.ErrorPolicyCustomMatched, decision.Policy)
		require.True(t, decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), http.StatusUnprocessableEntity, false))
		require.False(t, decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), http.StatusUnprocessableEntity))
		require.Equal(t, 1, repo.setErrorCalls)
		require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	})

	t.Run("matching temporary rule only pauses requested model", func(t *testing.T) {
		provider := newGrokPoolProvider(628)
		provider.Record.Credentials["temp_unschedulable_enabled"] = true
		provider.Record.Credentials["temp_unschedulable_rules"] = []any{
			map[string]any{
				"error_code":       float64(http.StatusServiceUnavailable),
				"keywords":         []any{"maintenance"},
				"duration_minutes": float64(30),
			},
		}
		svc, repo := newGrokPoolHealthForTest(provider)

		shouldDisable := handleGrokHealthForTest(svc,
			context.Background(),
			provider,
			http.StatusServiceUnavailable,
			nil,
			[]byte(`{"error":{"message":"maintenance in progress"}}`),
			"grok-4.5",
		)

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.modelRateLimitCalls)
		require.Equal(t, "grok-4.5", repo.lastModelRateLimitScope)
		require.Zero(t, repo.tempUnschedCalls)
		require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	})

	t.Run("unmatched temporary rule keeps default pool behavior", func(t *testing.T) {
		provider := newGrokPoolProvider(629)
		provider.Record.Credentials["temp_unschedulable_enabled"] = true
		provider.Record.Credentials["temp_unschedulable_rules"] = []any{
			map[string]any{
				"error_code":       float64(http.StatusServiceUnavailable),
				"keywords":         []any{"maintenance"},
				"duration_minutes": float64(30),
			},
		}
		svc, repo := newGrokPoolHealthForTest(provider)

		shouldDisable := handleGrokHealthForTest(svc,
			context.Background(),
			provider,
			http.StatusServiceUnavailable,
			nil,
			[]byte(`{"error":{"message":"temporary outage"}}`),
			"grok-4.5",
		)

		require.False(t, shouldDisable)
		require.Zero(t, repo.modelRateLimitCalls)
		require.Zero(t, repo.tempUnschedCalls)
		require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	})
}

func TestHandleGrokProviderUpstreamErrorTempUnschedulesNonRateLimitStates(t *testing.T) {
	tests := []struct {
		name            string
		status          int
		headers         http.Header
		wantReason      string
		wantMinCooldown time.Duration
		wantMaxCooldown time.Duration
	}{
		{
			name:            "unauthorized reauth",
			status:          http.StatusUnauthorized,
			wantReason:      "grok credentials unauthorized",
			wantMinCooldown: 10*time.Minute - time.Second,
			wantMaxCooldown: 10*time.Minute + time.Second,
		},
		{
			name:            "forbidden entitlement",
			status:          http.StatusForbidden,
			wantReason:      "grok access or entitlement denied",
			wantMinCooldown: 30*time.Minute - time.Second,
			wantMaxCooldown: 30*time.Minute + time.Second,
		},
		{
			name:            "payment required",
			status:          http.StatusPaymentRequired,
			wantReason:      "grok payment required",
			wantMinCooldown: 30*time.Minute - time.Second,
			wantMaxCooldown: 30*time.Minute + time.Second,
		},
		{
			name:            "method not allowed",
			status:          http.StatusMethodNotAllowed,
			wantReason:      "grok endpoint not supported (405)",
			wantMinCooldown: 30*time.Minute - time.Second,
			wantMaxCooldown: 30*time.Minute + time.Second,
		},
		{
			name:            "upstream temporary error",
			status:          http.StatusInternalServerError,
			wantReason:      "grok upstream temporary error",
			wantMinCooldown: 2*time.Minute - time.Second,
			wantMaxCooldown: 2*time.Minute + time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 61, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
			repo := &grokQuotaProviderRepo{}
			svc := newGrokHealthForTest(repo, nil)
			before := time.Now()

			handleGrokHealthForTest(svc, context.Background(), provider, tt.status, tt.headers, nil)

			require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
			require.Equal(t, 1, repo.tempUnschedCalls)
			require.Zero(t, repo.rateLimitedCalls)
			require.Equal(t, provider.Record.ID, repo.lastTempUnschedID)
			require.Equal(t, tt.wantReason, repo.lastTempUnschedReason)
			require.True(t, repo.lastTempUnschedUntil.After(before.Add(tt.wantMinCooldown)))
			require.True(t, repo.lastTempUnschedUntil.Before(before.Add(tt.wantMaxCooldown)))
		})
	}
}

func TestHandleGrokProviderUpstreamErrorSpendingLimit403RateLimits(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 614, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	before := time.Now()
	body := []byte(`{"code":"personal-team-blocked:spending-limit","error":"You have run out of credits"}`)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusForbidden, nil, body)

	require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Equal(t, provider.Record.ID, repo.lastRateLimitedID)
	require.WithinDuration(t, before.Add(grokSpendingLimitProbeCooldown), repo.lastRateLimitResetAt, 2*time.Second)
	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, grok.IsSpendingLimitError(body))
}

func TestHandleGrokProviderUpstreamError5xxRespectsPoolMode(t *testing.T) {
	t.Run("pool mode keeps scheduling state", func(t *testing.T) {
		provider := &gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 611,
				Platform: capability.PlatformGrok,
				Type:     capability.ProviderTypeAPIKey,
				Credentials: map[string]any{
					"pool_mode": true,
				},
			},
		}
		repo := &grokQuotaProviderRepo{}
		svc := newGrokHealthForTest(repo, nil)

		handleGrokHealthForTest(svc, context.Background(), provider, http.StatusBadGateway, nil, nil)

		require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
		require.Zero(t, repo.tempUnschedCalls)
		require.Nil(t, provider.Record.TempUnschedulableUntil)
		require.Empty(t, provider.Record.TempUnschedulableReason)
	})

	t.Run("non-pool mode keeps two minute cooldown", func(t *testing.T) {
		provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 612, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}}
		repo := &grokQuotaProviderRepo{}
		svc := newGrokHealthForTest(repo, nil)
		before := time.Now()

		handleGrokHealthForTest(svc, context.Background(), provider, http.StatusBadGateway, nil, nil)

		require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
		require.Equal(t, 1, repo.tempUnschedCalls)
		require.Equal(t, provider.Record.ID, repo.lastTempUnschedID)
		require.Equal(t, "grok upstream temporary error", repo.lastTempUnschedReason)
		require.WithinDuration(t, before.Add(2*time.Minute), repo.lastTempUnschedUntil, time.Second)
	})
}

func TestHandleGrokProviderUpstreamError405RespectsPoolMode(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 613,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"pool_mode": true,
			},
		},
	}
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusMethodNotAllowed, nil, nil)

	require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }), "公共池提供商应跳过 405 默认冷却")
	require.Zero(t, repo.tempUnschedCalls)
	require.Nil(t, provider.Record.TempUnschedulableUntil)
}

func TestHandleGrokProviderUpstreamError429SetsRateLimitedFromRetryAfter(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 61, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	before := time.Now()

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"45"}}, nil)

	require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Equal(t, provider.Record.ID, repo.lastRateLimitedID)
	require.WithinDuration(t, before.Add(45*time.Second), repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestHandleGrokProviderUpstreamError402RecoversAfterCooldownExpiry(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 610, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
			Status: billing.StatusActive, Schedulable: true,
		},
	}
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusPaymentRequired, nil, nil)
	require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	require.Equal(t, 1, repo.tempUnschedCalls)

	expired := time.Now().Add(-time.Second)
	provider.Record.TempUnschedulableUntil = &expired
	bindExpiredGrokHealthForTest(svc, provider.Record.ID, expired)

	require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	require.True(t, provider.View().IsSchedulable())
}

func TestHandleGrokProviderUpstreamError429UsesLatestExhaustedWindowReset(t *testing.T) {
	now := time.Now()
	requestReset := now.Add(10 * time.Minute).Truncate(time.Second)
	tokenReset := now.Add(20 * time.Minute).Truncate(time.Second)
	headers := http.Header{
		"X-Ratelimit-Limit-Requests":     []string{"10"},
		"X-Ratelimit-Remaining-Requests": []string{"0"},
		"X-Ratelimit-Reset-Requests":     []string{fmt.Sprintf("%d", requestReset.Unix())},
		"X-Ratelimit-Limit-Tokens":       []string{"1000"},
		"X-Ratelimit-Remaining-Tokens":   []string{"0"},
		"X-Ratelimit-Reset-Tokens":       []string{fmt.Sprintf("%d", tokenReset.Unix())},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 62, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusTooManyRequests, headers, nil)

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, tokenReset, repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestHandleGrokProviderUpstreamError429UsesFallbackReset(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 63, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	before := time.Now()

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusTooManyRequests, nil, nil)

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, before.Add(grokRateLimitFallbackCooldown), repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestGrokRateLimitResetAtForProviderEscalatesRepeated429s(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	retryAfter := 45
	snapshot := &grok.QuotaSnapshot{
		StatusCode:        http.StatusTooManyRequests,
		RetryAfterSeconds: &retryAfter,
		UpdatedAt:         now.Format(time.RFC3339),
	}
	tests := []struct {
		name             string
		previousCooldown time.Duration
		wantCooldown     time.Duration
	}{
		{name: "repeat after short boundary", previousCooldown: 45 * time.Second, wantCooldown: grokRateLimitRepeatCooldown},
		{name: "sustained repeat", previousCooldown: grokRateLimitRepeatCooldown, wantCooldown: grokRateLimitSustainedCooldown},
		{name: "capped repeat", previousCooldown: grokRateLimitSustainedCooldown, wantCooldown: grokRateLimitMaxAdaptiveCooldown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previousReset := now.Add(-time.Second)
			previousLimited := previousReset.Add(-tt.previousCooldown)
			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 630,
					Platform:         capability.PlatformGrok,
					Type:             capability.ProviderTypeOAuth,
					RateLimitedAt:    &previousLimited,
					RateLimitResetAt: &previousReset,
				},
			}

			resetAt, limited := providercore.GrokRateLimitResetAtForProvider(provider.View(), snapshot, now)

			require.True(t, limited)
			require.WithinDuration(t, now.Add(tt.wantCooldown), resetAt, time.Second)
		})
	}
}

func TestGrokRateLimitResetAtForProviderPreservesAuthoritativeAndQuietRecovery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	retryAfter := 45
	previousReset := now.Add(-grokRateLimitBackoffQuietPeriod - time.Second)
	previousLimited := previousReset.Add(-grokRateLimitSustainedCooldown)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 631,
			Platform:         capability.PlatformGrok,
			Type:             capability.ProviderTypeOAuth,
			RateLimitedAt:    &previousLimited,
			RateLimitResetAt: &previousReset,
		},
	}
	snapshot := &grok.QuotaSnapshot{
		StatusCode:        http.StatusTooManyRequests,
		RetryAfterSeconds: &retryAfter,
		UpdatedAt:         now.Format(time.RFC3339),
	}

	resetAt, limited := providercore.GrokRateLimitResetAtForProvider(provider.View(), snapshot, now)
	require.True(t, limited)
	require.WithinDuration(t, now.Add(45*time.Second), resetAt, time.Second)

	authoritativeReset := now.Add(2 * time.Hour)
	remaining := int64(0)
	snapshot.Requests = &grok.QuotaWindow{Remaining: &remaining, ResetUnix: grokInt64PtrForTest(authoritativeReset.Unix())}
	recentReset := now.Add(-time.Second)
	recentLimited := recentReset.Add(-grokRateLimitSustainedCooldown)
	provider.Record.RateLimitResetAt = &recentReset
	provider.Record.RateLimitedAt = &recentLimited

	resetAt, limited = providercore.GrokRateLimitResetAtForProvider(provider.View(), snapshot, now)
	require.True(t, limited)
	require.WithinDuration(t, authoritativeReset, resetAt, time.Second)
}

func TestGrokRateLimitResetAtForProviderLeavesAPIKey429PolicyUnchanged(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	retryAfter := 45
	previousReset := now.Add(-time.Second)
	previousLimited := previousReset.Add(-grokRateLimitSustainedCooldown)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 632,
			Platform:         capability.PlatformGrok,
			Type:             capability.ProviderTypeAPIKey,
			RateLimitedAt:    &previousLimited,
			RateLimitResetAt: &previousReset,
		},
	}
	snapshot := &grok.QuotaSnapshot{
		StatusCode:        http.StatusTooManyRequests,
		RetryAfterSeconds: &retryAfter,
		UpdatedAt:         now.Format(time.RFC3339),
	}

	resetAt, limited := providercore.GrokRateLimitResetAtForProvider(provider.View(), snapshot, now)
	require.True(t, limited)
	require.WithinDuration(t, now.Add(45*time.Second), resetAt, time.Second)
}

func TestGrokRateLimitResetAtUsesFutureWindowAfterRetryAfterExpires(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	observedAt := now.Add(-2 * time.Minute)
	windowReset := now.Add(15 * time.Minute)
	retryAfter := 30
	snapshot := &grok.QuotaSnapshot{
		StatusCode:        http.StatusTooManyRequests,
		UpdatedAt:         observedAt.Format(time.RFC3339),
		RetryAfterSeconds: &retryAfter,
		Requests: &grok.QuotaWindow{
			Limit:     grokInt64PtrForTest(10),
			Remaining: grokInt64PtrForTest(0),
			ResetUnix: grokInt64PtrForTest(windowReset.Unix()),
		},
	}

	resetAt, limited := providercore.GrokRateLimitResetAt(snapshot, now)

	require.True(t, limited)
	require.WithinDuration(t, windowReset, resetAt, time.Second)
}

func TestHandleGrokProviderUpstreamError429DoesNotShortenExistingPause(t *testing.T) {
	existingUntil := time.Now().Add(15 * time.Minute)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 64,
			Platform:                capability.PlatformGrok,
			Type:                    capability.ProviderTypeOAuth,
			TempUnschedulableUntil:  &existingUntil,
			TempUnschedulableReason: "existing pause",
		},
	}
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	clock := bindGrokHealthClockForTest(svc)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"45"}}, nil)

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, time.Now().Add(45*time.Second), repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
	clock.Set(existingUntil.Add(-time.Second))
	require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
	clock.Set(existingUntil.Add(time.Second))
	require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
}

func TestUpdateGrokUsageSnapshotExhaustedSuccessBypassesThrottleAndSetsRateLimited(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 65, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, providercore.NewWriteThrottle(time.Hour))
	now := time.Now()

	// Consume the ordinary snapshot write allowance first.
	svc.StoreSnapshot(context.Background(), provider.View(), &grok.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &grok.QuotaWindow{
			Limit:     grokInt64PtrForTest(10),
			Remaining: grokInt64PtrForTest(9),
		},
		UpdatedAt: now.UTC().Format(time.RFC3339),
	}, true, "")
	resetAt := now.Add(30 * time.Minute).Truncate(time.Second)
	svc.StoreSnapshot(context.Background(), provider.View(), &grok.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &grok.QuotaWindow{
			Limit:     grokInt64PtrForTest(10),
			Remaining: grokInt64PtrForTest(0),
			ResetUnix: grokInt64PtrForTest(resetAt.Unix()),
			ResetAt:   resetAt.UTC().Format(time.RFC3339),
		},
		UpdatedAt: now.UTC().Format(time.RFC3339),
	}, true, "")

	require.Equal(t, 2, repo.updateCalls)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Equal(t, provider.Record.ID, repo.lastRateLimitedID)
	require.WithinDuration(t, resetAt, repo.lastRateLimitResetAt, time.Second)
	require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
}

func TestUpdateGrokUsageSnapshotAvailableSuccessDoesNotSetRateLimited(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 66, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

	svc.StoreSnapshot(context.Background(), provider.View(), &grok.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &grok.QuotaWindow{
			Limit:     grokInt64PtrForTest(10),
			Remaining: grokInt64PtrForTest(1),
		},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}, true, "")

	require.Equal(t, 1, repo.updateCalls)
	require.Zero(t, repo.rateLimitedCalls)
}

func TestUpdateGrokUsageFromResponseHeaderlessSuccessClearsObservedCooldown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-grokRateLimitRepeatCooldown)
	observedResetAt := now.Add(-time.Second)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 660,
			Platform:         capability.PlatformGrok,
			Type:             capability.ProviderTypeOAuth,
			RateLimitedAt:    &limitedAt,
			RateLimitResetAt: &observedResetAt,
		},
	}
	repo := &grokQuotaProviderRepo{recoveryClearResult: true}
	svc := newGrokHealthForTest(repo, providercore.NewWriteThrottle(time.Hour))

	svc.ObserveResponse(context.Background(), provider.View(), nil, http.StatusOK, "")

	require.Zero(t, repo.updateCalls, "headerless success must not overwrite an informative quota snapshot")
	require.Equal(t, 1, repo.recoveryClearCalls)
	require.Equal(t, limitedAt, repo.recoveryObservedAt)
	require.Equal(t, observedResetAt, repo.recoveryObservedReset)
	require.Same(t, &observedResetAt, provider.Record.RateLimitResetAt, "shared provider snapshots must not be mutated in place")
}

func TestUpdateGrokUsageFromResponseRecoveryRespectsCancellationAndAPIKeyBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	observedResetAt := now.Add(-time.Second)
	observedLimitedAt := observedResetAt.Add(-grokRateLimitRepeatCooldown)

	t.Run("parent cancellation does not mutate provider state", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		provider := &gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 661,
				Platform:         capability.PlatformGrok,
				Type:             capability.ProviderTypeOAuth,
				RateLimitedAt:    &observedLimitedAt,
				RateLimitResetAt: &observedResetAt,
			},
		}
		repo := &grokQuotaProviderRepo{recoveryClearResult: true}
		svc := newGrokHealthForTest(repo, nil)

		svc.ObserveResponse(ctx, provider.View(), nil, http.StatusOK, "")

		require.Zero(t, repo.recoveryClearCalls)
	})

	t.Run("API key success does not alter OAuth cooldown state", func(t *testing.T) {
		provider := &gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 662,
				Platform:         capability.PlatformGrok,
				Type:             capability.ProviderTypeAPIKey,
				RateLimitedAt:    &observedLimitedAt,
				RateLimitResetAt: &observedResetAt,
			},
		}
		repo := &grokQuotaProviderRepo{recoveryClearResult: true}
		svc := newGrokHealthForTest(repo, nil)

		svc.ObserveResponse(context.Background(), provider.View(), nil, http.StatusOK, "")

		require.Zero(t, repo.recoveryClearCalls)
	})
}

func TestUpdateGrokUsageSnapshotExhaustedSuccessWithoutResetUsesFallback(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 67, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	before := time.Now()

	svc.StoreSnapshot(context.Background(), provider.View(), &grok.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Tokens: &grok.QuotaWindow{
			Limit:     grokInt64PtrForTest(2_000_000),
			Remaining: grokInt64PtrForTest(0),
		},
		UpdatedAt: before.UTC().Format(time.RFC3339),
	}, true, "")

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, before.Add(grokRateLimitFallbackCooldown), repo.lastRateLimitResetAt, time.Second)
	stored, ok := repo.updates[provider.Record.ID][grokQuotaSnapshotExtraKey].(*grok.QuotaSnapshot)
	require.True(t, ok)
	require.NotNil(t, stored.Tokens.ResetUnix)
	paused, _ := providercore.GrokQuotaWindowAutoPause("tokens", stored.Tokens, before.Add(time.Second))
	require.True(t, paused)
	paused, _ = providercore.GrokQuotaWindowAutoPause("tokens", stored.Tokens, repo.lastRateLimitResetAt.Add(time.Second))
	require.False(t, paused)
}

func TestHandleGrokProviderUpstreamErrorEntitlement403KeepsDefaultCooldown(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 4716, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	before := time.Now()

	handleGrokHealthForTest(svc,
		context.Background(), provider, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(29*time.Minute))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(31*time.Minute))
}

func TestHandleGrokProviderUpstreamError403UsesConfiguredRule(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4717,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusForbidden),
						"keywords":         []any{"subscription"},
						"duration_minutes": float64(7),
					},
				},
			},
		},
	}
	before := time.Now()

	handleGrokHealthForTest(svc,
		context.Background(), provider, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(6*time.Minute))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(8*time.Minute))
}

func TestHandleGrokProviderUpstreamError403ConfiguredUnmatchedKeepsDefaultCooldown(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4718,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusForbidden),
						"keywords":         []any{"different failure"},
						"duration_minutes": float64(7),
					},
				},
			},
		},
	}

	handleGrokHealthForTest(svc,
		context.Background(), provider, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	require.True(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
}

func TestHandleGrokProviderUpstreamError_FreeUsageBodyCoolsProvider(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9101, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	before := time.Now()
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"You've used all the included free usage. Usage resets over a rolling 24-hour window."}}`)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusBadRequest, nil, body)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok free usage exhausted", repo.lastTempUnschedReason)
	// Without an absolute upstream reset, exhausted rolling quota uses a short probe cooldown.
	// It must not impose a 24-hour lockout.
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(grok.GrokFreeUsageProbeCooldown-time.Second))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(grok.GrokFreeUsageProbeCooldown+time.Second))
}

func TestHandleGrokProviderUpstreamError_FreeUsageUsesUpstreamReset(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9102, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"free usage exhausted; rolling 24-hour window"}}`)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"3600"}}, body)

	require.Zero(t, repo.tempUnschedCalls)
	require.WithinDuration(t, time.Now().Add(time.Hour), repo.lastRateLimitResetAt, 2*time.Second)
}

func TestHandleGrokProviderUpstreamError_EmptyOutputCoolsProvider(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9102, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	before := time.Now()

	handleGrokHealthForTest(svc,
		context.Background(), provider, http.StatusBadGateway, nil,
		[]byte(`empty model output: no content/tool_calls`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok empty model output", repo.lastTempUnschedReason)
	require.WithinDuration(t, before.Add(4*time.Minute), repo.lastTempUnschedUntil, time.Second)
}

func TestHandleGrokProviderUpstreamError_MultiAgentCapacityBlocksOnlyThatModel(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9120, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	ctx := context.Background()

	handleGrokHealthWithTeamForTest(svc, "grok-4.20-multi-agent-0309",
		ctx, provider, http.StatusBadGateway, nil,
		[]byte(`{"error":{"message":"engine_overloaded"}}`),
	)

	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, providercore.IsGrokModelQuotaBlocked(provider.Record.ID, "grok-4.20-multi-agent-0309", time.Now()))
	require.False(t, providercore.IsGrokModelQuotaBlocked(provider.Record.ID, "grok-4.5", time.Now()))
}

func TestHandleGrokProviderUpstreamError_CapacityNeverCoolsProvider(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9121, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	ctx := context.Background()

	handleGrokHealthWithTeamForTest(svc, "grok-4.6", ctx, provider, http.StatusTooManyRequests, nil,
		[]byte(`{"error":{"message":"The model is currently at capacity due to high demand"}}`))

	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
}

func TestHandleGrokProviderUpstreamError_FreeUsageDoesNotCoolPoolMode(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 9103,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"pool_mode": true,
			},
		},
	}
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"free usage exhausted"}}`)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusBadRequest, nil, body)

	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, svc.Runtime.Blocked(provider.Record.ID, func() string { return providercore.RefreshCredentialIdentity(provider.View()) }))
}

func TestHandleGrokProviderUpstreamError_ContentPolicyStillNoMutation(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9104, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)

	handleGrokHealthForTest(svc, context.Background(), provider, http.StatusForbidden, nil, body)

	require.Zero(t, repo.tempUnschedCalls)
}

func TestHandleGrokProviderUpstreamError_Entitlement403Unchanged(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newGrokHealthForTest(repo, nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9105, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	before := time.Now()

	handleGrokHealthForTest(svc,
		context.Background(), provider, http.StatusForbidden, nil,
		[]byte(`{"error":{"message":"subscription required"}}`),
	)

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(29*time.Minute))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(31*time.Minute))
}

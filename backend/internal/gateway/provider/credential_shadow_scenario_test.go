package provider

// This file checks shadow-provider credentials, eligibility, and parent health.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestSparkShadowIntegration covers token lookup, model eligibility, and parent credential health.
// Shadows read rotated parent tokens, while model_mapping determines model eligibility.
// A parent error blocks shadows; scheduling pauses and global rate limits leave credentials usable.
func TestSparkShadowIntegration(t *testing.T) {
	ctx := context.Background()
	pid := int64(100)

	// Mutate the shared credential map to simulate rotation without rebuilding the stub.
	parent := &ExecutionProvider{
		Record: provider.Record{
			LoadLocation: time.LoadLocation, ID: 100,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"access_token": "T1",
			},
		},
	}
	// The shadow reads parent credentials and uses the Spark quota dimension.
	shadow := &ExecutionProvider{
		Record: provider.Record{
			LoadLocation: time.LoadLocation, ID: 200,
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeOAuth,
			ParentProviderID: &pid,
			QuotaDimension:   provider.QuotaDimensionSpark,
			Status:           billing.StatusActive,
			Schedulable:      true,
		},
	}

	// The repository stores execution-target pointers,
	// so credential-map mutations are visible on the next lookup.
	repo := newStubCredRepo(parent)
	credentials := &provider.OpenAIExecutionCredentials{Parent: func(ctx context.Context, id int64) (*provider.Record, error) {
		value, err := repo.GetByID(ctx, id)
		return ExecutionRecord(value), err
	}}

	// Credential rotation must be visible through the shadow.

	t.Run("credential_readthrough_initial_T1", func(t *testing.T) {
		// A shadow without credentials resolves to its parent.
		got, err := CredentialProvider(ctx, repo, shadow)
		require.NoError(t, err)
		require.Equal(t, int64(100), got.Record.ID, "解析结果应为母提供商")
		require.Equal(t, "T1", got.View().GetOpenAIAccessToken(),
			"初始应读到 T1")
	})

	t.Run("credential_readthrough_after_rotation_T2", func(t *testing.T) {
		// Rotate the parent token in place.
		// The next shadow lookup must read the updated value.
		parent.Record.Credentials["access_token"] = "T2"

		got, err := CredentialProvider(ctx, repo, shadow)
		require.NoError(t, err)
		require.Equal(t, "T2", got.View().GetOpenAIAccessToken(),
			"轮换后影子必须立即反映母提供商新 token（零脱钩）")
	})

	t.Run("get_access_token_e2e_reads_through_T3", func(t *testing.T) {
		// Resolve through the credential source after another rotation.
		// With no token provider, Resolve reads GetOpenAIAccessToken directly.
		parent.Record.Credentials["access_token"] = "T3"
		token, tokenType, err := credentials.Resolve(ctx, ExecutionRecord(shadow))
		require.NoError(t, err)
		require.Equal(t, "T3", token,
			"Resolve(影子) 必须返回母提供商当前 token")
		require.Equal(t, "oauth", tokenType)
	})

	t.Run("normal_provider_returns_its_own_token", func(t *testing.T) {
		// An ordinary provider returns its own credentials.
		ordinary := &ExecutionProvider{
			Record: provider.Record{
				LoadLocation: time.LoadLocation, ID: 300,
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeOAuth,
				Status:      billing.StatusActive,
				Schedulable: true,
				Credentials: map[string]any{
					"access_token": "ordinary-token",
				},
			},
		}
		token, _, err := credentials.Resolve(ctx, ExecutionRecord(ordinary))
		require.NoError(t, err)
		require.Equal(t, "ordinary-token", token)
	})

	// Model support determines routing eligibility.

	t.Run("routing_invariant", func(t *testing.T) {
		// Eligibility follows model_mapping and IsModelSupported.
		sparkModel := "gpt-5.3-codex-spark"
		normalModel := "gpt-5.3-codex"
		sparkCreds := map[string]any{"model_mapping": provideradapter.DefaultSparkShadowModels()}

		pid := int64(1)
		sparkShadow := &ExecutionProvider{Record: provider.Record{LoadLocation: time.LoadLocation, ID: 2, ParentProviderID: &pid, Platform: capability.PlatformOpenAI, Credentials: sparkCreds}}
		require.True(t, ExecutionProtocolRecord(sparkShadow).IsModelSupported(sparkModel, provideradapter.ModelDefaults(), provideradapter.ModelRules(ExecutionProtocolRecord(sparkShadow))), "影子配 spark → 接 spark")
		require.False(t, ExecutionProtocolRecord(sparkShadow).IsModelSupported(normalModel, provideradapter.ModelDefaults(), provideradapter.ModelRules(ExecutionProtocolRecord(sparkShadow))), "影子（仅 spark mapping）→ 拒非 spark")

		normalWithSpark := &ExecutionProvider{Record: provider.Record{LoadLocation: time.LoadLocation, ID: 3, Platform: capability.PlatformOpenAI, Credentials: sparkCreds}}
		require.True(t, ExecutionProtocolRecord(normalWithSpark).IsModelSupported(sparkModel, provideradapter.ModelDefaults(), provideradapter.ModelRules(ExecutionProtocolRecord(normalWithSpark))), "普通提供商配 spark → 接 spark（不再按类型排除）")

		normalNoSpark := &ExecutionProvider{Record: provider.Record{
			LoadLocation: time.LoadLocation, ID: 4, Platform: capability.PlatformOpenAI,
			Credentials: map[string]any{"model_whitelist": []string{normalModel}, "model_mapping": map[string]any{normalModel: normalModel}},
		}}
		require.False(t, ExecutionProtocolRecord(normalNoSpark).IsModelSupported(sparkModel, provideradapter.ModelDefaults(), provideradapter.ModelRules(ExecutionProtocolRecord(normalNoSpark))), "普通提供商显式白名单不含 Spark 时拒绝")
	})

	// Parent credential health constrains shadow eligibility.

	t.Run("parent_health_propagated_to_shadow", func(t *testing.T) {
		// Reset parent health before testing each restriction.
		parent.Record.Status = billing.StatusActive
		parent.Record.Schedulable = true

		lookup := func(id int64) *ExecutionProvider {
			if id == parent.Record.ID {
				return parent
			}
			return nil
		}

		// A healthy parent permits the shadow.
		require.True(t, provider.ParentHealthyForShadow(ExecutionRecord(shadow), func(id int64) *provider.Record {
			return ExecutionRecord(lookup(id))
		}), "健康母提供商时影子应健康")

		// A parent credential error blocks the shadow.
		parent.Record.Status = provider.StatusError
		require.False(t, provider.ParentHealthyForShadow(ExecutionRecord(shadow), func(id int64) *provider.Record {
			return ExecutionRecord(lookup(id))
		}), "Status=error 母提供商时影子应不健康")

		// A scheduling pause leaves the parent credentials usable by the shadow.
		parent.Record.Status = billing.StatusActive
		parent.Record.Schedulable = false
		require.True(t, provider.ParentHealthyForShadow(ExecutionRecord(shadow), func(id int64) *provider.Record {
			return ExecutionRecord(lookup(id))
		}), "母提供商手动暂停不应连坐影子(凭据仍可用)")

		// The shadow uses its own eligibility during a parent-wide rate limit.
		parent.Record.Schedulable = true
		resetAt := time.Now().Add(1 * time.Hour)
		parent.Record.RateLimitResetAt = &resetAt
		require.True(t, provider.ParentHealthyForShadow(ExecutionRecord(shadow), func(id int64) *provider.Record {
			return ExecutionRecord(lookup(id))
		}), "母提供商 global 限流不应连坐 spark 影子")
		parent.Record.RateLimitResetAt = nil

		// An ordinary provider passes without a parent lookup.
		parent.Record.Schedulable = true
		lookupNotCalled := func(_ int64) *ExecutionProvider {
			t.Error("非影子提供商不应调用 lookup")
			return nil
		}
		require.True(t, provider.ParentHealthyForShadow(ExecutionRecord(parent), func(id int64) *provider.Record {
			return ExecutionRecord(lookupNotCalled(id))
		}), "普通提供商应直接返回 true")
	})
}

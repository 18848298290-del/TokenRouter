package testkit

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// HealthyGrokOAuthProvider creates a schedulable Grok OAuth fixture with unexpired credentials.
func HealthyGrokOAuthProvider(id int64, token string) *gatewayadapter.ExecutionProvider {
	return &gatewayadapter.ExecutionProvider{
		Record: provider.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Name:        "grok",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":  token,
				"refresh_token": "refresh-token",
				"expires_at":    time.Now().Add(2 * provider.GrokTokenRefreshSkew).UTC().Format(time.RFC3339),
				"base_url":      xai.DefaultCLIBaseURL,
			},
		},
	}
}

// RequestCredentials wires credential reads and recovery to test dependencies.
func RequestCredentials(store gatewayadapter.ExecutionProviderStore, source *provider.OpenAIExecutionCredentials, tokens *provider.GrokTokenSource, blocks *provider.RuntimeBlockState) *gatewayadapter.RequestCredentials {
	if blocks == nil {
		blocks = provider.NewRuntimeBlockState(time.Now)
	}
	if source == nil {
		source = &provider.OpenAIExecutionCredentials{}
	}
	recovery := &provider.GrokCredentialRecovery{Runtime: blocks, Warn: slog.Warn}
	if store != nil {
		source.Parent = func(ctx context.Context, id int64) (*provider.Record, error) {
			value, err := store.GetByID(ctx, id)
			return gatewayadapter.ExecutionRecord(value), err
		}
		recovery.Read = source.Parent
		recovery.State, _ = store.(provider.GrokCredentialStateWriter)
	}
	if tokens != nil {
		source.Grok = tokens.GetAccessToken
		recovery.Invalidate = tokens.InvalidateToken
	}
	return &gatewayadapter.RequestCredentials{Source: source, HasGrokTokenSource: tokens != nil, Recovery: recovery, Runtime: blocks}
}

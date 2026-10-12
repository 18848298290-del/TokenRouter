package app

import (
	"context"
	"log"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
)

// provideRetryCooldown binds storage and logging; provider code decides when to apply cooldown.
func provideRetryCooldown(store *providerpostgres.ProviderStore) *provider.RetryCooldown {
	var source provider.RetryCooldownStore
	if store != nil {
		source = store
	}
	return provider.NewRetryCooldown(source, provider.RetryCooldownOptions{
		Logf: log.Printf,
		LookupError: func(id int64, err error) {
			logging.LegacyPrintf("service.gateway", "查询重试耗尽提供商失败: provider=%d error=%v", id, err)
		},
	})
}

func messageRetryCooldown(command *provider.RetryCooldown) func(context.Context, int64, *forward.UpstreamFailoverError) {
	return func(ctx context.Context, id int64, failure *forward.UpstreamFailoverError) {
		input := provider.RetryCooldownInput{ProviderID: id}
		if failure != nil {
			input.Status = failure.StatusCode
			input.Retryable = failure.RetryableOnSameProvider
			input.RequestScopedTransient = failure.RequestScopedTransient
		}
		command.Apply(ctx, input)
	}
}

// messageSessionIsolation reads isolation settings from the current key and uses a one-hour owner TTL.
func messageSessionIsolation(cache session.GatewayCache) func(context.Context, *apikey.APIKey, int64, string, string) error {
	return func(ctx context.Context, key *apikey.APIKey, userID int64, source, hash string) error {
		if key == nil {
			return nil
		}
		groupID := int64(0)
		if key.GroupID != nil {
			groupID = *key.GroupID
		}
		return session.EnsureIsolation(ctx, cache, session.IsolationInput{UserID: userID, GroupID: groupID, Source: source, Hash: hash, TTL: time.Hour, Enabled: key.Group != nil && key.Group.SessionIsolationEnabled})
	}
}

// messageDigestFind delegates digest lookup and expiry checks to the shared memory store.
func messageDigestFind(store *session.DigestSessionStore) func(context.Context, int64, string, string) (string, int64, string, bool) {
	return func(_ context.Context, id int64, prefix, chain string) (string, int64, string, bool) {
		return store.Find(id, prefix, chain)
	}
}

func messageDigestSave(store *session.DigestSessionStore) func(context.Context, int64, string, string, string, int64, string) error {
	return func(_ context.Context, id int64, prefix, chain, uuid string, providerID int64, old string) error {
		store.Save(id, prefix, chain, uuid, providerID, old)
		return nil
	}
}

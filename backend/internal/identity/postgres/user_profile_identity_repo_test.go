package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	"github.com/TokenFlux/TokenRouter/ent/identityadoptiondecision"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
)

// Account identity tests reuse historical WeChat keys and start two adoption
// calls for the same SQLite decision. Stored row identifiers and metadata
// expose alias reuse, while the first-create gate holds one mutation and gives
// the second caller a timed scheduling opportunity before both results are read.

// TestUserRepositoryBindAuthIdentityToUserCanonicalizesLegacyWeChatAlias binds
// a canonical WeChat key over legacy identity and channel records for the same
// account. It checks retained IDs, rewritten keys and metadata, and one stored
// row per tuple.
func TestUserRepositoryBindAuthIdentityToUserCanonicalizesLegacyWeChatAlias(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

	user := &identitycore.User{
		Email:        "wechat-legacy@example.com",
		Username:     "wechat-legacy",
		PasswordHash: "hash",
		Role:         identitycore.RoleUser,
		Status:       identitycore.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, user))

	legacyIdentity, err := client.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProviderType("wechat").
		SetProviderKey("wechat").
		SetProviderSubject("union-legacy-123").
		SetMetadata(map[string]any{"source": "legacy-alias"}).
		Save(ctx)
	require.NoError(t, err)

	legacyChannel, err := client.AuthIdentityChannel.Create().
		SetIdentityID(legacyIdentity.ID).
		SetProviderType("wechat").
		SetProviderKey("wechat").
		SetChannel("oa").
		SetChannelAppID("wx-app-legacy").
		SetChannelSubject("openid-legacy-123").
		SetMetadata(map[string]any{"scene": "legacy-alias"}).
		Save(ctx)
	require.NoError(t, err)

	bound, err := repo.BindAuthIdentityToUser(ctx, BindAuthIdentityInput{
		UserID: user.ID,
		Canonical: AuthIdentityKey{
			ProviderType:    "wechat",
			ProviderKey:     "wechat-main",
			ProviderSubject: "union-legacy-123",
		},
		Channel: &AuthIdentityChannelKey{
			ProviderType:   "wechat",
			ProviderKey:    "wechat-main",
			Channel:        "oa",
			ChannelAppID:   "wx-app-legacy",
			ChannelSubject: "openid-legacy-123",
		},
		Metadata:        map[string]any{"source": "canonical-bind"},
		ChannelMetadata: map[string]any{"scene": "canonical-bind"},
	})
	require.NoError(t, err)
	require.NotNil(t, bound)
	require.NotNil(t, bound.Identity)
	require.NotNil(t, bound.Channel)
	require.Equal(t, legacyIdentity.ID, bound.Identity.ID)
	require.Equal(t, legacyChannel.ID, bound.Channel.ID)
	require.Equal(t, "wechat-main", bound.Identity.ProviderKey)
	require.Equal(t, "wechat-main", bound.Channel.ProviderKey)

	reloadedIdentity, err := client.AuthIdentity.Get(ctx, legacyIdentity.ID)
	require.NoError(t, err)
	require.Equal(t, "wechat-main", reloadedIdentity.ProviderKey)
	require.Equal(t, "canonical-bind", reloadedIdentity.Metadata["source"])

	reloadedChannel, err := client.AuthIdentityChannel.Get(ctx, legacyChannel.ID)
	require.NoError(t, err)
	require.Equal(t, "wechat-main", reloadedChannel.ProviderKey)
	require.Equal(t, "canonical-bind", reloadedChannel.Metadata["scene"])

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("wechat"),
			authidentity.ProviderSubjectEQ("union-legacy-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, identityCount)

	channelCount, err := client.AuthIdentityChannel.Query().
		Where(
			authidentitychannel.ProviderTypeEQ("wechat"),
			authidentitychannel.ChannelEQ("oa"),
			authidentitychannel.ChannelAppIDEQ("wx-app-legacy"),
			authidentitychannel.ChannelSubjectEQ("openid-legacy-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, channelCount)
}

// TestUserRepositoryUpsertIdentityAdoptionDecisionIsIdempotentUnderConcurrency
// blocks the first adoption create while starting another call for the same
// session and identity. It checks matching returned IDs, one stored decision,
// and retained identity and adoption flags.
func TestUserRepositoryUpsertIdentityAdoptionDecisionIsIdempotentUnderConcurrency(t *testing.T) {
	repo, client := newUserEntRepo(t)
	ctx := context.Background()

	user := &identitycore.User{
		Email:        "repo-adoption@example.com",
		Username:     "repo-adoption",
		PasswordHash: "hash",
		Role:         identitycore.RoleUser,
		Status:       identitycore.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, user))

	identity, err := client.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProviderType("wechat").
		SetProviderKey("wechat-main").
		SetProviderSubject("union-repo-adoption").
		SetMetadata(map[string]any{}).
		Save(ctx)
	require.NoError(t, err)

	session, err := client.PendingAuthSession.Create().
		SetSessionToken("pending-repo-adoption").
		SetIntent("bind_current_user").
		SetProviderType("wechat").
		SetProviderKey("wechat-main").
		SetProviderSubject("union-repo-adoption").
		SetExpiresAt(time.Now().UTC().Add(15 * time.Minute)).
		SetUpstreamIdentityClaims(map[string]any{"provider_subject": "union-repo-adoption"}).
		SetLocalFlowState(map[string]any{"step": "pending"}).
		Save(ctx)
	require.NoError(t, err)

	// Gate the first create after it enters mutation, then launch the second
	// writer before release. Result collection accepts either finishing order;
	// the timed start does not establish the second writer's database position.
	firstCreateStarted := make(chan struct{})
	releaseFirstCreate := make(chan struct{})
	var firstCreate sync.Once
	client.IdentityAdoptionDecision.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			blocked := false
			if m.Op().Is(dbent.OpCreate) {
				firstCreate.Do(func() {
					blocked = true
					close(firstCreateStarted)
				})
			}
			if blocked {
				<-releaseFirstCreate
			}
			return next.Mutate(ctx, m)
		})
	})

	// adoptionResult transports a concurrent decision result and its persistence
	// failure for assertion.
	type adoptionResult struct {
		decision *dbent.IdentityAdoptionDecision // returned stored adoption decision
		err      error                           // returned adoption persistence failure
	}

	input := IdentityAdoptionDecisionInput{
		PendingAuthSessionID: session.ID,
		IdentityID:           &identity.ID,
		AdoptDisplayName:     true,
		AdoptAvatar:          true,
	}

	results := make(chan adoptionResult, 2)
	go func() {
		decision, err := repo.UpsertIdentityAdoptionDecision(ctx, input)
		results <- adoptionResult{decision: decision, err: err}
	}()

	<-firstCreateStarted

	go func() {
		decision, err := repo.UpsertIdentityAdoptionDecision(ctx, input)
		results <- adoptionResult{decision: decision, err: err}
	}()

	// Give the second writer a scheduling opportunity before releasing the first
	// create. The elapsed delay does not itself prove that the second goroutine
	// reached a particular database stage.
	time.Sleep(100 * time.Millisecond)
	close(releaseFirstCreate)

	first := <-results
	second := <-results

	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotNil(t, first.decision)
	require.NotNil(t, second.decision)
	require.Equal(t, first.decision.ID, second.decision.ID)

	count, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	loaded, err := client.IdentityAdoptionDecision.Query().
		Where(identityadoptiondecision.PendingAuthSessionIDEQ(session.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, loaded.IdentityID)
	require.Equal(t, identity.ID, *loaded.IdentityID)
	require.True(t, loaded.AdoptDisplayName)
	require.True(t, loaded.AdoptAvatar)
}

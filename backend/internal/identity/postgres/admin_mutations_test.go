package postgres

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	"github.com/TokenFlux/TokenRouter/ent/enttest"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Administrator binding tests exercise Ent-backed identity and channel writes
// on SQLite while a fixture supplies the selected account. Assertions inspect
// canonical provider keys, retained row counts, metadata, owner conflicts, and
// provider validation; they do not establish PostgreSQL concurrency behavior.

// bindingUsersFixture supplies a fixed account for administrator
// identity-binding validation around Ent writes.
type bindingUsersFixture struct {
	// UserRepository leaves unimplemented fixture operations unavailable. These
	// binding cases call the supplied GetByID method; an unexpected promoted
	// operation reaches the nil embedded repository.
	identitycore.UserRepository
	user *identitycore.User // fixed binding target account
}

// TestAdminServiceBindUserAuthIdentityCreatesCanonicalAndChannelBinding
// creates a WeChat subject with one scoped channel and supplied metadata. It
// checks returned verification state plus the stored owner, canonical key,
// channel link, and metadata.
func TestAdminServiceBindUserAuthIdentityCreatesCanonicalAndChannelBinding(t *testing.T) {
	client := newAdminServiceAuthIdentityBindingTestClient(t)
	ctx := context.Background()

	user, err := client.User.Create().
		SetEmail("bind-target@example.com").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	svc := newIdentityBindingAdminForTest(client, &identitycore.User{ID: user.ID, Email: user.Email, Status: billing.StatusActive})

	result, err := svc.BindUserAuthIdentity(ctx, user.ID, identitycore.AdminBindAuthIdentityInput{
		ProviderType:    "wechat",
		ProviderKey:     "wechat-main",
		ProviderSubject: "union-123",
		Metadata:        map[string]any{"source": "admin-repair"},
		Channel: &identitycore.AdminBindAuthIdentityChannelInput{
			Channel:        "open",
			ChannelAppID:   "wx-open",
			ChannelSubject: "openid-123",
			Metadata:       map[string]any{"scene": "migration"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, user.ID, result.UserID)
	require.Equal(t, "wechat", result.ProviderType)
	require.Equal(t, "wechat-main", result.ProviderKey)
	require.NotNil(t, result.VerifiedAt)
	require.NotNil(t, result.Channel)
	require.Equal(t, "open", result.Channel.Channel)

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("wechat"),
			authidentity.ProviderKeyEQ("wechat-main"),
			authidentity.ProviderSubjectEQ("union-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, user.ID, identity.UserID)
	require.Equal(t, "admin-repair", identity.Metadata["source"])
	require.NotNil(t, identity.VerifiedAt)

	channel, err := client.AuthIdentityChannel.Query().
		Where(
			authidentitychannel.ProviderTypeEQ("wechat"),
			authidentitychannel.ProviderKeyEQ("wechat-main"),
			authidentitychannel.ChannelEQ("open"),
			authidentitychannel.ChannelAppIDEQ("wx-open"),
			authidentitychannel.ChannelSubjectEQ("openid-123"),
		).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, identity.ID, channel.IdentityID)
	require.Equal(t, "migration", channel.Metadata["scene"])
}

// TestAdminServiceBindUserAuthIdentityRejectsOtherOwner supplies an OIDC
// subject already stored for a different current account. It checks the
// ownership conflict returned when binding that subject to the fixture target.
func TestAdminServiceBindUserAuthIdentityRejectsOtherOwner(t *testing.T) {
	client := newAdminServiceAuthIdentityBindingTestClient(t)
	ctx := context.Background()

	owner, err := client.User.Create().
		SetEmail("owner@example.com").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	target, err := client.User.Create().
		SetEmail("target@example.com").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.AuthIdentity.Create().
		SetUserID(owner.ID).
		SetProviderType("oidc").
		SetProviderKey("https://issuer.example").
		SetProviderSubject("subject-1").
		Save(ctx)
	require.NoError(t, err)

	svc := newIdentityBindingAdminForTest(client, &identitycore.User{ID: target.ID, Email: target.Email, Status: billing.StatusActive})

	_, err = svc.BindUserAuthIdentity(ctx, target.ID, identitycore.AdminBindAuthIdentityInput{
		ProviderType:    "oidc",
		ProviderKey:     "https://issuer.example",
		ProviderSubject: "subject-1",
	})
	require.Error(t, err)
	require.Equal(t, "AUTH_IDENTITY_OWNERSHIP_CONFLICT", apperror.Reason(err))
}

// TestAdminServiceBindUserAuthIdentityIsIdempotentForSameUser binds the same
// OIDC subject twice for one account with different metadata. It checks one
// stored identity row and the replacement metadata on the second result.
func TestAdminServiceBindUserAuthIdentityIsIdempotentForSameUser(t *testing.T) {
	client := newAdminServiceAuthIdentityBindingTestClient(t)
	ctx := context.Background()

	user, err := client.User.Create().
		SetEmail("same-user@example.com").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	svc := newIdentityBindingAdminForTest(client, &identitycore.User{ID: user.ID, Email: user.Email, Status: billing.StatusActive})

	first, err := svc.BindUserAuthIdentity(ctx, user.ID, identitycore.AdminBindAuthIdentityInput{
		ProviderType:    "oidc",
		ProviderKey:     "https://issuer.example",
		ProviderSubject: "subject-2",
		Metadata:        map[string]any{"source": "first"},
	})
	require.NoError(t, err)

	second, err := svc.BindUserAuthIdentity(ctx, user.ID, identitycore.AdminBindAuthIdentityInput{
		ProviderType:    "oidc",
		ProviderKey:     "https://issuer.example",
		ProviderSubject: "subject-2",
		Metadata:        map[string]any{"source": "second"},
	})
	require.NoError(t, err)
	require.Equal(t, first.UserID, second.UserID)
	require.Equal(t, "second", second.Metadata["source"])

	identities, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("oidc"),
			authidentity.ProviderKeyEQ("https://issuer.example"),
			authidentity.ProviderSubjectEQ("subject-2"),
		).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, identities, 1)
	require.Equal(t, "second", identities[0].Metadata["source"])
}

// TestAdminServiceBindUserAuthIdentityReusesLegacyWeChatAliasRecords starts
// with legacy WeChat identity and channel keys before manual binding requests
// the canonical key. It checks reused IDs, rewritten keys and metadata, and
// one row per identity/channel.
func TestAdminServiceBindUserAuthIdentityReusesLegacyWeChatAliasRecords(t *testing.T) {
	client := newAdminServiceAuthIdentityBindingTestClient(t)
	ctx := context.Background()

	user, err := client.User.Create().
		SetEmail("wechat-alias@example.com").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	legacyIdentity, err := client.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProviderType("wechat").
		SetProviderKey("wechat").
		SetProviderSubject("union-legacy-123").
		SetMetadata(map[string]any{"source": "legacy"}).
		Save(ctx)
	require.NoError(t, err)

	legacyChannel, err := client.AuthIdentityChannel.Create().
		SetIdentityID(legacyIdentity.ID).
		SetProviderType("wechat").
		SetProviderKey("wechat").
		SetChannel("open").
		SetChannelAppID("wx-open").
		SetChannelSubject("openid-legacy-123").
		SetMetadata(map[string]any{"scene": "legacy"}).
		Save(ctx)
	require.NoError(t, err)

	svc := newIdentityBindingAdminForTest(client, &identitycore.User{ID: user.ID, Email: user.Email, Status: billing.StatusActive})

	result, err := svc.BindUserAuthIdentity(ctx, user.ID, identitycore.AdminBindAuthIdentityInput{
		ProviderType:    "wechat",
		ProviderKey:     "wechat-main",
		ProviderSubject: "union-legacy-123",
		Metadata:        map[string]any{"source": "admin-repair"},
		Channel: &identitycore.AdminBindAuthIdentityChannelInput{
			Channel:        "open",
			ChannelAppID:   "wx-open",
			ChannelSubject: "openid-legacy-123",
			Metadata:       map[string]any{"scene": "admin-repair"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "wechat-main", result.ProviderKey)
	require.NotNil(t, result.Channel)
	require.Equal(t, "open", result.Channel.Channel)

	identity, err := client.AuthIdentity.Get(ctx, legacyIdentity.ID)
	require.NoError(t, err)
	require.Equal(t, "wechat-main", identity.ProviderKey)
	require.Equal(t, "admin-repair", identity.Metadata["source"])

	channel, err := client.AuthIdentityChannel.Get(ctx, legacyChannel.ID)
	require.NoError(t, err)
	require.Equal(t, "wechat-main", channel.ProviderKey)
	require.Equal(t, legacyIdentity.ID, channel.IdentityID)
	require.Equal(t, "admin-repair", channel.Metadata["scene"])

	identityCount, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("wechat"),
			authidentity.ProviderSubjectEQ("union-legacy-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, identityCount)

	channelCount, err := client.AuthIdentityChannel.Query().
		Where(
			authidentitychannel.ProviderTypeEQ("wechat"),
			authidentitychannel.ChannelEQ("open"),
			authidentitychannel.ChannelAppIDEQ("wx-open"),
			authidentitychannel.ChannelSubjectEQ("openid-legacy-123"),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, channelCount)
}

// TestAdminServiceBindUserAuthIdentityRejectsInvalidProviderType submits
// GitHub as an unsupported manual-binding provider for the fixture account. It
// checks the service rejection reason before a supported provider binding can
// be saved.
func TestAdminServiceBindUserAuthIdentityRejectsInvalidProviderType(t *testing.T) {
	client := newAdminServiceAuthIdentityBindingTestClient(t)
	ctx := context.Background()

	user, err := client.User.Create().
		SetEmail("invalid-provider@example.com").
		SetPasswordHash("hash").
		SetRole(identitycore.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	svc := newIdentityBindingAdminForTest(client, &identitycore.User{ID: user.ID, Email: user.Email, Status: billing.StatusActive})

	_, err = svc.BindUserAuthIdentity(ctx, user.ID, identitycore.AdminBindAuthIdentityInput{
		ProviderType:    "github",
		ProviderKey:     "github-main",
		ProviderSubject: "subject-3",
	})
	require.Error(t, err)
	require.Equal(t, "INVALID_INPUT", apperror.Reason(err))
}

// newAdminServiceAuthIdentityBindingTestClient creates the shared-name SQLite
// binding fixture with foreign keys enabled. Cleanup closes the Ent client
// before its underlying pool, and callers run these fixtures sequentially.
func newAdminServiceAuthIdentityBindingTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", "file:admin_service_auth_identity_binding?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	// Register pool cleanup before later fixture setup so initialization failure
	// still closes it. Ent cleanup is added afterward and runs first; both close
	// errors are discarded.
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	// Close the Ent client before the earlier registered pool cleanup runs, so
	// driver teardown precedes the final pool close at fixture completion.
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// GetByID returns the fixed fixture account pointer for administrator
// validation. The requested ID is ignored, so tests prepare the account
// corresponding to the route or service target.
func (s bindingUsersFixture) GetByID(context.Context, int64) (*identitycore.User, error) {
	return s.user, nil
}

// newIdentityBindingAdminForTest constructs administrator use cases with
// SQLite transaction-backed binding and a fixed account reader. Other embedded
// repository operations remain unavailable, isolating identity persistence and
// provider validation.
func newIdentityBindingAdminForTest(client *dbent.Client, user *identitycore.User) *identitycore.UserAdmin {
	users := bindingUsersFixture{user: user}
	return identitycore.NewUserAdmin(identitycore.AdminDependencies{Users: users, Transactions: &AdminMutations{Client: client, Users: users}})
}

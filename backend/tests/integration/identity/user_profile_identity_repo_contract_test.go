//go:build integration

package identity_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
)

// Canonical provider subjects and channel-specific subjects locate the same
// persisted identity owner, while profile adoption decisions can move their
// identity reference between pending sessions. PostgreSQL suite cases check
// binding reuse, ownership conflicts, legacy WeChat aliases, first-binding
// grant records, avatars, and dedicated login and activity timestamps through a
// shared cloned database.

// UserProfileIdentityRepoSuite retains a cloned PostgreSQL database for
// identity and profile storage cases.
type UserProfileIdentityRepoSuite struct {
	// db and client share the clone across sequential cases. SetupTest replaces
	// ctx and repo and clears selected fixture tables; clone cleanup owns
	// connection closure.
	db          *sql.DB             // borrowed suite connection pool
	suite.Suite                     // assertion and suite lifecycle support
	ctx         context.Context     // case execution context
	client      *dbent.Client       // borrowed suite Ent client
	repo        *postgres.UserStore // case profile storage fixture
}

// sqlNullTime records the driver timestamp value and whether SQL supplied a
// nonnull time.
type sqlNullTime struct {
	// Time and Valid change together during Scan. A time.Time input stores its
	// value and marks Valid true; SQL NULL clears Time and marks Valid false.
	Time  time.Time // scanned timestamp value
	Valid bool      // whether a timestamp was scanned
}

// TestUserProfileIdentityRepoSuite runs canonical identity and profile
// persistence cases against PostgreSQL. Its fixtures inspect binding ownership,
// channel records, adoption links, grant deduplication, avatar values, and
// timestamp writes.
func TestUserProfileIdentityRepoSuite(t *testing.T) {
	suite.Run(t, new(UserProfileIdentityRepoSuite))
}

// SetupSuite obtains one migrated PostgreSQL clone shared by the
// profile-identity suite. Its pool and Ent client remain alive across cases and
// are closed by the registered database cleanup afterward.
func (s *UserProfileIdentityRepoSuite) SetupSuite() {
	s.db, s.client = identityDatabase(s.T())
}

// SetupTest replaces the case context and user store, then truncates profile,
// identity, pending, and grant fixture tables. It registers cascading user
// truncation after each case, and both cleanup statements assert database
// success.
func (s *UserProfileIdentityRepoSuite) SetupTest() {
	s.ctx = context.Background()
	// Clear users with cascading dependencies after each case, including ID reset,
	// while keeping the shared cloned database open for the next suite fixture
	// setup.
	s.T().Cleanup(func() {
		_, err := s.db.ExecContext(context.Background(), "TRUNCATE users RESTART IDENTITY CASCADE")
		s.Require().NoError(err)
	})
	s.repo = postgres.NewUserStoreWithSQL(s.client, s.db)

	// Truncate the related identity, pending, grant, and avatar tables together
	// before the next case so fixed provider subjects and adoption references can
	// be reused.
	_, err := s.db.ExecContext(s.ctx, `
TRUNCATE TABLE
	identity_adoption_decisions,
	auth_identity_channels,
	auth_identities,
	pending_auth_sessions,
	user_provider_default_grants,
	user_avatars
RESTART IDENTITY`)
	s.Require().NoError(err)
}

// mustCreateUser inserts an active ordinary user using label and the current
// nanosecond timestamp for its email. Save failures fail the suite case before
// dependent identity fixture creation begins.
func (s *UserProfileIdentityRepoSuite) mustCreateUser(label string) *dbent.User {
	s.T().Helper()

	user, err := s.client.User.Create().
		SetEmail(fmt.Sprintf("%s-%d@example.com", label, time.Now().UnixNano())).
		SetPasswordHash("test-password-hash").
		SetRole("user").
		SetStatus("active").
		Save(s.ctx)
	s.Require().NoError(err)
	return user
}

// mustCreatePendingAuthSession inserts a bind-current-user session for the
// supplied provider tuple with a fifteen-minute expiry. Fixture claims and
// local state identify its subject and pending step; save errors fail the case.
func (s *UserProfileIdentityRepoSuite) mustCreatePendingAuthSession(key postgres.AuthIdentityKey) *dbent.PendingAuthSession {
	s.T().Helper()

	session, err := s.client.PendingAuthSession.Create().
		SetSessionToken(fmt.Sprintf("pending-%d", time.Now().UnixNano())).
		SetIntent("bind_current_user").
		SetProviderType(key.ProviderType).
		SetProviderKey(key.ProviderKey).
		SetProviderSubject(key.ProviderSubject).
		SetExpiresAt(time.Now().UTC().Add(15 * time.Minute)).
		SetUpstreamIdentityClaims(map[string]any{"provider_subject": key.ProviderSubject}).
		SetLocalFlowState(map[string]any{"step": "pending"}).
		Save(s.ctx)
	s.Require().NoError(err)
	return session
}

// TestCreateAndLookupCanonicalAndChannelIdentity creates a WeChat union subject
// with an MP channel subject. Canonical and channel lookups return the same
// owner and their expected persisted identity and channel IDs.
func (s *UserProfileIdentityRepoSuite) TestCreateAndLookupCanonicalAndChannelIdentity() {
	user := s.mustCreateUser("canonical-channel")

	// Round verification time to whole seconds for the stored identity fixture.
	verifiedAt := time.Now().UTC().Truncate(time.Second)
	created, err := s.repo.CreateAuthIdentity(s.ctx, postgres.CreateAuthIdentityInput{
		UserID: user.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "wechat",
			ProviderKey:     "wechat-open",
			ProviderSubject: "union-123",
		},
		Channel: &postgres.AuthIdentityChannelKey{
			ProviderType:   "wechat",
			ProviderKey:    "wechat-open",
			Channel:        "mp",
			ChannelAppID:   "wx-app",
			ChannelSubject: "openid-123",
		},
		Issuer:          stringPtr("https://issuer.example"),
		VerifiedAt:      &verifiedAt,
		Metadata:        map[string]any{"unionid": "union-123"},
		ChannelMetadata: map[string]any{"openid": "openid-123"},
	})
	s.Require().NoError(err)
	s.Require().NotNil(created.Identity)
	s.Require().NotNil(created.Channel)

	canonical, err := s.repo.GetUserByCanonicalIdentity(s.ctx, created.IdentityRef())
	s.Require().NoError(err)
	s.Require().Equal(user.ID, canonical.User.ID)
	s.Require().Equal(created.Identity.ID, canonical.Identity.ID)
	s.Require().Equal("union-123", canonical.Identity.ProviderSubject)

	channel, err := s.repo.GetUserByChannelIdentity(s.ctx, *created.ChannelRef())
	s.Require().NoError(err)
	s.Require().Equal(user.ID, channel.User.ID)
	s.Require().Equal(created.Identity.ID, channel.Identity.ID)
	s.Require().Equal(created.Channel.ID, channel.Channel.ID)
}

// TestBindAuthIdentityToUser_IsIdempotentAndRejectsOtherOwners binds one
// LinuxDo subject twice for its owner with changed metadata. IDs are reused,
// metadata updates, and another owner receives canonical or channel ownership
// conflicts.
func (s *UserProfileIdentityRepoSuite) TestBindAuthIdentityToUser_IsIdempotentAndRejectsOtherOwners() {
	owner := s.mustCreateUser("owner")
	other := s.mustCreateUser("other")

	first, err := s.repo.BindAuthIdentityToUser(s.ctx, postgres.BindAuthIdentityInput{
		UserID: owner.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "linuxdo",
			ProviderKey:     "linuxdo-main",
			ProviderSubject: "subject-1",
		},
		Channel: &postgres.AuthIdentityChannelKey{
			ProviderType:   "linuxdo",
			ProviderKey:    "linuxdo-main",
			Channel:        "oauth",
			ChannelAppID:   "linuxdo-web",
			ChannelSubject: "subject-1",
		},
		Metadata:        map[string]any{"username": "first"},
		ChannelMetadata: map[string]any{"scope": "read"},
	})
	s.Require().NoError(err)

	second, err := s.repo.BindAuthIdentityToUser(s.ctx, postgres.BindAuthIdentityInput{
		UserID: owner.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "linuxdo",
			ProviderKey:     "linuxdo-main",
			ProviderSubject: "subject-1",
		},
		Channel: &postgres.AuthIdentityChannelKey{
			ProviderType:   "linuxdo",
			ProviderKey:    "linuxdo-main",
			Channel:        "oauth",
			ChannelAppID:   "linuxdo-web",
			ChannelSubject: "subject-1",
		},
		Metadata:        map[string]any{"username": "second"},
		ChannelMetadata: map[string]any{"scope": "write"},
	})
	s.Require().NoError(err)
	s.Require().Equal(first.Identity.ID, second.Identity.ID)
	s.Require().Equal(first.Channel.ID, second.Channel.ID)
	s.Require().Equal("second", second.Identity.Metadata["username"])
	s.Require().Equal("write", second.Channel.Metadata["scope"])

	_, err = s.repo.BindAuthIdentityToUser(s.ctx, postgres.BindAuthIdentityInput{
		UserID: other.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "linuxdo",
			ProviderKey:     "linuxdo-main",
			ProviderSubject: "subject-1",
		},
	})
	s.Require().ErrorIs(err, postgres.ErrAuthIdentityOwnershipConflict)

	_, err = s.repo.BindAuthIdentityToUser(s.ctx, postgres.BindAuthIdentityInput{
		UserID: other.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "linuxdo",
			ProviderKey:     "linuxdo-main",
			ProviderSubject: "subject-2",
		},
		Channel: &postgres.AuthIdentityChannelKey{
			ProviderType:   "linuxdo",
			ProviderKey:    "linuxdo-main",
			Channel:        "oauth",
			ChannelAppID:   "linuxdo-web",
			ChannelSubject: "subject-1",
		},
	})
	s.Require().ErrorIs(err, postgres.ErrAuthIdentityChannelOwnershipConflict)
}

// TestBindAuthIdentityToUser_ReusesLegacyWeChatAliasRecords seeds WeChat
// identities under the legacy provider key then binds its canonical key.
// Identity and channel IDs are retained, keys and metadata update, and row
// counts stay one.
func (s *UserProfileIdentityRepoSuite) TestBindAuthIdentityToUser_ReusesLegacyWeChatAliasRecords() {
	user := s.mustCreateUser("wechat-legacy-alias")

	legacyIdentity, err := s.client.AuthIdentity.Create().
		SetUserID(user.ID).
		SetProviderType("wechat").
		SetProviderKey("wechat").
		SetProviderSubject("union-legacy-123").
		SetMetadata(map[string]any{"source": "legacy-alias"}).
		Save(s.ctx)
	s.Require().NoError(err)

	legacyChannel, err := s.client.AuthIdentityChannel.Create().
		SetIdentityID(legacyIdentity.ID).
		SetProviderType("wechat").
		SetProviderKey("wechat").
		SetChannel("oa").
		SetChannelAppID("wx-app-legacy").
		SetChannelSubject("openid-legacy-123").
		SetMetadata(map[string]any{"scene": "legacy-alias"}).
		Save(s.ctx)
	s.Require().NoError(err)

	bound, err := s.repo.BindAuthIdentityToUser(s.ctx, postgres.BindAuthIdentityInput{
		UserID: user.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "wechat",
			ProviderKey:     "wechat-main",
			ProviderSubject: "union-legacy-123",
		},
		Channel: &postgres.AuthIdentityChannelKey{
			ProviderType:   "wechat",
			ProviderKey:    "wechat-main",
			Channel:        "oa",
			ChannelAppID:   "wx-app-legacy",
			ChannelSubject: "openid-legacy-123",
		},
		Metadata:        map[string]any{"source": "canonical-bind"},
		ChannelMetadata: map[string]any{"scene": "canonical-bind"},
	})
	s.Require().NoError(err)
	s.Require().NotNil(bound)
	s.Require().NotNil(bound.Identity)
	s.Require().NotNil(bound.Channel)
	s.Require().Equal(legacyIdentity.ID, bound.Identity.ID)
	s.Require().Equal(legacyChannel.ID, bound.Channel.ID)
	s.Require().Equal("wechat-main", bound.Identity.ProviderKey)
	s.Require().Equal("wechat-main", bound.Channel.ProviderKey)
	s.Require().Equal("canonical-bind", bound.Identity.Metadata["source"])
	s.Require().Equal("canonical-bind", bound.Channel.Metadata["scene"])

	identityCount, err := s.client.AuthIdentity.Query().
		Where(
			authidentity.UserIDEQ(user.ID),
			authidentity.ProviderTypeEQ("wechat"),
			authidentity.ProviderSubjectEQ("union-legacy-123"),
		).
		Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(1, identityCount)

	channelCount, err := s.client.AuthIdentityChannel.Query().
		Where(
			authidentitychannel.ProviderTypeEQ("wechat"),
			authidentitychannel.ChannelEQ("oa"),
			authidentitychannel.ChannelAppIDEQ("wx-app-legacy"),
			authidentitychannel.ChannelSubjectEQ("openid-legacy-123"),
		).
		Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(1, channelCount)
}

// TestCreateAuthIdentity_RejectsChannelProviderMismatch combines a WeChat
// canonical identity with a LinuxDo channel in one create input. The repository
// returns AuthIdentityChannelProviderMismatch for that mismatched provider
// tuple.
func (s *UserProfileIdentityRepoSuite) TestCreateAuthIdentity_RejectsChannelProviderMismatch() {
	user := s.mustCreateUser("provider-mismatch-create")

	_, err := s.repo.CreateAuthIdentity(s.ctx, postgres.CreateAuthIdentityInput{
		UserID: user.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "wechat",
			ProviderKey:     "wechat-main",
			ProviderSubject: "union-create-mismatch",
		},
		Channel: &postgres.AuthIdentityChannelKey{
			ProviderType:   "linuxdo",
			ProviderKey:    "linuxdo-main",
			Channel:        "oauth",
			ChannelAppID:   "app-mismatch",
			ChannelSubject: "openid-create-mismatch",
		},
	})
	s.Require().ErrorIs(err, postgres.ErrAuthIdentityChannelProviderMismatch)
}

// TestBindAuthIdentityToUser_RejectsChannelProviderMismatch supplies matching
// WeChat types with different canonical and channel provider keys. Binding
// returns AuthIdentityChannelProviderMismatch for that attempted pairing in the
// fixture.
func (s *UserProfileIdentityRepoSuite) TestBindAuthIdentityToUser_RejectsChannelProviderMismatch() {
	user := s.mustCreateUser("provider-mismatch-bind")

	_, err := s.repo.BindAuthIdentityToUser(s.ctx, postgres.BindAuthIdentityInput{
		UserID: user.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "wechat",
			ProviderKey:     "wechat-main",
			ProviderSubject: "union-bind-mismatch",
		},
		Channel: &postgres.AuthIdentityChannelKey{
			ProviderType:   "wechat",
			ProviderKey:    "wechat-legacy",
			Channel:        "oa",
			ChannelAppID:   "wx-app-bind-mismatch",
			ChannelSubject: "openid-bind-mismatch",
		},
	})
	s.Require().ErrorIs(err, postgres.ErrAuthIdentityChannelProviderMismatch)
}

// TestWithUserProfileIdentityTx_RollsBackIdentityAndGrantOnError inserts a
// canonical identity and provider grant before returning a callback failure.
// Outside reads find neither identity ownership nor a retained grant row.
func (s *UserProfileIdentityRepoSuite) TestWithUserProfileIdentityTx_RollsBackIdentityAndGrantOnError() {
	user := s.mustCreateUser("tx-rollback")
	expectedErr := errors.New("rollback")

	// Create both the identity and its first-binding grant on the selected
	// transaction context before injecting failure, exposing either write if
	// rollback is missed.
	err := s.repo.WithUserProfileIdentityTx(s.ctx, func(txCtx context.Context) error {
		_, err := s.repo.CreateAuthIdentity(txCtx, postgres.CreateAuthIdentityInput{
			UserID: user.ID,
			Canonical: postgres.AuthIdentityKey{
				ProviderType:    "oidc",
				ProviderKey:     "https://issuer.example",
				ProviderSubject: "subject-rollback",
			},
		})
		s.Require().NoError(err)

		inserted, err := s.repo.RecordProviderGrant(txCtx, postgres.ProviderGrantRecordInput{
			UserID:       user.ID,
			ProviderType: "oidc",
			GrantReason:  postgres.ProviderGrantReasonFirstBind,
		})
		s.Require().NoError(err)
		s.Require().True(inserted)
		return expectedErr
	})
	s.Require().ErrorIs(err, expectedErr)

	_, err = s.repo.GetUserByCanonicalIdentity(s.ctx, postgres.AuthIdentityKey{
		ProviderType:    "oidc",
		ProviderKey:     "https://issuer.example",
		ProviderSubject: "subject-rollback",
	})
	s.Require().True(dbent.IsNotFound(err))

	var count int
	s.Require().NoError(s.db.QueryRowContext(s.ctx, `
SELECT COUNT(*)
FROM user_provider_default_grants
WHERE user_id = $1 AND provider_type = $2 AND grant_reason = $3`,
		user.ID,
		"oidc",
		string(postgres.ProviderGrantReasonFirstBind),
	).Scan(&count))
	s.Require().Zero(count)
}

// TestRecordProviderGrant_IsIdempotentPerReason records a first-bind grant
// twice and a signup grant once for one provider and user. The duplicate is
// unapplied while two distinct reason rows remain.
func (s *UserProfileIdentityRepoSuite) TestRecordProviderGrant_IsIdempotentPerReason() {
	user := s.mustCreateUser("grant")

	inserted, err := s.repo.RecordProviderGrant(s.ctx, postgres.ProviderGrantRecordInput{
		UserID:       user.ID,
		ProviderType: "wechat",
		GrantReason:  postgres.ProviderGrantReasonFirstBind,
	})
	s.Require().NoError(err)
	s.Require().True(inserted)

	inserted, err = s.repo.RecordProviderGrant(s.ctx, postgres.ProviderGrantRecordInput{
		UserID:       user.ID,
		ProviderType: "wechat",
		GrantReason:  postgres.ProviderGrantReasonFirstBind,
	})
	s.Require().NoError(err)
	s.Require().False(inserted)

	inserted, err = s.repo.RecordProviderGrant(s.ctx, postgres.ProviderGrantRecordInput{
		UserID:       user.ID,
		ProviderType: "wechat",
		GrantReason:  postgres.ProviderGrantReasonSignup,
	})
	s.Require().NoError(err)
	s.Require().True(inserted)

	var count int
	s.Require().NoError(s.db.QueryRowContext(s.ctx, `
SELECT COUNT(*)
FROM user_provider_default_grants
WHERE user_id = $1 AND provider_type = $2`,
		user.ID,
		"wechat",
	).Scan(&count))
	s.Require().Equal(2, count)
}

// TestUpsertIdentityAdoptionDecision_PersistsAndLinksIdentity updates one
// pending-session decision to adopt an avatar and link its identity. The
// decision ID is reused and reloading returns the selected identity reference.
func (s *UserProfileIdentityRepoSuite) TestUpsertIdentityAdoptionDecision_PersistsAndLinksIdentity() {
	user := s.mustCreateUser("adoption")
	identity, err := s.repo.CreateAuthIdentity(s.ctx, postgres.CreateAuthIdentityInput{
		UserID: user.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "wechat",
			ProviderKey:     "wechat-open",
			ProviderSubject: "union-adoption",
		},
	})
	s.Require().NoError(err)

	session := s.mustCreatePendingAuthSession(identity.IdentityRef())

	first, err := s.repo.UpsertIdentityAdoptionDecision(s.ctx, postgres.IdentityAdoptionDecisionInput{
		PendingAuthSessionID: session.ID,
		AdoptDisplayName:     true,
		AdoptAvatar:          false,
	})
	s.Require().NoError(err)
	s.Require().True(first.AdoptDisplayName)
	s.Require().False(first.AdoptAvatar)
	s.Require().Nil(first.IdentityID)

	second, err := s.repo.UpsertIdentityAdoptionDecision(s.ctx, postgres.IdentityAdoptionDecisionInput{
		PendingAuthSessionID: session.ID,
		IdentityID:           &identity.Identity.ID,
		AdoptDisplayName:     true,
		AdoptAvatar:          true,
	})
	s.Require().NoError(err)
	s.Require().Equal(first.ID, second.ID)
	s.Require().NotNil(second.IdentityID)
	s.Require().Equal(identity.Identity.ID, *second.IdentityID)
	s.Require().True(second.AdoptAvatar)

	loaded, err := s.repo.GetIdentityAdoptionDecisionByPendingAuthSessionID(s.ctx, session.ID)
	s.Require().NoError(err)
	s.Require().Equal(second.ID, loaded.ID)
	s.Require().Equal(identity.Identity.ID, *loaded.IdentityID)
}

// TestUpsertIdentityAdoptionDecision_ReassignsExistingIdentityReference links
// two successive pending-session decisions to the same identity. The second
// retains that identity reference and reloading the first shows its reference
// cleared.
func (s *UserProfileIdentityRepoSuite) TestUpsertIdentityAdoptionDecision_ReassignsExistingIdentityReference() {
	user := s.mustCreateUser("adoption-reassign")
	identity, err := s.repo.CreateAuthIdentity(s.ctx, postgres.CreateAuthIdentityInput{
		UserID: user.ID,
		Canonical: postgres.AuthIdentityKey{
			ProviderType:    "wechat",
			ProviderKey:     "wechat-open",
			ProviderSubject: "union-adoption-reassign",
		},
	})
	s.Require().NoError(err)

	firstSession := s.mustCreatePendingAuthSession(identity.IdentityRef())
	firstDecision, err := s.repo.UpsertIdentityAdoptionDecision(s.ctx, postgres.IdentityAdoptionDecisionInput{
		PendingAuthSessionID: firstSession.ID,
		IdentityID:           &identity.Identity.ID,
		AdoptDisplayName:     true,
		AdoptAvatar:          false,
	})
	s.Require().NoError(err)
	s.Require().NotNil(firstDecision.IdentityID)
	s.Require().Equal(identity.Identity.ID, *firstDecision.IdentityID)

	secondSession := s.mustCreatePendingAuthSession(identity.IdentityRef())
	secondDecision, err := s.repo.UpsertIdentityAdoptionDecision(s.ctx, postgres.IdentityAdoptionDecisionInput{
		PendingAuthSessionID: secondSession.ID,
		IdentityID:           &identity.Identity.ID,
		AdoptDisplayName:     false,
		AdoptAvatar:          true,
	})
	s.Require().NoError(err)
	s.Require().NotNil(secondDecision.IdentityID)
	s.Require().Equal(identity.Identity.ID, *secondDecision.IdentityID)

	reloadedFirst, err := s.repo.GetIdentityAdoptionDecisionByPendingAuthSessionID(s.ctx, firstSession.ID)
	s.Require().NoError(err)
	s.Require().Nil(reloadedFirst.IdentityID)
}

// TestWithUserProfileIdentityTx_AllowsAvatarOnlyProfileUpdate writes an avatar
// inside the profile transaction and submits an empty user-column mask. The
// transaction succeeds and later reads return the supplied avatar URL.
func (s *UserProfileIdentityRepoSuite) TestWithUserProfileIdentityTx_AllowsAvatarOnlyProfileUpdate() {
	user := s.mustCreateUser("avatar-only-update")

	model, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().NotNil(model)

	err = s.repo.WithUserProfileIdentityTx(s.ctx, func(txCtx context.Context) error {
		_, err := s.repo.UpsertUserAvatar(txCtx, user.ID, identitycore.UpsertUserAvatarInput{
			StorageProvider: "remote_url",
			URL:             "https://cdn.example.com/avatar.png",
		})
		if err != nil {
			return err
		}
		// Select no user columns after storing the avatar so the transaction can
		// complete a profile operation whose persistent change belongs to the avatar
		// table.
		return s.repo.Update(txCtx, model, identitycore.UserUpdateFields{})
	})
	s.Require().NoError(err)

	avatar, err := s.repo.GetUserAvatar(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().NotNil(avatar)
	s.Require().Equal("https://cdn.example.com/avatar.png", avatar.URL)
}

// TestUserAvatarCRUDAndUserLookup stores inline avatar data, replaces it with a
// remote URL, and deletes the record. Reads return selected metadata, clear the
// replaced byte size, and finally return nil.
func (s *UserProfileIdentityRepoSuite) TestUserAvatarCRUDAndUserLookup() {
	user := s.mustCreateUser("avatar")

	inlineAvatar, err := s.repo.UpsertUserAvatar(s.ctx, user.ID, identitycore.UpsertUserAvatarInput{
		StorageProvider: "inline",
		URL:             "data:image/png;base64,QUJD",
		ContentType:     "image/png",
		ByteSize:        3,
		SHA256:          "902fbdd2b1df0c4f70b4a5d23525e932",
	})
	s.Require().NoError(err)
	s.Require().Equal("inline", inlineAvatar.StorageProvider)
	s.Require().Equal("data:image/png;base64,QUJD", inlineAvatar.URL)

	loadedAvatar, err := s.repo.GetUserAvatar(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().NotNil(loadedAvatar)
	s.Require().Equal("image/png", loadedAvatar.ContentType)
	s.Require().Equal(3, loadedAvatar.ByteSize)

	_, err = s.repo.UpsertUserAvatar(s.ctx, user.ID, identitycore.UpsertUserAvatarInput{
		StorageProvider: "remote_url",
		URL:             "https://cdn.example.com/avatar.png",
	})
	s.Require().NoError(err)

	loadedAvatar, err = s.repo.GetUserAvatar(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().NotNil(loadedAvatar)
	s.Require().Equal("remote_url", loadedAvatar.StorageProvider)
	s.Require().Equal("https://cdn.example.com/avatar.png", loadedAvatar.URL)
	s.Require().Zero(loadedAvatar.ByteSize)

	s.Require().NoError(s.repo.DeleteUserAvatar(s.ctx, user.ID))
	loadedAvatar, err = s.repo.GetUserAvatar(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Nil(loadedAvatar)
}

// TestUpdateUserLastLoginAndActiveAt_UsesDedicatedColumns writes distinct login
// and activity times through dedicated repository methods. A raw PostgreSQL
// read scans both nonnull columns and returns the expected values exactly.
func (s *UserProfileIdentityRepoSuite) TestUpdateUserLastLoginAndActiveAt_UsesDedicatedColumns() {
	user := s.mustCreateUser("activity")
	loginAt := time.Date(2026, 4, 20, 8, 0, 0, 0, time.UTC)
	activeAt := loginAt.Add(5 * time.Minute)

	s.Require().NoError(s.repo.UpdateUserLastLoginAt(s.ctx, user.ID, loginAt))
	s.Require().NoError(s.repo.UpdateUserLastActiveAt(s.ctx, user.ID, activeAt))

	var storedLoginAt sqlNullTime
	var storedActiveAt sqlNullTime
	s.Require().NoError(s.db.QueryRowContext(s.ctx, `
SELECT last_login_at, last_active_at
FROM users
WHERE id = $1`,
		user.ID,
	).Scan(&storedLoginAt, &storedActiveAt))
	s.Require().True(storedLoginAt.Valid)
	s.Require().True(storedActiveAt.Valid)
	s.Require().True(storedLoginAt.Time.Equal(loginAt))
	s.Require().True(storedActiveAt.Time.Equal(activeAt))
}

// Scan accepts a driver-supplied time.Time or SQL NULL for the fixture
// timestamp and updates its validity flag. Nil clears the time, while an
// unsupported type returns an error and retains the preceding fields.
func (t *sqlNullTime) Scan(value any) error {
	switch v := value.(type) {
	case time.Time:
		t.Time = v
		t.Valid = true
		return nil
	case nil:
		t.Time = time.Time{}
		t.Valid = false
		return nil
	default:
		return fmt.Errorf("unsupported scan type %T", value)
	}
}

// stringPtr returns a pointer to a distinct copy of the supplied string fixture
// value.
func stringPtr(v string) *string {
	return &v
}

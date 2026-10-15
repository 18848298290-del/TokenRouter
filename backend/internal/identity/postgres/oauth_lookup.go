package postgres

import (
	"context"
	"errors"
	"strings"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// oauth_lookup.go resolves compatible OAuth account matches and repairs WeChat
// runtime identity bindings.
// Provider email hints can match a local account without proving that account
// belongs to the external subject. Compatibility lookups normalize email text,
// reject synthetic addresses, and report ambiguous rows so pending flows can
// request account proof before binding.
//
// WeChat lookup also connects canonical subjects, channel-scoped openids, and
// historical provider-key records. It checks canonical identity first, then a
// complete mode/application/openid channel, and finally legacy openid
// subjects. Multiple owners are rejected, missing account rows do not
// authorize login, and inactive accounts return their status error. Runtime
// repair saves identity and channel data in an owned transaction before
// preparing the browser login continuation.

// FindLinuxDoCompatEmailUser returns one current account matching a normalized
// real provider email hint. Blank or synthetic addresses yield nil; multiple
// matches return USER_EMAIL_CONFLICT, and the result alone does not prove
// ownership.
func (d *PendingFlowDatabase) FindLinuxDoCompatEmailUser(ctx context.Context, email string) (*identitycore.User, error) {
	client := d.Client
	if client == nil {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" ||
		strings.HasSuffix(email, identitycore.LinuxDoConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.OIDCConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.WeChatConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.DingTalkConnectSyntheticEmailDomain) {
		return nil, nil
	}

	userEntity, err := client.User.Query().
		Where(UserNormalizedEmailPredicate(email)).
		Order(dbent.Asc(dbuser.FieldID)).
		All(ctx)
	if err != nil {
		return nil, infraerrors.InternalServer("COMPAT_EMAIL_LOOKUP_FAILED", "failed to look up compat email user").WithCause(err)
	}
	switch len(userEntity) {
	case 0:
		return nil, nil
	case 1:
		return UserFromEntity(userEntity[0]), nil
	default:
		return nil, infraerrors.Conflict("USER_EMAIL_CONFLICT", "normalized email matched multiple users")
	}
}

// FindOIDCCompatEmailUser returns one current account matching a normalized
// real email hint. Blank, synthetic, or missing addresses yield nil; other
// normalized-email failures retain compatibility lookup error context.
func (d *PendingFlowDatabase) FindOIDCCompatEmailUser(ctx context.Context, email string) (*identitycore.User, error) {
	client := d.Client
	if client == nil {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" ||
		strings.HasSuffix(email, identitycore.LinuxDoConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.OIDCConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.WeChatConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.DingTalkConnectSyntheticEmailDomain) {
		return nil, nil
	}

	userEntity, err := FindUserByNormalizedEmail(ctx, client, email)
	if err != nil {
		// Compatibility email supplies an optional account-choice observation. An
		// absent normalized address leaves no local candidate, allowing pending
		// proof to continue without a compatibility storage failure.
		if errors.Is(err, identitycore.ErrUserNotFound) {
			return nil, nil
		}
		return nil, infraerrors.InternalServer("COMPAT_EMAIL_LOOKUP_FAILED", "failed to look up compat email user").WithCause(err)
	}
	return UserFromEntity(userEntity), nil
}

// FindDingTalkCompatEmailUser returns one current account matching a
// normalized real staff email hint. Blank or synthetic addresses yield nil,
// while ambiguous rows return conflict; pending completion still requires
// account proof.
func (d *PendingFlowDatabase) FindDingTalkCompatEmailUser(ctx context.Context, email string) (*identitycore.User, error) {
	client := d.Client
	if client == nil {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" ||
		strings.HasSuffix(email, identitycore.DingTalkConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.LinuxDoConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.OIDCConnectSyntheticEmailDomain) ||
		strings.HasSuffix(email, identitycore.WeChatConnectSyntheticEmailDomain) {
		return nil, nil
	}

	userEntities, err := client.User.Query().
		Where(UserNormalizedEmailPredicate(email)).
		Order(dbent.Asc(dbuser.FieldID)).
		All(ctx)
	if err != nil {
		return nil, infraerrors.InternalServer("COMPAT_EMAIL_LOOKUP_FAILED", "failed to look up compat email user").WithCause(err)
	}
	switch len(userEntities) {
	case 0:
		return nil, nil
	case 1:
		return UserFromEntity(userEntities[0]), nil
	default:
		return nil, infraerrors.Conflict("USER_EMAIL_CONFLICT", "normalized email matched multiple users")
	}
}

// EnsureWeChatBindOwnership prechecks compatible WeChat subjects and supplied
// channel data against the target account. Other current active owners are
// rejected; missing-owner rows do not conflict here, while inactive-owner
// lookup remains an error. Channel lookup requires nonblank application ID and
// subject and uses the trimmed mode as supplied, including blank mode.
func (d *PendingFlowDatabase) EnsureWeChatBindOwnership(
	ctx context.Context,
	userID int64,
	providerSubject string,
	cfg identitycore.WeChatIdentityChannel,
	channelSubject string,
) error {
	client := d.Client
	if client == nil {
		return infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	identities, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("wechat"),
			authidentity.ProviderKeyIn(identitycore.WeChatCompatibleProviderKeys(identitycore.WeChatOAuthProviderKey)...),
			authidentity.ProviderSubjectEQ(strings.TrimSpace(providerSubject)),
		).
		All(ctx)
	if err != nil {
		return infraerrors.InternalServer("WECHAT_BIND_LOOKUP_FAILED", "failed to inspect wechat identity ownership").WithCause(err)
	}
	for _, identity := range identities {
		if identity != nil && identity.UserID != userID {
			activeOwner, lookupErr := FindActiveUserByID(ctx, client, identity.UserID)
			if lookupErr != nil {
				return lookupErr
			}
			if activeOwner != nil {
				return infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
			}
		}
	}

	channelSubject = strings.TrimSpace(channelSubject)
	channelAppID := strings.TrimSpace(cfg.AppID)
	if channelSubject == "" || channelAppID == "" {
		return nil
	}

	channels, err := client.AuthIdentityChannel.Query().
		Where(
			authidentitychannel.ProviderTypeEQ("wechat"),
			authidentitychannel.ProviderKeyIn(identitycore.WeChatCompatibleProviderKeys(identitycore.WeChatOAuthProviderKey)...),
			authidentitychannel.ChannelEQ(strings.TrimSpace(cfg.Mode)),
			authidentitychannel.ChannelAppIDEQ(channelAppID),
			authidentitychannel.ChannelSubjectEQ(channelSubject),
		).
		WithIdentity().
		All(ctx)
	if err != nil {
		return infraerrors.InternalServer("WECHAT_BIND_CHANNEL_LOOKUP_FAILED", "failed to inspect wechat identity channel ownership").WithCause(err)
	}
	for _, channel := range channels {
		if channel != nil && channel.Edges.Identity != nil && channel.Edges.Identity.UserID != userID {
			activeOwner, lookupErr := FindActiveUserByID(ctx, client, channel.Edges.Identity.UserID)
			if lookupErr != nil {
				return lookupErr
			}
			if activeOwner != nil {
				return infraerrors.Conflict("AUTH_IDENTITY_CHANNEL_OWNERSHIP_CONFLICT", "auth identity channel already belongs to another user")
			}
		}
	}
	return nil
}

// FindWeChatUserByLegacyOpenIDEntity resolves compatible WeChat identity forms
// to one current active account. A configured database is required, and the
// supplied channel is used only when mode, application ID, and openid are all
// populated.
//
// Canonical subject matches take precedence over channel records, followed by
// historical identities whose subject equals openid. Each set must resolve to
// at most one loaded user, then that account is reloaded for active-state
// admission. A resolved missing account returns nil instead of trying
// lower-priority identities; conflicts and inactive-account errors stop the
// lookup.
func (d *PendingFlowDatabase) FindWeChatUserByLegacyOpenIDEntity(
	ctx context.Context,
	identity identitycore.PendingAuthIdentityKey,
	cfg identitycore.WeChatIdentityChannel,
	openid string,
) (*dbent.User, error) {
	client := d.Client
	if client == nil {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	providerType := strings.TrimSpace(identity.ProviderType)
	providerSubject := strings.TrimSpace(identity.ProviderSubject)
	providerKeys := identitycore.WeChatCompatibleProviderKeys(identity.ProviderKey)
	// Prefer the canonical subject lookup before channel or legacy-openid
	// records. Once a subject set resolves an account, its current-user admission
	// result determines the lookup outcome.
	if providerSubject != "" {
		records, err := client.AuthIdentity.Query().
			Where(
				authidentity.ProviderTypeEQ(providerType),
				authidentity.ProviderKeyIn(providerKeys...),
				authidentity.ProviderSubjectEQ(providerSubject),
			).
			WithUser().
			All(ctx)
		if err != nil {
			return nil, infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
		}
		if user, err := singleWeChatIdentityUser(records); err != nil || user != nil {
			if err != nil || user == nil {
				return user, err
			}
			return FindActiveUserByID(ctx, client, user.ID)
		}
	}

	openid = strings.TrimSpace(openid)
	channel := strings.TrimSpace(cfg.Mode)
	channelAppID := strings.TrimSpace(cfg.AppID)
	// A channel openid is scoped by mode and application ID. Use that complete
	// tuple before legacy subject matching so an openid cannot select an
	// unrelated application record.
	if openid != "" && channel != "" && channelAppID != "" {
		records, err := client.AuthIdentityChannel.Query().
			Where(
				authidentitychannel.ProviderTypeEQ(providerType),
				authidentitychannel.ProviderKeyIn(providerKeys...),
				authidentitychannel.ChannelEQ(channel),
				authidentitychannel.ChannelAppIDEQ(channelAppID),
				authidentitychannel.ChannelSubjectEQ(openid),
			).
			WithIdentity(func(q *dbent.AuthIdentityQuery) {
				q.WithUser()
			}).
			All(ctx)
		if err != nil {
			return nil, infraerrors.InternalServer("AUTH_IDENTITY_CHANNEL_LOOKUP_FAILED", "failed to inspect auth identity channel ownership").WithCause(err)
		}
		if user, err := singleWeChatChannelUser(records); err != nil || user != nil {
			if err != nil || user == nil {
				return user, err
			}
			return FindActiveUserByID(ctx, client, user.ID)
		}
	}

	// Canonical and complete channel lookup found no user. A supplied openid can
	// still identify a historical subject record under the compatible
	// provider-key set.
	if openid == "" {
		return nil, nil
	}

	records, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderKeyIn(providerKeys...),
			authidentity.ProviderSubjectEQ(openid),
		).
		WithUser().
		All(ctx)
	if err != nil {
		return nil, infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	user, err := singleWeChatIdentityUser(records)
	if err != nil || user == nil {
		return user, err
	}
	return FindActiveUserByID(ctx, client, user.ID)
}

// FindWeChatUserByLegacyOpenID maps the legacy-compatible WeChat account
// lookup into identity domain data. Missing matches return nil with no error,
// while ambiguous ownership and inactive account results remain errors.
func (d *PendingFlowDatabase) FindWeChatUserByLegacyOpenID(ctx context.Context, k identitycore.PendingAuthIdentityKey, ch identitycore.WeChatIdentityChannel, openid string) (*identitycore.User, error) {
	v, e := d.FindWeChatUserByLegacyOpenIDEntity(ctx, k, ch, openid)
	return UserFromEntity(v), e
}

// EnsureWeChatRuntimeIdentityBinding repairs the supplied account's compatible
// WeChat identity and channel data in a new owned transaction. Callers supply
// verified subject and channel observations and keep nested claim values
// stable while the outer metadata copy is encoded.
//
// The injected transaction context reaches the pending identity helper, which
// can update a reused row and then its channel. A later binding failure
// returns before commit and deferred rollback attempts to undo those writes.
// Successful commit precedes the callback's pending-login continuation;
// credentials and browser-state creation remain with that caller.
func (d *PendingFlowDatabase) EnsureWeChatRuntimeIdentityBinding(
	ctx context.Context,
	userID int64,
	identity identitycore.PendingAuthIdentityKey,
	upstreamClaims map[string]any,
) error {
	client := d.Client
	if client == nil {
		return infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		return infraerrors.InternalServer("AUTH_IDENTITY_BIND_FAILED", "failed to begin wechat identity repair transaction").WithCause(err)
	}
	// Keep canonical identity and channel repair in the same owned transaction.
	// Rollback is attempted on failure, with its cleanup error discarded in favor
	// of the binding error.
	defer func() { _ = tx.Rollback() }()

	_, err = EnsurePendingOAuthIdentityForUser(dbent.NewTxContext(ctx, tx), tx, &dbent.PendingAuthSession{
		ProviderType:           strings.TrimSpace(identity.ProviderType),
		ProviderKey:            strings.TrimSpace(identity.ProviderKey),
		ProviderSubject:        strings.TrimSpace(identity.ProviderSubject),
		UpstreamIdentityClaims: identitycore.CloneOAuthMetadata(upstreamClaims),
	}, userID)
	if err != nil {
		return err
	}
	// Commit compatible WeChat identity and channel repair before the callback
	// uses that account for pending login, keeping the returned continuation
	// after the owned storage completion.
	return tx.Commit()
}

// singleWeChatIdentityUser returns one loaded user shared by the supplied
// identity records. Missing records or user edges are skipped; two different
// loaded user IDs return the identity ownership conflict.
func singleWeChatIdentityUser(records []*dbent.AuthIdentity) (*dbent.User, error) {
	var resolved *dbent.User
	for _, record := range records {
		if record == nil || record.Edges.User == nil {
			continue
		}
		if resolved == nil {
			resolved = record.Edges.User
			continue
		}
		if resolved.ID != record.Edges.User.ID {
			return nil, infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
		}
	}
	return resolved, nil
}

// singleWeChatChannelUser returns one loaded user shared by the supplied
// channel identity edges. Missing records or relationships are skipped; two
// different loaded user IDs return the channel ownership conflict.
func singleWeChatChannelUser(records []*dbent.AuthIdentityChannel) (*dbent.User, error) {
	var resolved *dbent.User
	for _, record := range records {
		if record == nil || record.Edges.Identity == nil || record.Edges.Identity.Edges.User == nil {
			continue
		}
		if resolved == nil {
			resolved = record.Edges.Identity.Edges.User
			continue
		}
		if resolved.ID != record.Edges.Identity.Edges.User.ID {
			return nil, infraerrors.Conflict("AUTH_IDENTITY_CHANNEL_OWNERSHIP_CONFLICT", "auth identity channel already belongs to another user")
		}
	}
	return resolved, nil
}

// FindOAuthBindTarget reads the pending binding account and maps its current
// row. Missing users return authentication required and other failures carry
// lookup context; this method does not independently check active status.
func (d *PendingFlowDatabase) FindOAuthBindTarget(ctx context.Context, id int64) (*identitycore.User, error) {
	v, e := d.Client.User.Get(ctx, id)
	if e != nil {
		if dbent.IsNotFound(e) {
			return nil, infraerrors.Unauthorized("AUTH_REQUIRED", "current user is required to bind wechat account")
		}
		return nil, infraerrors.InternalServer("WECHAT_BIND_USER_LOOKUP_FAILED", "failed to load current user").WithCause(e)
	}
	return UserFromEntity(v), nil
}

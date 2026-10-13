package postgres

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	"github.com/TokenFlux/TokenRouter/ent/identityadoptiondecision"
	"github.com/TokenFlux/TokenRouter/ent/pendingauthsession"
	"github.com/TokenFlux/TokenRouter/ent/predicate"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Pending identity persistence distinguishes stable provider subjects from
// account email hints and optional application-scoped channel subjects. Target
// resolution prefers a supplied positive account ID, while email lookup is an
// exact trimmed, case-folded account observation that still requires the
// surrounding flow proof.
//
// Binding can join a supplied Ent transaction or own one, applying selected
// display-name and avatar hints beside provider metadata and adoption
// references. WeChat paths inspect compatible canonical and historical rows
// plus complete channel tuples before choosing their writes. Browser
// consumption has separate transactional and service paths: the transaction
// helper rechecks session state before conditionally marking consumption,
// while its callers decide whether adoption and consumption share one commit
// or run in separate stages.

// UpdatePendingOAuthSessionProgress saves intent, resolved email, target, and
// replaced completion payload through the supplied client. Outer state maps
// are copied; nil or nonpositive target clears its stored reference, while
// other local state entries remain.
func UpdatePendingOAuthSessionProgress(
	ctx context.Context,
	client *dbent.Client,
	session *dbent.PendingAuthSession,
	intent string,
	resolvedEmail string,
	targetUserID *int64,
	completionResponse map[string]any,
) (*dbent.PendingAuthSession, error) {
	if client == nil || session == nil {
		return nil, infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth session is invalid")
	}

	localFlowState := clonePendingMap(session.LocalFlowState)
	localFlowState[identitycore.OAuthCompletionResponseKey] = clonePendingMap(completionResponse)

	update := client.PendingAuthSession.UpdateOneID(session.ID).
		SetIntent(strings.TrimSpace(intent)).
		SetResolvedEmail(strings.TrimSpace(resolvedEmail)).
		SetLocalFlowState(localFlowState)
	if targetUserID != nil && *targetUserID > 0 {
		update = update.SetTargetUserID(*targetUserID)
	} else {
		update = update.ClearTargetUserID()
	}
	return update.Save(ctx)
}

// ResolvePendingOAuthTargetUserID selects a positive stored target before
// normalized email lookup. Missing session or usable email returns an input
// error, and a missing email-matched account becomes the pending-target lookup
// error.
func ResolvePendingOAuthTargetUserID(ctx context.Context, client *dbent.Client, session *dbent.PendingAuthSession) (int64, error) {
	if session == nil {
		return 0, infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth session is invalid")
	}
	if session.TargetUserID != nil && *session.TargetUserID > 0 {
		return *session.TargetUserID, nil
	}
	email := strings.TrimSpace(session.ResolvedEmail)
	if email == "" {
		return 0, infraerrors.BadRequest("PENDING_AUTH_TARGET_USER_MISSING", "pending auth target user is missing")
	}

	userEntity, err := FindUserByNormalizedEmail(ctx, client, email)
	if err != nil {
		if errors.Is(err, identitycore.ErrUserNotFound) {
			return 0, infraerrors.InternalServer("PENDING_AUTH_TARGET_USER_NOT_FOUND", "pending auth target user was not found")
		}
		return 0, err
	}
	return userEntity.ID, nil
}

// UserNormalizedEmailPredicate matches trimmed lowercase email text against
// stored account email. Empty normalized input uses exact original text, with
// account lifecycle and transaction selection left to the surrounding query.
func UserNormalizedEmailPredicate(email string) predicate.User {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return dbuser.EmailEQ(email)
	}
	return predicate.User(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString("LOWER(TRIM(").
				Ident(s.C(dbuser.FieldEmail)).
				WriteString(")) = ").
				Arg(normalized)
		}))
	})
}

// FindUserByNormalizedEmail returns one selected account matching normalized
// email through the supplied client. No matches return ErrUserNotFound,
// multiple matches return conflict, and a missing client reports pending
// service unavailable.
func FindUserByNormalizedEmail(ctx context.Context, client *dbent.Client, email string) (*dbent.User, error) {
	if client == nil {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}

	matches, err := client.User.Query().
		Where(UserNormalizedEmailPredicate(email)).
		Order(dbent.Asc(dbuser.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, identitycore.ErrUserNotFound
	}
	if len(matches) > 1 {
		return nil, infraerrors.Conflict("USER_EMAIL_CONFLICT", "normalized email matched multiple users")
	}
	return matches[0], nil
}

// EnsurePendingOAuthRegistrationIdentityAvailable rejects a pending subject
// already owned by a current active account. Missing identity or missing owner
// permits further registration checks; inactive-owner lookup returns its
// account status error.
func EnsurePendingOAuthRegistrationIdentityAvailable(ctx context.Context, client *dbent.Client, session *dbent.PendingAuthSession) error {
	if client == nil || session == nil {
		return infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid")
	}

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(strings.TrimSpace(session.ProviderType)),
			authidentity.ProviderKeyEQ(strings.TrimSpace(session.ProviderKey)),
			authidentity.ProviderSubjectEQ(strings.TrimSpace(session.ProviderSubject)),
		).
		Only(ctx)
	if err != nil {
		// An absent provider tuple leaves this pending identity available for
		// account registration. Existing tuples continue through ownership
		// rejection, while other lookup failures remain errors for the registration
		// caller.
		if dbent.IsNotFound(err) {
			return nil
		}
		return err
	}
	if identity == nil || identity.UserID <= 0 {
		return nil
	}

	activeOwner, err := FindActiveUserByID(ctx, client, identity.UserID)
	if err != nil {
		return err
	}
	if activeOwner != nil {
		return infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
	}
	return nil
}

// EnsurePendingOAuthIdentityForUser ensures a provider tuple for the validated
// target inside the caller Ent transaction. WeChat delegates its compatible
// identity and channel handling.
//
// Other providers reload the exact tuple before creating or reusing it.
// Another current active owner causes conflict; a missing owner permits
// reassignment to the target, while an inactive owner returns its status
// error. New identity metadata receives an outer copy of pending claims, and
// an issuer hint is stored when available. Callers own commit or rollback of
// this write and later adoption stages.
func EnsurePendingOAuthIdentityForUser(ctx context.Context, tx *dbent.Tx, session *dbent.PendingAuthSession, userID int64) (*dbent.AuthIdentity, error) {
	if session != nil && strings.EqualFold(strings.TrimSpace(session.ProviderType), "wechat") {
		return EnsurePendingWeChatOAuthIdentityForUser(ctx, tx, session, userID)
	}

	client := tx.Client()
	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(strings.TrimSpace(session.ProviderType)),
			authidentity.ProviderKeyEQ(strings.TrimSpace(session.ProviderKey)),
			authidentity.ProviderSubjectEQ(strings.TrimSpace(session.ProviderSubject)),
		).
		Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return nil, err
	}
	if identity != nil {
		if identity.UserID != userID {
			activeOwner, err := FindActiveUserByID(ctx, client, identity.UserID)
			if err != nil {
				return nil, err
			}
			if activeOwner != nil {
				return nil, infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
			}
			return client.AuthIdentity.UpdateOneID(identity.ID).
				SetUserID(userID).
				Save(ctx)
		}
		return identity, nil
	}

	create := client.AuthIdentity.Create().
		SetUserID(userID).
		SetProviderType(strings.TrimSpace(session.ProviderType)).
		SetProviderKey(strings.TrimSpace(session.ProviderKey)).
		SetProviderSubject(strings.TrimSpace(session.ProviderSubject)).
		SetMetadata(cloneOAuthMetadata(session.UpstreamIdentityClaims))
	if issuer := oauthIdentityIssuer(session); issuer != nil {
		create = create.SetIssuer(strings.TrimSpace(*issuer))
	}
	return create.Save(ctx)
}

// EnsurePendingWeChatOAuthIdentityForUser selects canonical and historical
// subject rows and maintains a complete channel tuple inside the caller
// transaction. Prepared pending subject and a validated target account are
// required.
//
// It prefers the requested key among compatible rows and checks another owner
// through current-account lookup. Existing canonical rows can be reassigned
// when that owner is missing; the legacy-openid branch changes key, subject,
// and metadata while retaining that selected row user ID. Complete channel
// metadata is merged and relinked to the selected identity. Key rewriting is
// skipped when a canonical-key record already exists, and the caller rolls
// back any later channel failure.
func EnsurePendingWeChatOAuthIdentityForUser(ctx context.Context, tx *dbent.Tx, session *dbent.PendingAuthSession, userID int64) (*dbent.AuthIdentity, error) {
	client := tx.Client()
	providerType := strings.TrimSpace(session.ProviderType)
	providerKey := strings.TrimSpace(session.ProviderKey)
	providerSubject := strings.TrimSpace(session.ProviderSubject)
	providerKeys := identitycore.WeChatCompatibleProviderKeys(providerKey)
	channel := strings.TrimSpace(pendingSessionStringValue(session.UpstreamIdentityClaims, "channel"))
	channelAppID := strings.TrimSpace(pendingSessionStringValue(session.UpstreamIdentityClaims, "channel_app_id"))
	channelSubject := strings.TrimSpace(pendingSessionStringValue(session.UpstreamIdentityClaims, "channel_subject"))
	metadata := cloneOAuthMetadata(session.UpstreamIdentityClaims)

	identityRecords, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderKeyIn(providerKeys...),
			authidentity.ProviderSubjectEQ(providerSubject),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	identity, hasCanonicalKey, err := ChooseWeChatIdentityForUser(ctx, client, identityRecords, userID, providerKey)
	if err != nil {
		return nil, err
	}

	var legacyOpenIDIdentity *dbent.AuthIdentity
	if channelSubject != "" && channelSubject != providerSubject {
		legacyOpenIDRecords, err := client.AuthIdentity.Query().
			Where(
				authidentity.ProviderTypeEQ(providerType),
				authidentity.ProviderKeyIn(providerKeys...),
				authidentity.ProviderSubjectEQ(channelSubject),
			).
			All(ctx)
		if err != nil {
			return nil, err
		}
		legacyOpenIDIdentity, _, err = ChooseWeChatIdentityForUser(ctx, client, legacyOpenIDRecords, userID, providerKey)
		if err != nil {
			return nil, err
		}
	}

	switch {
	case identity != nil:
		update := client.AuthIdentity.UpdateOneID(identity.ID).
			SetMetadata(mergeOAuthMetadata(identity.Metadata, metadata))
		if identity.UserID != userID {
			update = update.SetUserID(userID)
		}
		if !strings.EqualFold(strings.TrimSpace(identity.ProviderKey), providerKey) && !hasCanonicalKey {
			update = update.SetProviderKey(providerKey)
		}
		if issuer := oauthIdentityIssuer(session); issuer != nil {
			update = update.SetIssuer(strings.TrimSpace(*issuer))
		}
		identity, err = update.Save(ctx)
		if err != nil {
			return nil, err
		}
	// The selected legacy-openid row is rewritten to the current subject and
	// provider key. This branch retains its UserID, unlike reassignment in the
	// existing canonical identity branch.
	case legacyOpenIDIdentity != nil:
		update := client.AuthIdentity.UpdateOneID(legacyOpenIDIdentity.ID).
			SetProviderKey(providerKey).
			SetProviderSubject(providerSubject).
			SetMetadata(mergeOAuthMetadata(legacyOpenIDIdentity.Metadata, metadata))
		if issuer := oauthIdentityIssuer(session); issuer != nil {
			update = update.SetIssuer(strings.TrimSpace(*issuer))
		}
		identity, err = update.Save(ctx)
		if err != nil {
			return nil, err
		}
	default:
		create := client.AuthIdentity.Create().
			SetUserID(userID).
			SetProviderType(providerType).
			SetProviderKey(providerKey).
			SetProviderSubject(providerSubject).
			SetMetadata(metadata)
		if issuer := oauthIdentityIssuer(session); issuer != nil {
			create = create.SetIssuer(strings.TrimSpace(*issuer))
		}
		identity, err = create.Save(ctx)
		if err != nil {
			return nil, err
		}
	}

	// A channel subject is meaningful with its mode and application ID.
	// Incomplete channel observations keep the ensured canonical identity and
	// skip channel persistence for this call.
	if channel == "" || channelAppID == "" || channelSubject == "" {
		return identity, nil
	}

	channelRecords, err := client.AuthIdentityChannel.Query().
		Where(
			authidentitychannel.ProviderTypeEQ(providerType),
			authidentitychannel.ProviderKeyIn(providerKeys...),
			authidentitychannel.ChannelEQ(channel),
			authidentitychannel.ChannelAppIDEQ(channelAppID),
			authidentitychannel.ChannelSubjectEQ(channelSubject),
		).
		WithIdentity().
		All(ctx)
	if err != nil {
		return nil, err
	}
	channelRecord, hasCanonicalChannelKey, err := ChooseWeChatChannelForUser(ctx, client, channelRecords, userID, providerKey)
	if err != nil {
		return nil, err
	}

	channelMetadata := mergeOAuthMetadata(ChannelRecordMetadata(channelRecord), metadata)
	if channelRecord == nil {
		if _, err := client.AuthIdentityChannel.Create().
			SetIdentityID(identity.ID).
			SetProviderType(providerType).
			SetProviderKey(providerKey).
			SetChannel(channel).
			SetChannelAppID(channelAppID).
			SetChannelSubject(channelSubject).
			SetMetadata(channelMetadata).
			Save(ctx); err != nil {
			return nil, err
		}
		return identity, nil
	}

	updateChannel := client.AuthIdentityChannel.UpdateOneID(channelRecord.ID).
		SetIdentityID(identity.ID).
		SetMetadata(channelMetadata)
	if !strings.EqualFold(strings.TrimSpace(channelRecord.ProviderKey), providerKey) && !hasCanonicalChannelKey {
		updateChannel = updateChannel.SetProviderKey(providerKey)
	}
	_, err = updateChannel.Save(ctx)
	if err != nil {
		return nil, err
	}
	return identity, nil
}

// ChooseWeChatIdentityForUser selects the first preferred-key identity or
// compatible alternative after checking its observed owner against the target.
// The boolean records whether a preferred-key row was observed.
//
// Records with another owner require a current-account lookup: active owners
// cause conflict, missing owners remain selectable, and inactive or database
// errors stop selection. Nil records are skipped and supplied order breaks
// ties. The result can therefore retain a different missing owner ID; the
// subsequent mutation path decides which owner fields it rewrites before
// committing.
func ChooseWeChatIdentityForUser(ctx context.Context, client *dbent.Client, records []*dbent.AuthIdentity, userID int64, preferredProviderKey string) (*dbent.AuthIdentity, bool, error) {
	var preferred *dbent.AuthIdentity
	var fallback *dbent.AuthIdentity
	hasCanonicalKey := false
	for _, record := range records {
		if record == nil {
			continue
		}
		if record.UserID != userID {
			activeOwner, err := FindActiveUserByID(ctx, client, record.UserID)
			if err != nil {
				return nil, false, err
			}
			if activeOwner != nil {
				return nil, false, infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
			}
		}
		if strings.EqualFold(strings.TrimSpace(record.ProviderKey), preferredProviderKey) {
			hasCanonicalKey = true
			if preferred == nil {
				preferred = record
			}
			continue
		}
		if fallback == nil {
			fallback = record
		}
	}
	if preferred != nil {
		return preferred, hasCanonicalKey, nil
	}
	return fallback, hasCanonicalKey, nil
}

// ChooseWeChatChannelForUser selects the first preferred-key channel or
// compatible alternative and reports preferred-key presence. Nil records are
// skipped, while missing identity edges provide no owner observation.
//
// A loaded different owner is checked against current account state before
// selection. Active owners conflict, missing owners remain selectable, and
// inactive or storage failures stop the result. Supplied order breaks ties.
// Callers relink the selected channel to the resolved identity in their
// transaction and handle any later persistence failure.
func ChooseWeChatChannelForUser(ctx context.Context, client *dbent.Client, records []*dbent.AuthIdentityChannel, userID int64, preferredProviderKey string) (*dbent.AuthIdentityChannel, bool, error) {
	var preferred *dbent.AuthIdentityChannel
	var fallback *dbent.AuthIdentityChannel
	hasCanonicalKey := false
	for _, record := range records {
		if record == nil {
			continue
		}
		if record.Edges.Identity != nil && record.Edges.Identity.UserID != userID {
			activeOwner, err := FindActiveUserByID(ctx, client, record.Edges.Identity.UserID)
			if err != nil {
				return nil, false, err
			}
			if activeOwner != nil {
				return nil, false, infraerrors.Conflict("AUTH_IDENTITY_CHANNEL_OWNERSHIP_CONFLICT", "auth identity channel already belongs to another user")
			}
		}
		if strings.EqualFold(strings.TrimSpace(record.ProviderKey), preferredProviderKey) {
			hasCanonicalKey = true
			if preferred == nil {
				preferred = record
			}
			continue
		}
		if fallback == nil {
			fallback = record
		}
	}
	if preferred != nil {
		return preferred, hasCanonicalKey, nil
	}
	return fallback, hasCanonicalKey, nil
}

// FindActiveUserByID reads one current account and enforces trimmed
// case-insensitive active status. Missing client, nonpositive ID, or missing
// rows return nil; inactive rows return ErrUserNotActive and other failures
// carry lookup context.
func FindActiveUserByID(ctx context.Context, client *dbent.Client, userID int64) (*dbent.User, error) {
	if client == nil || userID <= 0 {
		return nil, nil
	}
	userEntity, err := client.User.Get(ctx, userID)
	if err != nil {
		// A missing identity owner supplies no active account candidate. Callers
		// decide whether another compatible record can resolve ownership, while
		// inactive loaded accounts still produce the separate status error.
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, infraerrors.InternalServer("AUTH_IDENTITY_USER_LOOKUP_FAILED", "failed to load auth identity user").WithCause(err)
	}
	if !strings.EqualFold(strings.TrimSpace(userEntity.Status), identitycore.StatusActive) {
		return nil, identitycore.ErrUserNotActive
	}
	return userEntity, nil
}

// ChannelRecordMetadata returns an outer copy of channel metadata or an
// allocated empty map for nil input. Nested mutable values remain shared with
// the stored channel and need copying before independent changes.
func ChannelRecordMetadata(channel *dbent.AuthIdentityChannel) map[string]any {
	if channel == nil {
		return map[string]any{}
	}
	return cloneOAuthMetadata(channel.Metadata)
}

// ApplyPendingOAuthBinding applies selected pending identity and profile
// writes in an injected or owned Ent transaction. Missing client/session or a
// nonforced unselected decision skips persistence.
//
// An injected transaction delegates directly to its binding stages; otherwise
// an owned transaction rolls back failures and commits only after the
// delegated operation succeeds. Force selection and first-bind defaults come
// from the validated flow. The supplied account/profile/entitlement
// participants need to honor the transaction context, while effects outside it
// retain caller recovery responsibility.
func ApplyPendingOAuthBinding(
	ctx context.Context,
	client *dbent.Client,
	authService *identitycore.AuthService,
	userService *identitycore.UserService,
	session *dbent.PendingAuthSession,
	decision *dbent.IdentityAdoptionDecision,
	overrideUserID *int64,
	forceBind bool,
	applyFirstBindDefaults bool,
) error {
	if client == nil || session == nil {
		return nil
	}
	if !forceBind && !shouldBindPendingOAuthIdentity(session, decision) {
		return nil
	}

	if tx := dbent.TxFromContext(ctx); tx != nil {
		return ApplyPendingOAuthBindingTx(ctx, tx, authService, userService, session, decision, overrideUserID, forceBind, applyFirstBindDefaults)
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	// Rollback the owned identity, metadata, profile, and grant writes when a
	// later binding stage fails. Cleanup errors are discarded while that stage
	// failure remains the caller recovery input.
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := ApplyPendingOAuthBindingTx(txCtx, tx, authService, userService, session, decision, overrideUserID, forceBind, applyFirstBindDefaults); err != nil {
		return err
	}
	// Commit the selected identity/profile binding stages after their success,
	// while earlier failed stages reach rollback and the caller receives no
	// completed binding result.
	return tx.Commit()
}

// ApplyPendingOAuthBindingTx saves selected profile and provider identity data
// inside the caller transaction. A positive override selects its account
// before stored-target or normalized-email resolution; missing
// transaction/session or unselected nonforced choice skips work.
//
// Chosen display name is normalized, and known invalid avatar hints can be
// skipped before writes. Username storage precedes identity ensuring and
// metadata merging. Adoption references are cleared from other decisions
// before linking the current one, then configured first-bind grants and avatar
// storage follow. Every failure stops later stages and needs caller rollback
// of preceding writes; external profile effects follow their own service
// behavior.
func ApplyPendingOAuthBindingTx(
	ctx context.Context,
	tx *dbent.Tx,
	authService *identitycore.AuthService,
	userService *identitycore.UserService,
	session *dbent.PendingAuthSession,
	decision *dbent.IdentityAdoptionDecision,
	overrideUserID *int64,
	forceBind bool,
	applyFirstBindDefaults bool,
) error {
	if tx == nil || session == nil {
		return nil
	}
	if !forceBind && !shouldBindPendingOAuthIdentity(session, decision) {
		return nil
	}

	targetUserID := int64(0)
	if overrideUserID != nil && *overrideUserID > 0 {
		targetUserID = *overrideUserID
	} else {
		resolvedUserID, err := ResolvePendingOAuthTargetUserID(ctx, tx.Client(), session)
		if err != nil {
			return err
		}
		targetUserID = resolvedUserID
	}

	adoptedDisplayName := ""
	if decision != nil && decision.AdoptDisplayName {
		adoptedDisplayName = normalizeAdoptedOAuthDisplayName(pendingSessionStringValue(session.UpstreamIdentityClaims, "suggested_display_name"))
	}
	adoptedAvatarURL := ""
	if decision != nil && decision.AdoptAvatar {
		adoptedAvatarURL = pendingSessionStringValue(session.UpstreamIdentityClaims, "suggested_avatar_url")
	}
	shouldAdoptAvatar := false
	if decision != nil && decision.AdoptAvatar && adoptedAvatarURL != "" {
		// Known rejected provider-avatar hints leave adoption able to proceed with
		// other fields. An unrelated validation failure stops the transaction before
		// the selected profile writes begin.
		if err := identitycore.ValidateUserAvatar(adoptedAvatarURL); err == nil {
			shouldAdoptAvatar = true
		} else if !shouldSkipAvatarAdoption(err) {
			return err
		}
	}

	if decision != nil && decision.AdoptDisplayName && adoptedDisplayName != "" {
		if err := tx.Client().User.UpdateOneID(targetUserID).
			SetUsername(adoptedDisplayName).
			Exec(ctx); err != nil {
			return err
		}
	}

	identity, err := EnsurePendingOAuthIdentityForUser(ctx, tx, session, targetUserID)
	if err != nil {
		return err
	}

	metadata := cloneOAuthMetadata(identity.Metadata)
	maps.Copy(metadata, session.UpstreamIdentityClaims)
	if decision != nil && decision.AdoptDisplayName && adoptedDisplayName != "" {
		metadata["display_name"] = adoptedDisplayName
	}
	if shouldAdoptAvatar {
		metadata["avatar_url"] = adoptedAvatarURL
	}

	updateIdentity := tx.Client().AuthIdentity.UpdateOneID(identity.ID).SetMetadata(metadata)
	if issuer := oauthIdentityIssuer(session); issuer != nil {
		updateIdentity = updateIdentity.SetIssuer(strings.TrimSpace(*issuer))
	}
	if _, err := updateIdentity.Save(ctx); err != nil {
		return err
	}

	// Move the provider reference by clearing other decisions before linking this
	// choice. Both writes share the caller transaction so a link failure can roll
	// back preceding reference removal.
	if decision != nil && (decision.IdentityID == nil || *decision.IdentityID != identity.ID) {
		if _, err := tx.Client().IdentityAdoptionDecision.Update().
			Where(
				identityadoptiondecision.IdentityIDEQ(identity.ID),
				identityadoptiondecision.IDNEQ(decision.ID),
			).
			ClearIdentityID().
			Save(ctx); err != nil {
			return err
		}
		if _, err := tx.Client().IdentityAdoptionDecision.UpdateOneID(decision.ID).
			SetIdentityID(identity.ID).
			Save(ctx); err != nil {
			return err
		}
	}

	if applyFirstBindDefaults && authService != nil {
		if err := authService.ApplyProviderDefaultSettingsOnFirstBind(ctx, targetUserID, session.ProviderType); err != nil {
			return err
		}
	}

	if shouldAdoptAvatar && userService != nil {
		if _, err := userService.SetAvatar(ctx, targetUserID, adoptedAvatarURL); err != nil {
			return err
		}
	}

	return nil
}

// ConsumePendingOAuthBrowserSessionTx reloads session state inside the caller
// transaction before claiming consumption. Missing rows, prior consumption,
// expiry, and mismatched retained browser proof produce their pending errors.
//
// The update conditions consumption on an unconsumed row and clears
// completion-code values. Unlike the service consumption path, expiry and
// browser observations are checked by the preceding read and legacy local
// completion credentials are not scrubbed here. A conditional miss reloads
// consumption state to recognize a concurrent claimant. The caller keeps
// adoption and this claim in its chosen transaction and owns completion.
func ConsumePendingOAuthBrowserSessionTx(
	ctx context.Context,
	tx *dbent.Tx,
	session *dbent.PendingAuthSession,
) error {
	if tx == nil || session == nil {
		return identitycore.ErrPendingAuthSessionNotFound
	}

	storedSession, err := tx.Client().PendingAuthSession.Get(ctx, session.ID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return identitycore.ErrPendingAuthSessionNotFound
		}
		return err
	}

	now := time.Now().UTC()
	if storedSession.ConsumedAt != nil {
		return identitycore.ErrPendingAuthSessionConsumed
	}
	if !storedSession.ExpiresAt.IsZero() && now.After(storedSession.ExpiresAt) {
		return identitycore.ErrPendingAuthSessionExpired
	}
	if strings.TrimSpace(storedSession.BrowserSessionKey) != "" &&
		strings.TrimSpace(storedSession.BrowserSessionKey) != strings.TrimSpace(session.BrowserSessionKey) {
		return identitycore.ErrPendingAuthBrowserMismatch
	}

	if _, err := tx.Client().PendingAuthSession.UpdateOneID(storedSession.ID).
		// A second transaction can claim the row after the preceding read. Condition
		// this mutation on absent consumption so only one claimant can save its
		// consumption timestamp.
		Where(pendingauthsession.ConsumedAtIsNil()).
		SetConsumedAt(now).
		SetCompletionCodeHash("").
		ClearCompletionCodeExpiresAt().
		Save(ctx); err != nil {
		if dbent.IsNotFound(err) {
			current, loadErr := tx.Client().PendingAuthSession.Get(ctx, storedSession.ID)
			if loadErr != nil {
				return loadErr
			}
			if current.ConsumedAt != nil {
				return identitycore.ErrPendingAuthSessionConsumed
			}
		}
		return err
	}

	return nil
}

// ApplyPendingOAuthAdoptionAndConsumeSession saves selected adoption and
// consumes its browser session in one owned transaction. A client, nonnil
// pending session, and positive target account are required.
//
// It injects the new transaction before adoption, so binding stages join that
// scope. Browser consumption follows adoption before commit, with rollback
// attempted on either failure. Credentials or account creation performed by an
// earlier authentication caller remain outside this helper transaction.
// Successful commit completes these pending writes before the HTTP caller
// clears browser proof and responds.
func ApplyPendingOAuthAdoptionAndConsumeSession(
	ctx context.Context,
	client *dbent.Client,
	authService *identitycore.AuthService,
	userService *identitycore.UserService,
	session *dbent.PendingAuthSession,
	decision *dbent.IdentityAdoptionDecision,
	userID int64,
) error {
	if client == nil {
		return infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}
	if session == nil || userID <= 0 {
		return infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid")
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	// Rollback the owned pending transaction after a failing identity, profile,
	// or consumption stage. Cleanup errors are discarded while the originating
	// operation error remains the caller recovery input.
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := ApplyPendingOAuthAdoption(txCtx, client, authService, userService, session, decision, &userID); err != nil {
		return err
	}
	if err := ConsumePendingOAuthBrowserSessionTx(txCtx, tx, session); err != nil {
		return err
	}
	// Commit selected profile adoption and the conditional browser-session claim
	// together before HTTP clears proof cookies, so either stage failure rolls
	// back their shared pending result.
	return tx.Commit()
}

// ApplyPendingOAuthAdoption delegates decision-selected binding and profile
// writes for a nonnil pending session. Current-user binding intent requests
// first-bind defaults; the shared helper chooses transaction participation and
// respects the supplied target override.
func ApplyPendingOAuthAdoption(
	ctx context.Context,
	client *dbent.Client,
	authService *identitycore.AuthService,
	userService *identitycore.UserService,
	session *dbent.PendingAuthSession,
	decision *dbent.IdentityAdoptionDecision,
	overrideUserID *int64,
) error {
	return ApplyPendingOAuthBinding(
		ctx,
		client,
		authService,
		userService,
		session,
		decision,
		overrideUserID,
		false,
		strings.EqualFold(strings.TrimSpace(session.Intent), "bind_current_user"),
	)
}

// PendingOAuthIdentityExistsForUser reports a provider subject owned by the
// supplied positive account. Missing client/session/type/subject returns
// false; WeChat uses compatible keys, while other providers constrain a
// nonblank supplied key.
func PendingOAuthIdentityExistsForUser(
	ctx context.Context,
	client *dbent.Client,
	session *dbent.PendingAuthSession,
	userID int64,
) (bool, error) {
	if client == nil || session == nil || userID <= 0 {
		return false, nil
	}

	providerType := strings.TrimSpace(session.ProviderType)
	providerKey := strings.TrimSpace(session.ProviderKey)
	providerSubject := strings.TrimSpace(session.ProviderSubject)
	if providerType == "" || providerSubject == "" {
		return false, nil
	}

	query := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderSubjectEQ(providerSubject),
			authidentity.UserIDEQ(userID),
		)
	if strings.EqualFold(providerType, "wechat") {
		query = query.Where(authidentity.ProviderKeyIn(identitycore.WeChatCompatibleProviderKeys(providerKey)...))
	} else if providerKey != "" {
		query = query.Where(authidentity.ProviderKeyEQ(providerKey))
	}

	count, err := query.Count(ctx)
	if err != nil {
		return false, infraerrors.InternalServer("AUTH_IDENTITY_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	return count > 0, nil
}

// clonePendingMap delegates a new outer copy of pending metadata entries. Nil
// and empty input produce allocated empty maps; nested maps, slices, and
// pointers remain shared for callers to treat accordingly.
func clonePendingMap(values map[string]any) map[string]any {
	return identitycore.ClonePendingMap(values)
}

// pendingSessionStringValue delegates trimmed string lookup from stored
// pending metadata. Missing keys and unsupported value types return empty
// text, leaving step and issuer selection to the calling flow.
func pendingSessionStringValue(values map[string]any, key string) string {
	return identitycore.PendingSessionStringValue(values, key)
}

// cloneOAuthMetadata delegates a new outer provider metadata map with copied
// entries. Nil and empty input become allocated empty maps; nested mutable
// values remain shared before later independent changes.
func cloneOAuthMetadata(values map[string]any) map[string]any {
	return identitycore.CloneOAuthMetadata(values)
}

// mergeOAuthMetadata delegates outer copying of the base provider map and
// overlay replacement. Overlay values win even when nil, and nested values
// remain shared with supplied metadata collections.
func mergeOAuthMetadata(base map[string]any, overlay map[string]any) map[string]any {
	return identitycore.MergeOAuthMetadata(base, overlay)
}

// normalizeAdoptedOAuthDisplayName delegates trimming and the one-hundred-rune
// limit for provider-name adoption. Blank or shorter input retains its trimmed
// value, and oversized input becomes a complete-rune prefix before account
// persistence.
func normalizeAdoptedOAuthDisplayName(value string) string {
	return identitycore.NormalizeAdoptedOAuthDisplayName(value)
}

// oauthIdentityIssuer maps pending state and delegates issuer-hint selection
// for provider metadata. OIDC key takes precedence over its issuer claim;
// other types use the claim when present, and missing values return nil.
func oauthIdentityIssuer(session *dbent.PendingAuthSession) *string {
	return identitycore.OauthIdentityIssuer(PendingAuthSessionFromEntity(session))
}

// shouldBindPendingOAuthIdentity maps pending and decision values for domain
// binding selection. Recognized authentication intents need a decision, while
// other intents use its selected profile flags before storage proceeds.
func shouldBindPendingOAuthIdentity(session *dbent.PendingAuthSession, decision *dbent.IdentityAdoptionDecision) bool {
	return identitycore.ShouldBindPendingOAuthIdentity(PendingAuthSessionFromEntity(session), IdentityAdoptionDecisionFromEntity(decision))
}

// shouldSkipAvatarAdoption delegates recognition of invalid-format, oversized,
// and non-image provider avatar hints.
func shouldSkipAvatarAdoption(err error) bool { return identitycore.ShouldSkipAvatarAdoption(err) }

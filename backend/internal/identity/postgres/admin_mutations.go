package postgres

import (
	"context"
	"fmt"
	"strings"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/ent/authidentitychannel"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Administrator account deletion and group replacement combine account
// permissions with API Key mutations. A configured Ent client supplies an
// owned transaction, and the selected key participant plus user repository
// receive its context so their related writes can share completion.
//
// Manual provider binding inspects compatible identity and channel records
// before creating or updating them. It rejects another owner, retains existing
// row identifiers when a compatible record is reused, and writes the canonical
// provider key with supplied metadata and verification time. Optional issuer
// and metadata inputs update existing values when present. Commit precedes the
// returned bound-identity view, while rollback is attempted on failing
// database paths.

// AdminMutations implements account deletion, group migration, and manual
// provider binding persistence.
type AdminMutations struct {
	// Client starts owned transactions; Users and the selected Keys writer
	// receive their injected context. KeysInTx takes precedence when configured,
	// and these supplied participants retain responsibility for using that
	// transaction scope.
	Client   *dbent.Client                                    // administrator mutation transaction client
	Users    identitycore.UserRepository                      // account mutation repository participant
	Keys     identitycore.AdminKeyParticipant                 // default audited API Key participant
	KeysInTx func(*dbent.Tx) identitycore.AdminKeyParticipant // transaction-bound API Key participant factory
	Observer identitycore.Observer                            // mutation failure diagnostic observer
}

// HasDatabase reports whether administrator mutation storage has an Ent client
// configured.
func (s *AdminMutations) HasDatabase() bool { return s != nil && s.Client != nil }

// DeleteUserAndKeys deletes supplied API Key summaries and their account,
// using an owned transaction when Client is configured. A configured KeysInTx
// callback selects the transaction-specific participant.
//
// The selected writer handles each positive key ID before Users.Delete runs.
// Any failure stops the sequence and reaches deferred rollback when a
// transaction was created. With no Client, the supplied participants run
// directly and their earlier writes can survive a later failure. Successful
// transactional work is committed before return; caller-provided participants
// must honor its context for combined persistence.
func (s *AdminMutations) DeleteUserAndKeys(ctx context.Context, id int64, keys []identitycore.AdminKeySummary) error {
	opCtx := ctx
	writer := s.Keys
	var tx *dbent.Tx
	if s.Client != nil {
		var err error
		tx, err = s.Client.Tx(ctx)
		if err != nil {
			return err
		}
		// An owned deletion transaction rolls back successful earlier key writes
		// when later deletion fails. Cleanup errors are discarded in favor of the
		// operation failure.
		defer func() { _ = tx.Rollback() }()
		opCtx = dbent.NewTxContext(ctx, tx)
		if s.KeysInTx != nil {
			writer = s.KeysInTx(tx)
		}
	}
	if err := s.DeleteUserWithKeys(opCtx, id, keys, writer); err != nil {
		return err
	}
	if tx != nil {
		// Commit only after audited key deletion and account deletion succeed,
		// keeping their selected transaction participants in one completed
		// administrator removal.
		return tx.Commit()
	}
	return nil
}

// DeleteUserWithKeys deletes positive key summaries through the supplied
// writer before deleting the account. A nil writer skips key operations, and
// the caller provides the persistence scope for these steps.
//
// A key failure stops later key and account writes after recording its
// diagnostic. An account failure can follow completed key deletes and also
// receives a diagnostic. The surrounding owner rolls back those earlier writes
// when the participants share its transaction; this helper creates no separate
// transaction or compensation path.
func (s *AdminMutations) DeleteUserWithKeys(ctx context.Context, id int64, keys []identitycore.AdminKeySummary, writer identitycore.AdminKeyParticipant) error {
	if writer != nil {
		for _, key := range keys {
			if key.ID <= 0 {
				continue
			}
			if err := writer.DeleteWithAudit(ctx, key.ID); err != nil {
				s.Observer.Printf("service.admin", "delete user api key failed: user_id=%d api_key_id=%d err=%v", id, key.ID, err)
				return fmt.Errorf("delete user api key %d: %w", key.ID, err)
			}
		}
	}
	if err := s.Users.Delete(ctx, id); err != nil {
		s.Observer.Printf("service.admin", "delete user failed: user_id=%d err=%v", id, err)
		return err
	}
	return nil
}

// ReplaceUserGroup grants the destination, moves matching account keys, and
// revokes the preceding grant inside an owned Ent transaction. A configured
// client and usable selected key writer are required.
//
// The destination grant precedes key migration so the moved keys have their
// new permission in the same transaction. Old-grant removal follows migration,
// and any stage failure returns before commit with rollback attempted. The
// returned count comes from the key participant; supplied user and key
// dependencies retain transaction participation responsibility.
func (s *AdminMutations) ReplaceUserGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error) {
	if s.Client == nil {
		return 0, fmt.Errorf("entClient is nil, cannot perform group replacement")
	}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	// Roll back the destination grant and any migrated keys when a later
	// replacement stage fails. Discard cleanup errors while retaining that
	// failing stage for the administrator caller.
	defer func() { _ = tx.Rollback() }()
	opCtx := dbent.NewTxContext(ctx, tx)
	writer := s.Keys
	if s.KeysInTx != nil {
		writer = s.KeysInTx(tx)
	}

	// Grant destination access before migrating keys, then remove the preceding
	// grant. The selected participants share the injected transaction so a later
	// stage can roll back the permission change.
	if err := s.Users.AddGroupToAllowedGroups(opCtx, userID, newGroupID); err != nil {
		return 0, fmt.Errorf("add new group to allowed groups: %w", err)
	}

	migrated, err := writer.UpdateGroupIDByUserAndGroup(opCtx, userID, oldGroupID, newGroupID)
	if err != nil {
		return 0, fmt.Errorf("migrate api keys: %w", err)
	}

	if err := s.Users.RemoveGroupFromUserAllowedGroups(opCtx, userID, oldGroupID); err != nil {
		return 0, fmt.Errorf("remove old group from allowed groups: %w", err)
	}

	// Commit the destination grant, migrated key references, and preceding-grant
	// removal together before reporting the migration count to the administrator
	// caller.
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return migrated, nil
}

// BindAuthIdentity writes a prepared provider identity and optional channel
// after checking compatible-record ownership. The caller validates provider
// keys, subject, account, and verification time before supplying this prepared
// input.
//
// Any compatible row owned by another account rejects the operation. An owned
// record is reused and canonicalized, retaining its identifier; missing
// records are created. Non-nil issuer and metadata replace those fields on
// existing identities, while channel linkage and supplied channel metadata are
// updated together. All stages use an owned transaction, and the returned view
// follows successful commit.
func (s *AdminMutations) BindAuthIdentity(ctx context.Context, p identitycore.PreparedAdminIdentityBinding) (*identitycore.AdminBoundAuthIdentity, error) {
	userID, providerType, providerSubject := p.UserID, p.ProviderType, p.ProviderSubject
	canonicalProviderKey, compatibleProviderKeys := p.ProviderKey, p.CompatibleKeys
	issuer, channelInput, verifiedAt := p.Issuer, p.Channel, p.VerifiedAt
	input := identitycore.AdminBindAuthIdentityInput{Metadata: p.Metadata}
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_TX_FAILED", "failed to start auth identity bind transaction").WithCause(err)
	}
	// Rollback keeps a failed channel or metadata stage from leaving its
	// preceding identity create or canonicalization committed. Cleanup errors
	// remain secondary to the binding failure.
	defer func() { _ = tx.Rollback() }()

	// Inspect every compatible provider-key record before selecting an owned row,
	// so canonicalization cannot claim an external subject already stored for
	// another account.
	identityRecords, err := tx.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ(providerType),
			authidentity.ProviderKeyIn(compatibleProviderKeys...),
			authidentity.ProviderSubjectEQ(providerSubject),
		).
		All(ctx)
	if err != nil {
		return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_LOOKUP_FAILED", "failed to inspect auth identity ownership").WithCause(err)
	}
	if AdminHasAdminAuthIdentityOwnershipConflict(identityRecords, userID) {
		return nil, infraerrors.Conflict("AUTH_IDENTITY_OWNERSHIP_CONFLICT", "auth identity already belongs to another user")
	}
	identity := AdminSelectOwnedAdminAuthIdentity(identityRecords, userID)

	if identity == nil {
		create := tx.AuthIdentity.Create().
			SetUserID(userID).
			SetProviderType(providerType).
			SetProviderKey(canonicalProviderKey).
			SetProviderSubject(providerSubject).
			SetVerifiedAt(verifiedAt)
		if issuer != nil {
			create = create.SetIssuer(*issuer)
		}
		if input.Metadata != nil {
			create = create.SetMetadata(identitycore.AdminCloneAdminAuthIdentityMetadata(input.Metadata))
		}
		identity, err = create.Save(ctx)
		if err != nil {
			return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_SAVE_FAILED", "failed to save auth identity").WithCause(err)
		}
	} else {
		update := tx.AuthIdentity.UpdateOneID(identity.ID).
			SetVerifiedAt(verifiedAt).
			SetProviderKey(canonicalProviderKey)
		if issuer != nil {
			update = update.SetIssuer(*issuer)
		}
		if input.Metadata != nil {
			update = update.SetMetadata(identitycore.AdminCloneAdminAuthIdentityMetadata(input.Metadata))
		}
		identity, err = update.Save(ctx)
		if err != nil {
			return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_SAVE_FAILED", "failed to save auth identity").WithCause(err)
		}
	}

	// After identity persistence, inspect and save channel ownership in the same
	// transaction; a channel conflict rolls back any preceding identity create or
	// update.
	var channel *dbent.AuthIdentityChannel
	if channelInput != nil {
		channelRecords, err := tx.AuthIdentityChannel.Query().
			Where(
				authidentitychannel.ProviderTypeEQ(providerType),
				authidentitychannel.ProviderKeyIn(compatibleProviderKeys...),
				authidentitychannel.ChannelEQ(channelInput.Channel),
				authidentitychannel.ChannelAppIDEQ(channelInput.ChannelAppID),
				authidentitychannel.ChannelSubjectEQ(channelInput.ChannelSubject),
			).
			WithIdentity().
			All(ctx)
		if err != nil {
			return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_CHANNEL_LOOKUP_FAILED", "failed to inspect auth identity channel ownership").WithCause(err)
		}
		if AdminHasAdminAuthIdentityChannelOwnershipConflict(channelRecords, userID) {
			return nil, infraerrors.Conflict("AUTH_IDENTITY_CHANNEL_OWNERSHIP_CONFLICT", "auth identity channel already belongs to another user")
		}
		channel = AdminSelectOwnedAdminAuthIdentityChannel(channelRecords, userID)
		if channel == nil {
			create := tx.AuthIdentityChannel.Create().
				SetIdentityID(identity.ID).
				SetProviderType(providerType).
				SetProviderKey(canonicalProviderKey).
				SetChannel(channelInput.Channel).
				SetChannelAppID(channelInput.ChannelAppID).
				SetChannelSubject(channelInput.ChannelSubject)
			if channelInput.Metadata != nil {
				create = create.SetMetadata(identitycore.AdminCloneAdminAuthIdentityMetadata(channelInput.Metadata))
			}
			channel, err = create.Save(ctx)
			if err != nil {
				return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_CHANNEL_SAVE_FAILED", "failed to save auth identity channel").WithCause(err)
			}
		} else {
			update := tx.AuthIdentityChannel.UpdateOneID(channel.ID).
				SetIdentityID(identity.ID).
				SetProviderKey(canonicalProviderKey)
			if channelInput.Metadata != nil {
				update = update.SetMetadata(identitycore.AdminCloneAdminAuthIdentityMetadata(channelInput.Metadata))
			}
			channel, err = update.Save(ctx)
			if err != nil {
				return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_CHANNEL_SAVE_FAILED", "failed to save auth identity channel").WithCause(err)
			}
		}
	}

	// Commit the selected canonical identity and optional channel before
	// constructing the returned bound view, so a response cannot claim completion
	// of an uncommitted binding.
	if err := tx.Commit(); err != nil {
		return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_COMMIT_FAILED", "failed to commit auth identity bind").WithCause(err)
	}
	return AdminBuildAdminBoundAuthIdentity(identity, channel), nil
}

// AdminSelectOwnedAdminAuthIdentity chooses the supplied account record with
// the lowest provider-key rank. Ties retain input order, absent owned records
// return nil, and callers supply nonnil records from their compatible lookup.
func AdminSelectOwnedAdminAuthIdentity(records []*dbent.AuthIdentity, userID int64) *dbent.AuthIdentity {
	var selected *dbent.AuthIdentity
	for _, record := range records {
		if record.UserID != userID {
			continue
		}
		if selected == nil || identitycore.AdminAdminAuthIdentityProviderKeyRank(record.ProviderType, record.ProviderKey) < identitycore.AdminAdminAuthIdentityProviderKeyRank(selected.ProviderType, selected.ProviderKey) {
			selected = record
		}
	}
	return selected
}

// AdminHasAdminAuthIdentityOwnershipConflict reports whether any supplied
// identity belongs to another account. Empty input returns false, and callers
// provide nonnil records from the compatible provider-subject ownership query.
func AdminHasAdminAuthIdentityOwnershipConflict(records []*dbent.AuthIdentity, userID int64) bool {
	for _, record := range records {
		if record.UserID != userID {
			return true
		}
	}
	return false
}

// AdminSelectOwnedAdminAuthIdentityChannel chooses an account-owned channel
// with the lowest provider-key rank. Missing identity edges are skipped and
// ties retain input order; callers load the channel identity relationship
// before selection.
func AdminSelectOwnedAdminAuthIdentityChannel(records []*dbent.AuthIdentityChannel, userID int64) *dbent.AuthIdentityChannel {
	var selected *dbent.AuthIdentityChannel
	for _, record := range records {
		if record.Edges.Identity == nil || record.Edges.Identity.UserID != userID {
			continue
		}
		if selected == nil || identitycore.AdminAdminAuthIdentityProviderKeyRank(record.ProviderType, record.ProviderKey) < identitycore.AdminAdminAuthIdentityProviderKeyRank(selected.ProviderType, selected.ProviderKey) {
			selected = record
		}
	}
	return selected
}

// AdminHasAdminAuthIdentityChannelOwnershipConflict reports another owner
// through a loaded channel identity edge. Missing edges do not establish a
// conflict, so callers use WithIdentity when checking compatible channel
// records.
func AdminHasAdminAuthIdentityChannelOwnershipConflict(records []*dbent.AuthIdentityChannel, userID int64) bool {
	for _, record := range records {
		if record.Edges.Identity != nil && record.Edges.Identity.UserID != userID {
			return true
		}
	}
	return false
}

// AdminBuildAdminBoundAuthIdentity maps committed identity and optional
// channel values to the administration response. Nil identity returns nil,
// metadata receives outer copies, and optional issuer and verification
// pointers remain shared.
func AdminBuildAdminBoundAuthIdentity(identity *dbent.AuthIdentity, channel *dbent.AuthIdentityChannel) *identitycore.AdminBoundAuthIdentity {
	if identity == nil {
		return nil
	}
	result := &identitycore.AdminBoundAuthIdentity{
		UserID:          identity.UserID,
		ProviderType:    strings.TrimSpace(identity.ProviderType),
		ProviderKey:     strings.TrimSpace(identity.ProviderKey),
		ProviderSubject: strings.TrimSpace(identity.ProviderSubject),
		VerifiedAt:      identity.VerifiedAt,
		Issuer:          identity.Issuer,
		Metadata:        identitycore.AdminCloneAdminAuthIdentityMetadata(identity.Metadata),
		CreatedAt:       identity.CreatedAt,
		UpdatedAt:       identity.UpdatedAt,
	}
	if channel != nil {
		result.Channel = &identitycore.AdminBoundAuthIdentityChannel{
			Channel:        strings.TrimSpace(channel.Channel),
			ChannelAppID:   strings.TrimSpace(channel.ChannelAppID),
			ChannelSubject: strings.TrimSpace(channel.ChannelSubject),
			Metadata:       identitycore.AdminCloneAdminAuthIdentityMetadata(channel.Metadata),
			CreatedAt:      channel.CreatedAt,
			UpdatedAt:      channel.UpdatedAt,
		}
	}
	return result
}

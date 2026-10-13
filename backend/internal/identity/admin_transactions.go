package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Administrator account deletion collects Key identifiers and credential text
// before its transaction request. Successful mutation requests optional
// invalidation from those observations, while a failed listing or transaction
// prevents that publication.
//
// Group replacement checks the target group before delegating grant,
// migration and revoke ordering, then reloads credentials for cache
// notification. Administrative identity binding separately prepares
// provider/compatibility/channel observations and lets storage recheck
// ownership. These entry points require the services used by their selected
// path; adapter transaction participation and callback completion remain
// distinct, so a successful result does not establish an immutable Key
// snapshot or completed cache propagation.

// BindUserAuthIdentity validates an existing account and a supported trimmed
// provider tuple before delegating administrative binding. It prepares
// compatibility keys, optional nonblank issuer and a complete channel input,
// stamps UTC operation time and requires database-backed transactions.
// Storage owns ownership rechecking/commit; this entry performs no provider
// proof request or cache invalidation.
func (s *UserAdmin) BindUserAuthIdentity(ctx context.Context, userID int64, input AdminBindAuthIdentityInput) (*AdminBoundAuthIdentity, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user_id must be greater than 0")
	}
	if s == nil || s.Transactions == nil || !s.Transactions.HasDatabase() || s.Users == nil {
		return nil, infraerrors.InternalServer("ADMIN_AUTH_IDENTITY_BIND_UNAVAILABLE", "auth identity binding service is unavailable")
	}
	if _, err := s.Users.GetByID(ctx, userID); err != nil {
		return nil, err
	}

	providerType := AdminNormalizeAdminAuthIdentityProviderType(input.ProviderType)
	providerKey := strings.TrimSpace(input.ProviderKey)
	providerSubject := strings.TrimSpace(input.ProviderSubject)
	if providerType == "" {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "provider_type must be one of email, linuxdo, oidc, wechat, or dingtalk")
	}
	if providerKey == "" || providerSubject == "" {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "provider_type, provider_key, and provider_subject are required")
	}
	canonicalProviderKey := AdminCanonicalAdminAuthIdentityProviderKey(providerType, "", providerKey)
	compatibleProviderKeys := AdminCompatibleAdminAuthIdentityProviderKeys(providerType, providerKey)

	var issuer *string
	if input.Issuer != nil {
		trimmed := strings.TrimSpace(*input.Issuer)
		if trimmed != "" {
			issuer = &trimmed
		}
	}

	channelInput := AdminNormalizeAdminBindChannelInput(input.Channel)
	if input.Channel != nil && channelInput == nil {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "channel, channel_app_id, and channel_subject are required when channel binding is provided")
	}

	verifiedAt := s.now().UTC()
	return s.Transactions.BindAuthIdentity(ctx, PreparedAdminIdentityBinding{UserID: userID, ProviderType: providerType, ProviderKey: canonicalProviderKey, ProviderSubject: providerSubject, CompatibleKeys: compatibleProviderKeys, Issuer: issuer, Metadata: input.Metadata, Channel: channelInput, VerifiedAt: verifiedAt})
}

// DeleteUser deletes a nonadministrator and its collected Key records before
// publishing optional authentication invalidation. It requires assembled user
// and transaction services, rejects administrator accounts before
// listing/deletion and stops on a failed key listing or mutation.
//
// The adapter receives the previously collected IDs/secrets and owns actual
// transaction participation. Cache callbacks run only after a successful
// mutation return, using trimmed nonblank snapshotted secrets and the account
// ID. These reads do not reserve a consistent Key snapshot, and publication
// has no returned error, so callers keep mutation success separate from
// confirmation that every changing credential reached cache consumers.
func (s *UserAdmin) DeleteUser(ctx context.Context, id int64) error {
	user, err := s.Users.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if user.Role == "admin" {
		return errors.New("cannot delete admin user")
	}

	apiKeys, err := s.ListKeysForDeletion(ctx, id)
	if err != nil {
		return err
	}

	// Pass the collected Key IDs before invalidation; storage decides actual
	// transaction participation and a failed mutation returns before publishing
	// the saved credential/account changes.
	if err := s.Transactions.DeleteUserAndKeys(ctx, id, apiKeys); err != nil {
		return err
	}

	// Mutation returned successfully; invalidate each saved nonblank credential
	// and the account so cache consumers stop relying on the removed user and
	// listed Keys.
	if s.Invalidator != nil {
		for _, key := range apiKeys {
			if keyValue := strings.TrimSpace(key.Key); keyValue != "" {
				s.Invalidator.InvalidateAuthCacheByKey(ctx, keyValue)
			}
		}
		s.Invalidator.InvalidateAuthCacheByUserID(ctx, id)
	}
	return nil
}

// ListKeysForDeletion collects account Key records in ascending ID pages for
// a later deletion request. Missing Key reader returns nil without error;
// lookup failure returns a wrapped error before the caller starts mutation.
//
// An empty/short batch or accumulated count reaching the latest reported
// total ends collection. Each page is a separate observation, so concurrent
// Key changes can affect the collected snapshot. The transaction adapter
// deletes the collected positive IDs, and the caller uses their saved
// credential text for post-mutation invalidation; this helper provides
// neither locks nor a deletion reservation.
func (s *UserAdmin) ListKeysForDeletion(ctx context.Context, userID int64) ([]AdminKeySummary, error) {
	if s.Keys == nil {
		return nil, nil
	}

	// Use bounded Key batches to collect credentials for post-deletion invalidation.
	const pageSize = 1000
	keys := make([]AdminKeySummary, 0)
	for page := 1; ; page++ {
		batch, total, err := s.Keys.List(ctx, userID, page, pageSize, "id", pagination.SortOrderAsc)
		if err != nil {
			return nil, fmt.Errorf("list user api keys: %w", err)
		}
		keys = append(keys, batch...)
		if len(batch) == 0 || len(batch) < pageSize || int64(len(keys)) >= total {
			break
		}
	}
	return keys, nil
}

// ReplaceUserGroup validates a distinct target group as active and exclusive,
// then delegates access-grant and matching-Key migration. The mutation
// adapter grants target access before moving old-group Keys and removing old
// access, with completion owned by its transaction implementation.
//
// After success, optional invalidation reloads all account Key strings rather
// than only migrated ones because grants also affect authentication. Key-list
// failure is suppressed and the migrated count is still returned. Callers
// assemble Groups/Transactions and, when invalidation is supplied, Keys;
// target preflight and later mutation remain separate observations.
func (s *UserAdmin) ReplaceUserGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (*ReplaceUserGroupResult, error) {
	if oldGroupID == newGroupID {
		return nil, infraerrors.BadRequest("SAME_GROUP", "old and new group must be different")
	}

	newGroup, err := s.Groups.GetByID(ctx, newGroupID)
	if err != nil {
		return nil, err
	}
	if newGroup.Status != StatusActive {
		return nil, infraerrors.BadRequest("GROUP_NOT_ACTIVE", "target group is not active")
	}
	if !newGroup.IsExclusive {
		return nil, infraerrors.BadRequest("GROUP_NOT_EXCLUSIVE", "target group is not exclusive")
	}

	migrated, err := s.Transactions.ReplaceUserGroup(ctx, userID, oldGroupID, newGroupID)
	if err != nil {
		return nil, err
	}

	// Reload account credentials after grant/migration/revoke completion because
	// changed group access affects every authentication snapshot; a failed read
	// leaves publication skipped while the mutation remains successful.
	if s.Invalidator != nil {
		keys, keyErr := s.Keys.ListKeysByUserID(ctx, userID)
		if keyErr == nil {
			for _, k := range keys {
				s.Invalidator.InvalidateAuthCacheByKey(ctx, k)
			}
		}
	}

	return &ReplaceUserGroupResult{MigratedKeys: migrated}, nil
}

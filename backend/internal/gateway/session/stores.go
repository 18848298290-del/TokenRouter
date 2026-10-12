package session

import (
	"context"
	"errors"
	"time"
)

// Gateway session stores supply sticky routing, first-group ownership, and optional
// capabilities used by Responses, Live, and asynchronous video completion. The
// interfaces expose storage results so callers can distinguish a miss, an
// unavailable store, and an ownership comparison that rejected a stale claimant.

// ErrReasoningContentNotFound identifies an absent reasoning item so conversion
// callers can continue with other history evidence when this cache has no text.
var ErrReasoningContentNotFound = errors.New("reasoning content not found")

// OpenAIWSSessionPreemptionCache atomically replaces and compares shared Responses
// session-window owners. GatewayCache implementations may supply this capability;
// ordinary sticky-session adapters can omit it, and consumers discover it separately.
type OpenAIWSSessionPreemptionCache interface {
	// ClaimOpenAIResponsesSessionWindow replaces the group/session owner atomically and
	// returns the previous serialized owner, or nil when absent. Callers supply
	// nonempty owner bytes and a positive TTL; storage failures return an error.
	ClaimOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, owner []byte, ttl time.Duration) ([]byte, error)
	// CompareAndRefreshOpenAIResponsesSessionWindow extends lifetime when stored owner
	// bytes equal expected. Missing or different ownership returns false; callers
	// supply nonempty expected bytes and a positive TTL, and storage failures return errors.
	CompareAndRefreshOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte, ttl time.Duration) (bool, error)
	// CompareAndDeleteOpenAIResponsesSessionWindow removes a matching group/session
	// owner atomically. False means the entry is absent or belongs to another owner,
	// so stale cleanup cannot release a newer claimant's entry.
	CompareAndDeleteOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte) (bool, error)
}

// CyberSessionBlockStore provides block writes and scoped transcript lookup operations.
// AdaptCyberSessionBlockStore selects this optional capability or its legacy
// single-key adapter; a cache supporting neither disables this storage path.
type CyberSessionBlockStore interface {
	// SetCyberSessionBlocked writes supplied block evidence for ttl. Production
	// Redis writes bounded exact-key batches before activating their source scope,
	// so activation follows successful writes. An exact-write error can leave
	// earlier batches present and skips the later scope write.
	//
	// The legacy adapter writes one selected key: trimmed nonblank scope, otherwise
	// the first supplied candidate. A blank selection skips writing. CyberBlocks
	// logs errors as advisory persistence failures; successful legacy adaptation does
	// not confirm that every supplied exact candidate was stored.
	SetCyberSessionBlocked(ctx context.Context, scopeKey string, keys []string, ttl time.Duration) error
	// IsCyberSessionScopeActive reports whether the source scope enables transcript
	// lookup. A scope hit is coarse evidence; CyberBlocks still checks exact transcript
	// keys, while read failures are returned for its fail-open decision.
	IsCyberSessionScopeActive(ctx context.Context, scopeKey string) (bool, error)
	// FindCyberSessionBlocked returns the first blocked candidate in caller order, or
	// empty when none match. It returns storage errors separately so CyberBlocks can
	// log the failure and allow the request.
	FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error)
}

// GatewayCache supplies provider bindings and first-group ownership for session routing.
type GatewayCache interface {
	// GetSessionProviderID reads the provider bound to the group/session pair.
	// A missing binding or unavailable storage is represented by an error; callers
	// then apply their selection policy to the unbound session.
	GetSessionProviderID(ctx context.Context, groupID int64, sessionHash string) (int64, error)
	// SetSessionProviderID replaces the provider binding for the group/session pair.
	// The supplied TTL controls lifetime, and storage errors let the caller decide
	// whether routing should continue after an unsuccessful binding write.
	SetSessionProviderID(ctx context.Context, groupID int64, sessionHash string, providerID int64, ttl time.Duration) error
	// RefreshSessionTTL updates the existing group/session binding's lifetime to ttl.
	// It does not create a provider binding; callers use it after a usable sticky
	// lookup and handle storage failures separately.
	RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error
	// DeleteSessionProviderID removes the group's provider binding for sessionHash.
	// Callers use removal when a sticky target becomes unusable; an absent entry is
	// already removed and storage failures are returned.
	DeleteSessionProviderID(ctx context.Context, groupID int64, sessionHash string) error
	// SetSessionOwnerGroupID atomically inserts the group's first ownership record for
	// a user, source, and hash. True identifies this insertion; false leaves an
	// existing owner intact for EnsureIsolation to read and compare.
	SetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string, groupID int64, ttl time.Duration) (bool, error)
	// GetSessionOwnerGroupID reads the first group recorded for a user/source/hash
	// identity. A miss returns an error, allowing EnsureIsolation to retry insertion
	// when ownership expired after an unsuccessful first-owner write.
	GetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string) (int64, error)
	// RefreshSessionOwnerTTL extends an existing user/source/hash ownership record to
	// ttl. It leaves the recorded group unchanged; EnsureIsolation calls it after
	// matching the owner group and propagates storage errors.
	RefreshSessionOwnerTTL(ctx context.Context, userID int64, source, sessionHash string, ttl time.Duration) error
}

// ReasoningContentCache supplies item-ID reasoning text for Responses-to-Chat conversion.
type ReasoningContentCache interface {
	// SetReasoningContent stores text under itemID for the supplied lifetime.
	// The production cache ignores empty IDs or text and defaults nonpositive TTL;
	// write failures are returned to the caller's warning policy.
	SetReasoningContent(ctx context.Context, itemID string, content string, ttl time.Duration) error
	// GetReasoningContent returns reasoning text under itemID or an error on failure.
	// The production cache uses ErrReasoningContentNotFound for an empty ID or miss;
	// ReasoningHistory treats every read error as unavailable text.
	GetReasoningContent(ctx context.Context, itemID string) (string, error)
}

// GrokVideoBillingCache stores video billing snapshots and expiring settlement claims.
// A successful claim selects a caller while its key exists; settlement persistence
// and release after failure remain the caller's responsibility.
type GrokVideoBillingCache interface {
	// SetGrokVideoPendingBilling stores serialized creation-time pricing and ownership
	// for later polling. Callers supply a nonempty key and payload; production Redis
	// uses a one-day default when ttl is nonpositive.
	SetGrokVideoPendingBilling(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	// GetGrokVideoPendingBilling retrieves the serialized snapshot for a nonempty key.
	// A missing production Redis record returns nil with nil error, while storage
	// failures remain distinguishable for the polling caller.
	GetGrokVideoPendingBilling(ctx context.Context, key string) ([]byte, error)
	// ClaimGrokVideoBilled inserts an expiring billing claim when the key is absent.
	// True selects this caller; false indicates an existing claim. The caller settles
	// the video and releases its claim when settlement fails.
	ClaimGrokVideoBilled(ctx context.Context, key string, ttl time.Duration) (bool, error)
	// ReleaseGrokVideoBilled removes a failed settlement's claim so later polling can
	// retry billing. The claim has no owner token, so callers coordinate release with
	// their settlement lifetime and handle removal errors.
	ReleaseGrokVideoBilled(ctx context.Context, key string) error
}

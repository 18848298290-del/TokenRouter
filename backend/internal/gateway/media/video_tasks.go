package media

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// video_tasks.go stores video task ownership and creation billing snapshots
// and manages polling billing claims.
// A Grok video task is bound to user, API key, group and provider so later
// status/content polling can recover its execution owner. Creation
// snapshots separately retain model, resolution, duration and creation
// start time for delayed billing. Redis accepts a claim only when its key is
// absent, while persistent billing deduplication remains with the recorder.
//
// Provider binding is written before owner-group claim validation, and a
// later conflict or cache error leaves earlier writes in place. Saved
// owner/provider lookup precedes scanning current composite bindings,
// allowing ownership recovery after a group mapping disappears. Creation
// tracking submits request aliases, then performs best-effort binding and
// snapshot storage with one immediate retry. Those updates have no rollback
// or common transaction, so observers report individual cache failures.

const (
	// VideoPendingTTL retains creation billing snapshots, while VideoClaimTTL
	// retains claim keys. Provider bindings request at least the pending
	// lifetime so polling can use those snapshots.
	VideoPendingTTL = 24 * time.Hour // creation snapshot retention duration
	VideoClaimTTL   = 48 * time.Hour // billing claim retention duration
	// grokMediaVideoRequestOwnerSource selects the owner-group cache namespace
	// for video task hashes, separating those lookups from other session-owner
	// sources.
	grokMediaVideoRequestOwnerSource = "grok_video_request" // video task owner-cache namespace
)

// VideoOptions controls how long task-owner bindings remain available for
// polling.
type VideoOptions struct {
	// StickyTTL extends provider and owner binding retention only when
	// positive and longer than VideoPendingTTL. Zero or shorter values leave
	// the pending lifetime as the requested binding minimum.
	StickyTTL time.Duration // optional longer binding retention duration
}

// VideoTasks binds task ownership and delayed billing through retained
// cache interfaces.
type VideoTasks struct {
	// owners is required for ownership and billing operations, including
	// methods that then use billing. NewVideoTasks retains this interface
	// directly, and each cache implementation owns synchronization and
	// cancellation.
	owners session.GatewayCache // shared task owner/provider cache
	// billing is required by snapshot and claim methods after owner-cache
	// validation. Its claim lifetime is independent of recorded settlement,
	// and the Redis implementation releases claims by key deletion.
	billing session.GrokVideoBillingCache // shared pending snapshot and claim cache
	options VideoOptions                  // copied video binding lifetime options
}

// GrokVideoPendingBilling retains scalar creation evidence for later
// completed-task billing.
type GrokVideoPendingBilling struct {
	// Model records the routed creation request model and fills missing
	// completion names after BillingModel. OriginalModel remains a later
	// client-name fallback instead of replacing routed creation identity.
	Model string `json:"model"` // creation request model label
	// BillingModel has precedence when completion model or billing model is
	// missing. The creation adapter captures its selected result billing name
	// or falls back to the routed request model.
	BillingModel string `json:"billing_model,omitempty"` // selected creation billing model
	// UpstreamModel retains the provider execution name for missing completion
	// upstream identity. It is kept separate from client and billing model
	// selection during merging.
	UpstreamModel string `json:"upstream_model,omitempty"` // selected provider execution model
	// VideoResolution takes precedence over completion resolution when
	// nonblank. Store normalizes any nonempty value through pricing, while an
	// empty value remains for later completion defaulting.
	VideoResolution string `json:"video_resolution,omitempty"` // optional creation video resolution
	// VideoDurationSeconds fills nonpositive observed duration. Store
	// normalizes only positive values to the shared range, retaining
	// nonpositive evidence until completion supplies a default.
	VideoDurationSeconds int `json:"video_duration_seconds,omitempty"` // optional creation duration in seconds
	// OriginalModel is retained as the final pending candidate for a missing
	// response Model. It does not override a supplied BillingModel or routed
	// Model during completion selection.
	OriginalModel string `json:"original_model,omitempty"` // client-requested original model name
	// CreatedAt normally holds a UTC RFC3339Nano duration origin. The HTTP media
	// entry sets it before provider attempts; Store fills a blank value from
	// its storage clock and retains other trimmed spellings. Invalid timestamps
	// yield zero end-to-end duration later.
	CreatedAt string `json:"created_at,omitempty"` // serialized creation duration origin
}

// VideoBinding captures one composite-key group and its current
// availability.
type VideoBinding struct {
	// GroupID and Present come from the authentication binding list.
	// ResolveCompositeVideo scans only Present bindings, while saved ownership
	// can return a group absent from that current list.
	GroupID int64 // authentication snapshot group identifier
	Present bool  // group binding currently has data
}

// VideoOwner identifies the task's resolved group/provider and matching
// current binding.
type VideoOwner struct {
	// GroupID and ProviderID identify one recovered task execution owner. They
	// can come from saved owner/provider state or the first current binding
	// whose provider lookup succeeds.
	GroupID, ProviderID int64 // resolved task group identifier and resolved execution provider identifier
	// BindingIndex is -1 when saved ownership resolves but no Present current
	// binding matches the group. The entry adapter can restore a minimal query
	// group for that result.
	BindingIndex int // current binding index or -1
}

// NewVideoTasks retains cache interfaces and copies lifetime options into a
// task helper. It performs no availability validation; ownership calls
// require owners, while snapshot and claim calls require both owners and
// billing.
func NewVideoTasks(owners session.GatewayCache, billing session.GrokVideoBillingCache, options VideoOptions) *VideoTasks {
	return &VideoTasks{owners: owners, billing: billing, options: options}
}

// videoSessionHash returns the scheduler's current session digest for the
// supplied seed.
func videoSessionHash(seed string) string {
	current, _ := scheduler.DeriveSessionHashes(seed)
	return current
}

// videoSessionCacheKey trims a nonblank video session hash and prefixes it
// for the OpenAI session-provider cache. Blank input returns empty,
// allowing callers to reject an unavailable owner key before cache access.
func videoSessionCacheKey(hash string) string {
	if strings.TrimSpace(hash) == "" {
		return ""
	}
	return "openai:" + strings.TrimSpace(hash)
}

// derefGroupID returns a supplied group value or zero for nil. Zero is the
// ungrouped cache lookup value; nonpositive pointed values are retained for
// the cache caller's own handling.
func derefGroupID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// GrokMediaVideoRequestSessionHash derives video ownership identity from
// trimmed task request ID, positive user ID and positive API key ID.
// Invalid identity returns empty before hashing or cache access.
//
// The decimal user/key values precede task text in one seed passed to the
// scheduler's current hash, then receive the grok-video prefix. Both
// account and key therefore participate in the lookup identity for the same
// task spelling. Callers retain the actual task ID consistently; the helper
// does no alias expansion or authorization lookup itself.
func GrokMediaVideoRequestSessionHash(requestID string, userID, apiKeyID int64) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || userID <= 0 || apiKeyID <= 0 {
		return ""
	}
	ownerSeed := fmt.Sprintf("%d:%d:%s", userID, apiKeyID, requestID)
	return "grok-video:" + videoSessionHash(ownerSeed)
}

// BindGrokMediaVideoRequestProvider requests provider and owner-group
// binding for valid task/user/key and positive provider identity. Nil
// receiver/owner cache returns unavailable; retention uses the longer of
// pending lifetime and a positive StickyTTL.
//
// Provider state is written first using the dereferenced group. Nil or
// nonpositive group then ends without owner-group state. Positive group
// uses owner claim; an existing same owner refreshes TTL, while a different
// owner returns conflict. Later claim/read/refresh failure does not roll
// back provider state, so callers handle each reported failure rather than
// assume both entries changed atomically.
func (s *VideoTasks) BindGrokMediaVideoRequestProvider(
	ctx context.Context,
	groupID *int64,
	requestID string,
	userID, apiKeyID, providerID int64,
) error {
	if s == nil || s.owners == nil {
		return fmt.Errorf("grok video request binding cache is unavailable")
	}
	sessionHash := GrokMediaVideoRequestSessionHash(requestID, userID, apiKeyID)
	cacheKey := videoSessionCacheKey(sessionHash)
	if cacheKey == "" || providerID <= 0 {
		return fmt.Errorf("grok video request binding is invalid")
	}
	// Keep the requested binding lifetime at least as long as pending billing,
	// extending it for longer positive stickiness so later polls can still
	// resolve the selected provider.
	ttl := VideoPendingTTL
	if s.options.StickyTTL > 0 {
		if sticky := s.options.StickyTTL; sticky > ttl {
			ttl = sticky
		}
	}
	// Write provider binding before owner claim validation; a later owner
	// conflict or failure leaves this write in place, so this sequence
	// provides no rollback.
	if err := s.owners.SetSessionProviderID(ctx, derefGroupID(groupID), cacheKey, providerID, ttl); err != nil {
		return err
	}
	if groupID == nil || *groupID <= 0 {
		return nil
	}
	// Claim positive group ownership after provider state is available; an
	// existing owner must match before its TTL can be refreshed for these
	// polls.
	written, err := s.owners.SetSessionOwnerGroupID(ctx, userID, grokMediaVideoRequestOwnerSource, sessionHash, *groupID, ttl)
	if err != nil {
		return err
	}
	if !written {
		ownerGroupID, getErr := s.owners.GetSessionOwnerGroupID(ctx, userID, grokMediaVideoRequestOwnerSource, sessionHash)
		if getErr != nil {
			return getErr
		}
		if ownerGroupID != *groupID {
			return fmt.Errorf("grok video request binding belongs to another group")
		}
		// Refresh an existing matching owner after validation, retaining its
		// group identity while extending the owner key to the same requested
		// binding lifetime.
		return s.owners.RefreshSessionOwnerTTL(ctx, userID, grokMediaVideoRequestOwnerSource, sessionHash, ttl)
	}
	return nil
}

// ResolveGrokMediaVideoRequestGroup looks up the saved group for trimmed
// task ID and positive user/key identity. Invalid identity or missing owner
// cache returns an error; cache miss and cancellation results come from
// owners.
func (s *VideoTasks) ResolveGrokMediaVideoRequestGroup(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) (int64, error) {
	if s == nil || s.owners == nil {
		return 0, fmt.Errorf("grok video request binding cache is unavailable")
	}
	sessionHash := GrokMediaVideoRequestSessionHash(requestID, userID, apiKeyID)
	if sessionHash == "" {
		return 0, fmt.Errorf("grok video request binding is invalid")
	}
	return s.owners.GetSessionOwnerGroupID(ctx, userID, grokMediaVideoRequestOwnerSource, sessionHash)
}

// ResolveGrokMediaVideoRequestProvider looks up task provider using
// supplied group and task/user/key identity. Nil group maps to zero through
// derefGroupID; invalid owner key or missing owner cache returns an error
// before lookup.
func (s *VideoTasks) ResolveGrokMediaVideoRequestProvider(
	ctx context.Context,
	groupID *int64,
	requestID string,
	userID, apiKeyID int64,
) (int64, error) {
	if s == nil || s.owners == nil {
		return 0, fmt.Errorf("grok video request binding cache is unavailable")
	}
	cacheKey := videoSessionCacheKey(GrokMediaVideoRequestSessionHash(requestID, userID, apiKeyID))
	if cacheKey == "" {
		return 0, fmt.Errorf("grok video request binding is invalid")
	}
	return s.owners.GetSessionProviderID(ctx, derefGroupID(groupID), cacheKey)
}

// GrokVideoPendingCreatedAtNow returns the current UTC time encoded with
// RFC3339Nano precision.
func GrokVideoPendingCreatedAtNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// GrokVideoE2EDuration returns nonnegative elapsed time from the saved
// creation origin to discovery. Blank, invalid or zero creation timestamps
// return zero, and a zero discoveredAt uses the current clock.
//
// Parsing tries RFC3339Nano and then RFC3339; discovery before that origin
// also returns zero rather than a negative recorded interval.
// PrepareCompletion replaces polling duration only for a positive result,
// so missing or inconsistent timestamp evidence leaves the original poll
// duration available to that consumer. Equal creation/discovery timestamps
// likewise supply zero.
func GrokVideoE2EDuration(createdAt string, discoveredAt time.Time) time.Duration {
	createdAt = strings.TrimSpace(createdAt)
	if createdAt == "" {
		return 0
	}
	if discoveredAt.IsZero() {
		discoveredAt = time.Now()
	}
	var created time.Time
	var err error
	if created, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		// Try the second accepted timestamp layout after the first parse fails;
		// unusable time evidence returns zero so completion can retain its
		// polling duration.
		if created, err = time.Parse(time.RFC3339, createdAt); err != nil {
			return 0
		}
	}
	if created.IsZero() {
		return 0
	}
	d := discoveredAt.Sub(created)
	// Discovery before the creation origin yields zero instead of negative elapsed
	// duration.
	if d < 0 {
		return 0
	}
	return d
}

// VideoPendingKey joins positive user/key IDs with trimmed nonblank task ID
// for pending and claim caches. Invalid identity returns empty; callers use
// the same task spelling for storage, claim and release.
func VideoPendingKey(requestID string, userID, apiKeyID int64) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || userID <= 0 || apiKeyID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d:%s", userID, apiKeyID, requestID)
}

// StoreGrokVideoPendingBilling normalizes and JSON-encodes a value copy of
// creation evidence, then stores it for VideoPendingTTL. Both owner and
// billing cache interfaces are required, and invalid task/user/key identity
// returns an error.
//
// Model names are trimmed, nonempty resolution and positive duration use
// shared pricing normalization, and blank CreatedAt receives a clock
// timestamp. Nonpositive duration and empty resolution stay unresolved for
// later completion handling. A nonblank timestamp is trimmed but not
// validated here; caller pending storage stays unchanged because
// normalization applies to the local value copy.
func (s *VideoTasks) StoreGrokVideoPendingBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
	pending GrokVideoPendingBilling,
) error {
	if s == nil || s.owners == nil {
		return fmt.Errorf("grok video pending billing cache is unavailable")
	}
	key := VideoPendingKey(requestID, userID, apiKeyID)
	if key == "" {
		return fmt.Errorf("grok video pending billing key is invalid")
	}
	pending.Model = strings.TrimSpace(pending.Model)
	pending.BillingModel = strings.TrimSpace(pending.BillingModel)
	pending.UpstreamModel = strings.TrimSpace(pending.UpstreamModel)
	pending.OriginalModel = strings.TrimSpace(pending.OriginalModel)
	// Normalize supplied resolution and positive duration before encoding,
	// while leaving absent resolution and nonpositive duration for the later
	// completion defaults.
	if pending.VideoResolution != "" {
		pending.VideoResolution = pricing.NormalizeVideoBillingResolutionOrDefault(pending.VideoResolution)
	}
	if pending.VideoDurationSeconds > 0 {
		pending.VideoDurationSeconds = pricing.NormalizeVideoBillingDurationSecondsOrDefault(pending.VideoDurationSeconds)
	}
	// Fill only a missing creation origin during this storage attempt; a
	// retry receives the caller's original value and can choose another
	// timestamp when that value was blank.
	if strings.TrimSpace(pending.CreatedAt) == "" {
		pending.CreatedAt = GrokVideoPendingCreatedAtNow()
	} else {
		pending.CreatedAt = strings.TrimSpace(pending.CreatedAt)
	}
	payload, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	cache := s.billing
	if cache == nil {
		return fmt.Errorf("grok video pending billing cache is unavailable")
	}
	return cache.SetGrokVideoPendingBilling(ctx, key, payload, VideoPendingTTL)
}

// LoadGrokVideoPendingBilling decodes a retained creation snapshot using
// valid task/user/key identity and both caches. Empty payload returns nil
// with the cache error, while malformed JSON returns a decoding error;
// valid payload produces a new scalar snapshot.
func (s *VideoTasks) LoadGrokVideoPendingBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) (*GrokVideoPendingBilling, error) {
	if s == nil || s.owners == nil {
		return nil, fmt.Errorf("grok video pending billing cache is unavailable")
	}
	key := VideoPendingKey(requestID, userID, apiKeyID)
	if key == "" {
		return nil, fmt.Errorf("grok video pending billing key is invalid")
	}
	cache := s.billing
	if cache == nil {
		return nil, fmt.Errorf("grok video pending billing cache is unavailable")
	}
	payload, err := cache.GetGrokVideoPendingBilling(ctx, key)
	// Treat empty cache payload as no creation snapshot, preserving any
	// accompanying cache error so callers can observe failure before applying
	// measured-status fallback.
	if err != nil || len(payload) == 0 {
		return nil, err
	}
	var pending GrokVideoPendingBilling
	if err := json.Unmarshal(payload, &pending); err != nil {
		return nil, err
	}
	return &pending, nil
}

// ClaimGrokVideoBilling delegates a VideoClaimTTL claim for valid
// task/user/key identity and both configured caches. False or error
// prevents preparation from proceeding; Redis SET NX accepts only when the
// claim key is absent, separately from persistent settlement deduplication.
func (s *VideoTasks) ClaimGrokVideoBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) (bool, error) {
	if s == nil || s.owners == nil {
		return false, fmt.Errorf("grok video billing claim cache is unavailable")
	}
	key := VideoPendingKey(requestID, userID, apiKeyID)
	if key == "" {
		return false, fmt.Errorf("grok video billing claim key is invalid")
	}
	cache := s.billing
	if cache == nil {
		return false, fmt.Errorf("grok video billing claim cache is unavailable")
	}
	// Delegate claim acquisition with its retention lifetime. Redis SET NX
	// accepts when the key is absent; the recorder separately owns persistent
	// billing deduplication.
	return cache.ClaimGrokVideoBilled(ctx, key, VideoClaimTTL)
}

// ReleaseGrokVideoBilling delegates release of a task/user/key claim after
// the recording adapter reports Record error. The Redis implementation
// deletes by key without claimant-token verification; callers coordinate
// release, and this operation rolls back no persisted billing state.
func (s *VideoTasks) ReleaseGrokVideoBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) error {
	if s == nil || s.owners == nil {
		return fmt.Errorf("grok video billing claim cache is unavailable")
	}
	key := VideoPendingKey(requestID, userID, apiKeyID)
	if key == "" {
		return fmt.Errorf("grok video billing claim key is invalid")
	}
	cache := s.billing
	if cache == nil {
		return fmt.Errorf("grok video billing claim cache is unavailable")
	}
	// Release the cache key through its implementation after recording error,
	// permitting a later poll to seek a claim without undoing any
	// recorder-side effects.
	return cache.ReleaseGrokVideoBilled(ctx, key)
}

// StableGrokVideoBillingRequestID prefixes a trimmed task ID with
// grok-video for billing correlation. Empty returns empty and an existing
// grok-video prefix is retained; cross-poll deduplication requires callers
// to supply the same task identity.
func StableGrokVideoBillingRequestID(taskRequestID string) string {
	taskRequestID = strings.TrimSpace(taskRequestID)
	if taskRequestID == "" {
		return ""
	}
	if strings.HasPrefix(taskRequestID, "grok-video:") {
		return taskRequestID
	}
	return "grok-video:" + taskRequestID
}

// TrackCreated submits related-request/task/billing aliases to any request
// recorder attached to ctx, then attempts ownership binding and creation
// snapshot storage. Nil observer skips cache-failure notices, and the
// method returns no aggregate success value.
//
// Binding error is reported while snapshot storage still proceeds. First
// storage failure is observed and retried once immediately; second failure
// is also observed, with no rollback of telemetry or earlier binding
// writes. The original pending value is passed again, so Store can choose a
// fresh timestamp on retry when CreatedAt was blank. Creation timestamp parse
// failure supplies zero StartedAt in the submitted telemetry input; its
// recorder owns further normalization and retention.
func (s *VideoTasks) TrackCreated(ctx context.Context, groupID *int64, taskID string, userID, keyID, providerID int64, pending GrokVideoPendingBilling, observer VideoObserver) {
	billingKey := StableGrokVideoBillingRequestID(taskID)
	createdAt, _ := time.Parse(time.RFC3339Nano, pending.CreatedAt)
	// Submit creation time and task/billing aliases before cache writes when a
	// request recorder is attached; later cache failure does not compensate
	// that earlier submission.
	telemetry.RecordRelatedRequest(ctx, telemetry.RequestRecord{RequestID: telemetry.StableRequestID(fmt.Sprintf("%d/%s", keyID, billingKey)), StartedAt: createdAt, State: "running", UserID: userID, APIKeyID: keyID, ProviderID: providerID, Model: pending.Model, Aliases: []telemetry.RequestAlias{{Kind: "task", Value: taskID}, {Kind: "billing", Value: billingKey}}})

	if err := s.BindGrokMediaVideoRequestProvider(ctx, groupID, taskID, userID, keyID, providerID); err != nil {
		videoNotice(observer, VideoNotice{Kind: "bind_failed", TaskID: taskID, ProviderID: providerID, Err: err})
	}
	// A failed first snapshot write gets one immediate retry with the original
	// value, reporting both failure stages while leaving earlier ownership or
	// telemetry effects in place.
	if err := s.StoreGrokVideoPendingBilling(ctx, taskID, userID, keyID, pending); err != nil {
		videoNotice(observer, VideoNotice{Kind: "store_retry", TaskID: taskID, ProviderID: providerID, Err: err})
		if retryErr := s.StoreGrokVideoPendingBilling(ctx, taskID, userID, keyID, pending); retryErr != nil {
			videoNotice(observer, VideoNotice{Kind: "store_failed", TaskID: taskID, ProviderID: providerID, Err: retryErr})
		}
	}
}

// ResolveCompositeVideo returns usable saved owner/provider state before
// scanning current bindings for legacy tasks. Saved positive owner/provider
// can return BindingIndex -1 when its group is no longer in the Present
// binding list.
//
// When saved lookup is unusable, each Present current binding is tried in
// order and the first positive provider wins. Lookup errors are retained as
// later candidates are considered; if none succeeds, the last available
// error is returned or a not-found error is constructed. Current bindings
// provide the fallback groups, while task/user/key hashing keeps those
// provider lookups associated with the supplied task identity.
func (s *VideoTasks) ResolveCompositeVideo(ctx context.Context, taskID string, userID, keyID int64, bindings []VideoBinding) (VideoOwner, error) {
	var lookupErr error
	// Try saved group and provider first so a task can resolve after its
	// mapping is removed, using -1 to signal no current binding index.
	ownerID, err := s.ResolveGrokMediaVideoRequestGroup(ctx, taskID, userID, keyID)
	if err == nil && ownerID > 0 {
		providerID, providerErr := s.ResolveGrokMediaVideoRequestProvider(ctx, &ownerID, taskID, userID, keyID)
		if providerErr == nil && providerID > 0 {
			index := -1
			for i, binding := range bindings {
				if binding.GroupID == ownerID && binding.Present {
					index = i
					break
				}
			}
			return VideoOwner{GroupID: ownerID, ProviderID: providerID, BindingIndex: index}, nil
		}
		lookupErr = providerErr
	} else if err != nil {
		lookupErr = err
	}
	// Scan available current bindings only after saved ownership cannot supply
	// a usable provider, retaining lookup errors until a candidate succeeds or
	// the scan ends.
	for i, binding := range bindings {
		if !binding.Present {
			continue
		}
		id := binding.GroupID
		providerID, err := s.ResolveGrokMediaVideoRequestProvider(ctx, &id, taskID, userID, keyID)
		if err != nil {
			lookupErr = err
			continue
		}
		if providerID <= 0 {
			continue
		}
		return VideoOwner{GroupID: id, ProviderID: providerID, BindingIndex: i}, nil
	}
	if lookupErr == nil {
		lookupErr = fmt.Errorf("grok video request binding not found")
	}
	return VideoOwner{}, lookupErr
}

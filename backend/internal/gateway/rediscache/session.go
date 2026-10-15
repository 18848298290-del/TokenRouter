package rediscache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	schedulerredis "github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache"
)

// session.go stores gateway session state in Redis and arbitrates
// response-window ownership and Live controller transitions.
// Gateway session keys coordinate response-window owners, deferred video
// billing, reasoning recovery, transcript blocks, and saved Live calls through
// one Redis client. Atomic scripts replace or compare an owner before changing
// its window, while Live scripts qualify stored controller transitions and
// deduplicate closure claims.
//
// Transcript blocking publishes bounded exact-key batches before a coarse
// scope marker, so a fresh marker does not advertise work that has not
// completed. Earlier successful batches survive a later error. Live hashes
// retain the adapter's serialized field subset, with creation and expiry
// encoded in milliseconds and model mappings as JSON; ActorUserID and TeamID
// are omitted. Logical call expiry remains separate from the Redis TTL and
// closure-marker retention. Billing claim deletion uses its key alone, leaving
// persistent settlement deduplication to the billing layer.

const (
	// Response-window and Live-call prefixes partition their stored controller
	// state; callers supply the group/session identity or precomputed call hash
	// used inside that namespace.
	openAIResponsesSessionWindowPrefix = "openai_responses_session_window:" // response-window owner key namespace
	liveCallPrefix                     = "live:call:"                       // saved Live-call hash namespace

	// Video snapshot and claim prefixes keep creation-time billing evidence
	// separate from a temporary claimant marker, which guards polling while
	// persistent settlement remains independently deduplicated.
	grokVideoPendingBillingPrefix = "grok_video_pending:" // video billing snapshot key namespace
	grokVideoBilledPrefix         = "grok_video_billed:"  // video temporary claimant key namespace

	// Reasoning cache keys retain recovered text by item identity; nonpositive
	// caller TTL values select the seven-day default used by session history
	// recovery.
	reasoningContentPrefix = "reasoning_content:" // reasoning item text key namespace

	// reasoningContentDefaultTTL allows cached reasoning to support session
	// recovery for seven days.
	// Nonpositive caller TTLs select this default.
	reasoningContentDefaultTTL = 7 * 24 * time.Hour

	// Transcript blocks separate exact content keys from a coarse scope marker,
	// and command width bounds each write transaction or lookup batch over those
	// exact keys.
	cyberSessionBlockPrefix         = "cyber_session_block:" // exact transcript block key namespace
	cyberSessionScopePrefix         = "cyber_session_scope:" // coarse transcript scope key namespace
	cyberSessionRedisCommandMaxKeys = 128                    // maximum exact keys per Redis batch
)

var (
	// These response-window scripts replace an owner and return its predecessor,
	// or compare the stored owner before changing expiry or removing that same
	// session window.
	claimOpenAIResponsesSessionWindowScript = redis.NewScript(`
local previous = redis.call('GET', KEYS[1])
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return previous
`)

	compareAndRefreshOpenAIResponsesSessionWindowScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == false or current ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

	compareAndDeleteOpenAIResponsesSessionWindowScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == false or current ~= ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[1])
return 1
`)

	// These assertions keep the combined adapter discoverable through
	// response-window, video, reasoning, transcript-block and Live-store
	// interfaces consumed by the gateway's independent session components.
	_ session.OpenAIWSSessionPreemptionCache = (*gatewayCache)(nil)
	_ session.GrokVideoBillingCache          = (*gatewayCache)(nil)
	_ session.ReasoningContentCache          = (*gatewayCache)(nil)
	_ session.CyberSessionBlockStore         = (*gatewayCache)(nil)
	_ session.LiveCallStore                  = (*gatewayCache)(nil)

	// claimLiveControllerScript compares controller state before claiming
	// observer or proxy ownership; callers use the defined labels, and the script
	// records the supplied owner with that controller.
	claimLiveControllerScript = redis.NewScript(`
	local key = KEYS[1]
	local target = ARGV[1]
	local owner = ARGV[2]
	local current = redis.call('HGET', key, 'controller')
	if current == false or current == 'closed' then
		return 0
	end
	if target == 'observer' and current ~= 'pending' then
		return 0
	end
	if target == 'proxy' and current ~= 'pending' and current ~= 'observer' and
		(current ~= 'proxy' or redis.call('HGET', key, 'controller_owner') ~= owner) then
		return 0
	end
	redis.call('HSET', key, 'controller', target, 'controller_owner', owner)
	return 1
`)

	// markLiveCallClosedScript changes an existing nonclosed call to closed and
	// resets retention; its first acknowledged success permits the runtime's
	// separate cleanup and recording attempts.
	markLiveCallClosedScript = redis.NewScript(`
	local key = KEYS[1]
	if redis.call('EXISTS', key) == 0 then
		return 0
	end
	if redis.call('HGET', key, 'controller') == 'closed' then
		return 0
	end
	redis.call('HSET', key, 'controller', 'closed', 'controller_owner', '')
	redis.call('EXPIRE', key, ARGV[1])
	return 1
`)

	// releaseLiveControllerScript returns proxy control to pending only for the
	// matching owner, preventing a stale proxy cleanup from relinquishing another
	// owner's current claim.
	releaseLiveControllerScript = redis.NewScript(`
	local key = KEYS[1]
	if redis.call('HGET', key, 'controller') ~= 'proxy' or
		redis.call('HGET', key, 'controller_owner') ~= ARGV[1] then
		return 0
	end
	redis.call('HSET', key, 'controller', 'pending', 'controller_owner', '')
	return 1
`)
)

// gatewayCache combines scheduler sticky bindings with gateway protocol and
// Live state on Redis.
type gatewayCache struct {
	*schedulerredis.StickyCache               // inherited scheduler sticky binding operations
	rdb                         *redis.Client // shared application Redis command client
}

// NewGatewayCache binds the supplied client to sticky and protocol session
// operations. Callers provide a usable client for direct transcript and Live
// methods; guarded response, video, and reasoning methods report unavailable
// when the client is nil.
func NewGatewayCache(rdb *redis.Client) session.GatewayCache {
	return &gatewayCache{rdb: rdb, StickyCache: schedulerredis.NewStickyCache(rdb)}
}

// buildOpenAIResponsesSessionWindowKey combines the raw session hash with its
// numeric group namespace.
func buildOpenAIResponsesSessionWindowKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", openAIResponsesSessionWindowPrefix, groupID, sessionHash)
}

// ClaimOpenAIResponsesSessionWindow replaces the opaque owner and returns its
// predecessor from one atomic script. It rejects blank hashes, empty owner
// bytes, unavailable cache and nonpositive TTL; the key retains the supplied
// hash spelling.
//
// The previous value can arrive as Redis nil, string, or bytes, which are
// normalized into an independent byte result before callers decode ownership.
// Unknown result types are errors. TTL is truncated to whole milliseconds, so
// a positive duration below one millisecond gives Redis an invalid zero PX
// lifetime; callers choose at least one millisecond when establishing a
// window.
func (c *gatewayCache) ClaimOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, owner []byte, ttl time.Duration) ([]byte, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("gateway cache unavailable")
	}
	if len(owner) == 0 || strings.TrimSpace(sessionHash) == "" || ttl <= 0 {
		return nil, errors.New("invalid OpenAI Responses session-window claim")
	}
	// Replace ownership and capture the predecessor in the same script, so
	// competing preemption callers do not read another owner between separate GET
	// and SET commands.
	result, err := claimOpenAIResponsesSessionWindowScript.Run(
		ctx,
		c.rdb,
		[]string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)},
		owner,
		// Redis PX requires integral positive milliseconds for a new window.
		ttl.Milliseconds(),
	).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch value := result.(type) {
	case nil:
		return nil, nil
	case string:
		return []byte(value), nil
	case []byte:
		return append([]byte(nil), value...), nil
	default:
		return nil, fmt.Errorf("unexpected OpenAI Responses session-window claim result %T", result)
	}
}

// CompareAndRefreshOpenAIResponsesSessionWindow refreshes expiry only when the
// stored opaque owner equals expected. Inputs require nonblank hash, nonempty
// owner and positive TTL; whole-millisecond truncation means a submillisecond
// duration expires the matching key immediately.
func (c *gatewayCache) CompareAndRefreshOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("gateway cache unavailable")
	}
	if len(expected) == 0 || strings.TrimSpace(sessionHash) == "" || ttl <= 0 {
		return false, errors.New("invalid OpenAI Responses session-window refresh")
	}
	// Compare the expected owner inside Redis before changing expiry; a stale
	// controller refresh must not extend a window already replaced by another
	// opaque owner.
	n, err := compareAndRefreshOpenAIResponsesSessionWindowScript.Run(
		ctx,
		c.rdb,
		[]string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)},
		expected,
		// Zero whole milliseconds expires the matched owner window immediately.
		ttl.Milliseconds(),
	).Int()
	return n == 1, err
}

// CompareAndDeleteOpenAIResponsesSessionWindow removes the raw group/session
// key only when its stored owner equals expected. Empty owner bytes or blank
// hash are rejected; a missing or replaced owner returns false without
// deleting another claim.
func (c *gatewayCache) CompareAndDeleteOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("gateway cache unavailable")
	}
	if len(expected) == 0 || strings.TrimSpace(sessionHash) == "" {
		return false, errors.New("invalid OpenAI Responses session-window delete")
	}
	// Compare ownership before deletion inside the same script, keeping a stale
	// cleanup call from deleting the session window now held by a replacement
	// owner.
	n, err := compareAndDeleteOpenAIResponsesSessionWindowScript.Run(
		ctx,
		c.rdb,
		[]string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)},
		expected,
	).Int()
	return n == 1, err
}

// SetGrokVideoPendingBilling stores nonempty serialized creation evidence
// under a trimmed video key. Blank keys or payloads are rejected, and
// nonpositive TTL selects twenty-four hours for later status polling.
func (c *gatewayCache) SetGrokVideoPendingBilling(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" || len(payload) == 0 {
		return errors.New("invalid grok video pending billing payload")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return c.rdb.Set(ctx, grokVideoPendingBillingPrefix+key, payload, ttl).Err()
}

// GetGrokVideoPendingBilling returns bytes from the trimmed video snapshot key
// or nil on a Redis miss. Unavailable cache and blank keys are errors, and
// other command errors remain available to the caller.
func (c *gatewayCache) GetGrokVideoPendingBilling(ctx context.Context, key string) ([]byte, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("invalid grok video pending billing key")
	}
	payload, err := c.rdb.Get(ctx, grokVideoPendingBillingPrefix+key).Bytes()
	// Return a missing creation snapshot as nil for the polling caller to
	// interpret; other Redis failures remain errors rather than being treated as
	// absent billing evidence.
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return payload, err
}

// ClaimGrokVideoBilled uses SET NX to insert a claim under a trimmed video key
// when absent. Nonpositive TTL selects forty-eight hours; false denotes an
// existing claim, while expiry or release permits another claimant later.
func (c *gatewayCache) ClaimGrokVideoBilled(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, errors.New("invalid grok video billed key")
	}
	if ttl <= 0 {
		ttl = 48 * time.Hour
	}
	// Use an expiring absence claim to choose the current poller; this temporary
	// key can be reclaimed later and does not replace persistent settlement
	// idempotency.
	return c.rdb.SetNX(ctx, grokVideoBilledPrefix+key, "1", ttl).Result()
}

// ReleaseGrokVideoBilled deletes the trimmed video claim key after a failed
// settlement attempt. It compares no claimant token, so callers keep
// failed-claim release associated with their own polling operation and retain
// persistent billing deduplication separately.
func (c *gatewayCache) ReleaseGrokVideoBilled(ctx context.Context, key string) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("invalid grok video billed key")
	}
	// Remove the failed billing marker by key alone so a later poll can retry;
	// this operation has no stored claimant token to distinguish another owner.
	return c.rdb.Del(ctx, grokVideoBilledPrefix+key).Err()
}

// SetReasoningContent stores reasoning text under its trimmed item identifier.
// Available cache ignores blank IDs or empty text; nonpositive TTL selects
// seven days, while unavailable cache returns an error before that empty-input
// handling.
func (c *gatewayCache) SetReasoningContent(ctx context.Context, itemID string, content string, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" || content == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = reasoningContentDefaultTTL
	}
	return c.rdb.Set(ctx, reasoningContentPrefix+itemID, content, ttl).Err()
}

// GetReasoningContent returns cached text for a trimmed item identifier or
// ErrReasoningContentNotFound for blank IDs and Redis misses. Unavailable
// cache and other command failures remain distinct errors for session
// recovery.
func (c *gatewayCache) GetReasoningContent(ctx context.Context, itemID string) (string, error) {
	if c == nil || c.rdb == nil {
		return "", errors.New("gateway cache unavailable")
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return "", session.ErrReasoningContentNotFound
	}
	val, err := c.rdb.Get(ctx, reasoningContentPrefix+itemID).Result()
	if err != nil {
		// Translate a Redis miss into the recovery sentinel so reasoning history can
		// skip absent items while keeping actual command failures distinguishable
		// for its caller.
		if errors.Is(err, redis.Nil) {
			return "", session.ErrReasoningContentNotFound
		}
		return "", err
	}
	return val, nil
}

// SetCyberSessionBlocked writes nonempty exact keys in bounded transactions
// before publishing a nonempty coarse scope. It requires a usable Redis
// adapter; an empty input list returns successfully without publishing scope.
//
// Each batch completes before another begins, and an error stops later work
// while retaining any earlier writes. The new scope marker is attempted after
// all accepted exact keys succeed; a nonempty list containing only empty
// entries can still publish it. Existing scope markers are not cleared on
// failure, and these separate transactions supply no rollback across the
// complete input list.
func (c *gatewayCache) SetCyberSessionBlocked(ctx context.Context, scopeKey string, keys []string, ttl time.Duration) error {
	if len(keys) == 0 {
		return nil
	}
	exactKeys := make([]string, 0, cyberSessionRedisCommandMaxKeys)
	flush := func() error {
		if len(exactKeys) == 0 {
			return nil
		}
		// Commit a bounded exact-key batch before publishing coarse scope. Later
		// batch failure leaves this successful batch stored, with no cross-batch
		// rollback or fresh scope publication.
		pipe := c.rdb.TxPipeline()
		for _, key := range exactKeys {
			pipe.Set(ctx, cyberSessionBlockPrefix+key, "1", ttl)
		}
		_, err := pipe.Exec(ctx)
		exactKeys = exactKeys[:0]
		return err
	}
	for _, key := range keys {
		if key != "" {
			exactKeys = append(exactKeys, key)
			if len(exactKeys) == cyberSessionRedisCommandMaxKeys {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	// Publish coarse activity after every accepted exact batch completes, because
	// session lookup consults that marker before deriving and checking its
	// transcript-specific block keys.
	if scopeKey != "" {
		return c.rdb.Set(ctx, cyberSessionScopePrefix+scopeKey, "1", ttl).Err()
	}
	return nil
}

// IsCyberSessionScopeActive reports whether the exact supplied coarse-scope
// key exists in Redis. It requires a usable adapter and returns command errors
// directly; stored value content does not affect the existence result.
func (c *gatewayCache) IsCyberSessionScopeActive(ctx context.Context, scopeKey string) (bool, error) {
	n, err := c.rdb.Exists(ctx, cyberSessionScopePrefix+scopeKey).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FindCyberSessionBlocked reads bounded key batches in caller order and
// returns the first entry with a nonnil stored value. Empty input or no match
// returns an empty string; keys are used as supplied and read errors
// propagate.
func (c *gatewayCache) FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error) {
	if len(keys) == 0 {
		return "", nil
	}
	// Bound each Redis lookup while retaining caller ordering; stop at the first
	// matched batch so later candidate hashes need no command once a block is
	// found.
	for start := 0; start < len(keys); start += cyberSessionRedisCommandMaxKeys {
		end := min(start+cyberSessionRedisCommandMaxKeys, len(keys))
		redisKeys := make([]string, end-start)
		for i, key := range keys[start:end] {
			redisKeys[i] = cyberSessionBlockPrefix + key
		}
		values, err := c.rdb.MGet(ctx, redisKeys...).Result()
		if err != nil {
			return "", err
		}
		for i, value := range values {
			if value != nil {
				return keys[start+i], nil
			}
		}
	}
	return "", nil
}

// liveCallKey prefixes the supplied call hash for saved Live state lookup.
func liveCallKey(callHash string) string {
	return liveCallPrefix + callHash
}

// HashLiveCallID returns lowercase hexadecimal SHA-256 bytes for the supplied
// call identifier.
func HashLiveCallID(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(sum[:])
}

// SaveLiveCall stores the adapter's call-field subset with its supplied
// physical TTL. The record needs nonempty CallHash and CallID, and callers
// provide a usable Redis client; hash consistency and other field validity
// remain caller preparation.
//
// Model mapping is encoded as JSON before HSET and EXPIRE enter one
// transaction pipeline. Controller fields are written from the supplied
// record, and times lose submillisecond precision through UnixMilli.
// ActorUserID and TeamID are omitted. Transaction execution errors are
// returned without an application rollback, and repeated saves can overwrite
// controller fields; logical ExpiresAt is not the datastore retention
// argument.
func (c *gatewayCache) SaveLiveCall(ctx context.Context, record *session.LiveCallRecord, ttl time.Duration) error {
	if record == nil || record.CallHash == "" || record.CallID == "" {
		return fmt.Errorf("invalid live call record")
	}
	modelMapping, err := json.Marshal(record.APIKeyModelMapping)
	if err != nil {
		return fmt.Errorf("marshal live API key model mapping: %w", err)
	}
	// Serialize CreatedAt and ExpiresAt at whole-millisecond precision in this
	// field subset; their logical call lifetime remains distinct from the
	// physical expiry applied to the hash.
	values := map[string]any{
		"call_id":          record.CallID,
		"provider_id":      record.ProviderID,
		"api_key_id":       record.APIKeyID,
		"user_id":          record.UserID,
		"group_id":         record.GroupID,
		"subscription_id":  record.SubscriptionID,
		"lease_id":         record.LeaseID,
		"model":            record.Model,
		"requested_model":  record.RequestedModel,
		"upstream_model":   record.UpstreamModel,
		"mapping_chain":    record.ModelMappingChain,
		"api_key_mapping":  string(modelMapping),
		"created_at":       record.CreatedAt.UnixMilli(),
		"expires_at":       record.ExpiresAt.UnixMilli(),
		"controller":       record.Controller,
		"controller_owner": record.ControllerOwner,
		"user_agent":       record.UserAgent,
		"ip_address":       record.IPAddress,
		"inbound_endpoint": record.InboundEndpoint,
		"attestation":      record.AttestationCiphertext,
	}
	key := liveCallKey(record.CallHash)
	// Write the hash fields and physical expiry in one transaction pipeline
	// before another controller observes this record; command errors return
	// directly and no compensating delete runs here.
	pipe := c.rdb.TxPipeline()
	pipe.HSet(ctx, key, values)
	pipe.Expire(ctx, key, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// GetLiveCall decodes the persisted call subset or returns ErrLiveCallNotFound
// when its hash is absent. It requires a usable Redis client; malformed
// model-map JSON is an error, while numeric parsing errors are discarded.
//
// The record uses the supplied hash and decoded IDs, controller fields,
// metadata and Unix-millisecond times. Missing or invalid decimal fields use
// ParseInt's result, including zero for syntax errors and saturated values for
// range errors. APIKeyModelMapping is freshly decoded, ActorUserID and TeamID
// remain zero, and this read does not enforce logical expiry or validate the
// reconstructed identity.
func (c *gatewayCache) GetLiveCall(ctx context.Context, callHash string) (*session.LiveCallRecord, error) {
	values, err := c.rdb.HGetAll(ctx, liveCallKey(callHash)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, session.ErrLiveCallNotFound
	}
	// Use ParseInt's returned value even when it reports an error; syntax
	// failures become zero and range failures retain saturation, so callers
	// validate identity separately.
	parseInt := func(field string) int64 {
		value, _ := strconv.ParseInt(values[field], 10, 64)
		return value
	}
	// Decode both stored epoch values using their millisecond unit.
	createdAt := time.UnixMilli(parseInt("created_at"))
	expiresAt := time.UnixMilli(parseInt("expires_at"))
	modelMapping := make(map[string]string)
	if rawMapping := values["api_key_mapping"]; rawMapping != "" {
		if err := json.Unmarshal([]byte(rawMapping), &modelMapping); err != nil {
			return nil, fmt.Errorf("decode live API key model mapping: %w", err)
		}
	}
	return &session.LiveCallRecord{
		CallID:                values["call_id"],
		CallHash:              callHash,
		ProviderID:            parseInt("provider_id"),
		APIKeyID:              parseInt("api_key_id"),
		UserID:                parseInt("user_id"),
		GroupID:               parseInt("group_id"),
		SubscriptionID:        parseInt("subscription_id"),
		LeaseID:               values["lease_id"],
		Model:                 values["model"],
		RequestedModel:        values["requested_model"],
		UpstreamModel:         values["upstream_model"],
		ModelMappingChain:     values["mapping_chain"],
		APIKeyModelMapping:    modelMapping,
		CreatedAt:             createdAt,
		ExpiresAt:             expiresAt,
		Controller:            values["controller"],
		ControllerOwner:       values["controller_owner"],
		UserAgent:             values["user_agent"],
		IPAddress:             values["ip_address"],
		InboundEndpoint:       values["inbound_endpoint"],
		AttestationCiphertext: values["attestation"],
	}, nil
}

// ClaimLiveController atomically applies the requested observer or proxy
// transition with its owner. Callers use defined controller labels; missing or
// closed calls refuse claims, and a proxy can reassert only its own current
// proxy ownership.
func (c *gatewayCache) ClaimLiveController(ctx context.Context, callHash, controller, owner string) (bool, error) {
	// Compare the stored controller before writing this owner, allowing observer
	// takeover from pending and proxy takeover from pending or observer while
	// refusing another active proxy owner.
	result, err := claimLiveControllerScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, controller, owner).Int()
	return result == 1, err
}

// GetLiveController reads the stored controller label for this exact call
// hash. Missing controller data becomes ErrLiveCallNotFound; other Redis
// errors return unchanged and callers decide whether to reconnect or finalize.
func (c *gatewayCache) GetLiveController(ctx context.Context, callHash string) (string, error) {
	value, err := c.rdb.HGet(ctx, liveCallKey(callHash), "controller").Result()
	if err == redis.Nil {
		return "", session.ErrLiveCallNotFound
	}
	return value, err
}

// ReleaseLiveController returns matching proxy ownership to pending for later
// observation. A missing call, another controller label, or another owner
// returns false; callers keep the supplied owner associated with the proxy
// they acquired.
func (c *gatewayCache) ReleaseLiveController(ctx context.Context, callHash, owner string) (bool, error) {
	// Release only matching stored proxy ownership before an observer can reclaim
	// pending control; late cleanup from another owner leaves the current
	// controller pair intact.
	result, err := releaseLiveControllerScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, owner).Int()
	return result == 1, err
}

// MarkLiveCallClosed marks an existing nonclosed call and clears its owner
// atomically, returning whether it won closure. TTL is truncated to whole
// seconds; values below one second can immediately remove the record and its
// deduplication marker.
func (c *gatewayCache) MarkLiveCallClosed(ctx context.Context, callHash string, ttl time.Duration) (bool, error) {
	// Mark closure and clear the controller owner together before selecting the
	// winning finalizer; the runtime performs lease release and usage recording
	// only after this acknowledged result.
	// Redis EXPIRE receives whole seconds, so tiny retention removes the marker.
	result, err := markLiveCallClosedScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, int64(ttl.Seconds())).Int()
	return result == 1, err
}

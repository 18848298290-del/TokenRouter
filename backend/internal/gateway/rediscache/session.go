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

const (
	openAIResponsesSessionWindowPrefix = "openai_responses_session_window:"
	liveCallPrefix                     = "live:call:"

	grokVideoPendingBillingPrefix = "grok_video_pending:"
	grokVideoBilledPrefix         = "grok_video_billed:"

	reasoningContentPrefix = "reasoning_content:"

	// reasoningContentDefaultTTL allows cached reasoning to support session recovery for seven days.
	// Nonpositive caller TTLs select this default.
	reasoningContentDefaultTTL = 7 * 24 * time.Hour

	cyberSessionBlockPrefix         = "cyber_session_block:"
	cyberSessionScopePrefix         = "cyber_session_scope:"
	cyberSessionRedisCommandMaxKeys = 128
)

var (
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

	_ session.OpenAIWSSessionPreemptionCache = (*gatewayCache)(nil)
	_ session.GrokVideoBillingCache          = (*gatewayCache)(nil)
	_ session.ReasoningContentCache          = (*gatewayCache)(nil)
	_ session.CyberSessionBlockStore         = (*gatewayCache)(nil)
	_ session.LiveCallStore                  = (*gatewayCache)(nil)

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

type gatewayCache struct {
	*schedulerredis.StickyCache
	rdb *redis.Client
}

func NewGatewayCache(rdb *redis.Client) session.GatewayCache {
	return &gatewayCache{rdb: rdb, StickyCache: schedulerredis.NewStickyCache(rdb)}
}

func buildOpenAIResponsesSessionWindowKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", openAIResponsesSessionWindowPrefix, groupID, sessionHash)
}

func (c *gatewayCache) ClaimOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, owner []byte, ttl time.Duration) ([]byte, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("gateway cache unavailable")
	}
	if len(owner) == 0 || strings.TrimSpace(sessionHash) == "" || ttl <= 0 {
		return nil, errors.New("invalid OpenAI Responses session-window claim")
	}
	result, err := claimOpenAIResponsesSessionWindowScript.Run(
		ctx,
		c.rdb,
		[]string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)},
		owner,
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

func (c *gatewayCache) CompareAndRefreshOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("gateway cache unavailable")
	}
	if len(expected) == 0 || strings.TrimSpace(sessionHash) == "" || ttl <= 0 {
		return false, errors.New("invalid OpenAI Responses session-window refresh")
	}
	n, err := compareAndRefreshOpenAIResponsesSessionWindowScript.Run(
		ctx,
		c.rdb,
		[]string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)},
		expected,
		ttl.Milliseconds(),
	).Int()
	return n == 1, err
}

func (c *gatewayCache) CompareAndDeleteOpenAIResponsesSessionWindow(ctx context.Context, groupID int64, sessionHash string, expected []byte) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("gateway cache unavailable")
	}
	if len(expected) == 0 || strings.TrimSpace(sessionHash) == "" {
		return false, errors.New("invalid OpenAI Responses session-window delete")
	}
	n, err := compareAndDeleteOpenAIResponsesSessionWindowScript.Run(
		ctx,
		c.rdb,
		[]string{buildOpenAIResponsesSessionWindowKey(groupID, sessionHash)},
		expected,
	).Int()
	return n == 1, err
}

// SetGrokVideoPendingBilling stores the creation-time billing snapshot for later status polling.
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

// GetGrokVideoPendingBilling returns the video billing snapshot, or nil on a Redis miss.
func (c *gatewayCache) GetGrokVideoPendingBilling(ctx context.Context, key string) ([]byte, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("invalid grok video pending billing key")
	}
	payload, err := c.rdb.Get(ctx, grokVideoPendingBillingPrefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return payload, err
}

// ClaimGrokVideoBilled uses SET NX to select one billing claimant while the claim key exists.
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
	return c.rdb.SetNX(ctx, grokVideoBilledPrefix+key, "1", ttl).Result()
}

// ReleaseGrokVideoBilled releases a failed settlement claim so later polls can retry.
func (c *gatewayCache) ReleaseGrokVideoBilled(ctx context.Context, key string) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("invalid grok video billed key")
	}
	return c.rdb.Del(ctx, grokVideoBilledPrefix+key).Err()
}

// SetReasoningContent stores text by item ID; empty IDs or content are ignored when the cache is available.
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

// GetReasoningContent returns cached reasoning text; empty IDs and misses return
// session.ErrReasoningContentNotFound.
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
		if errors.Is(err, redis.Nil) {
			return "", session.ErrReasoningContentNotFound
		}
		return "", err
	}
	return val, nil
}

// SetCyberSessionBlocked writes exact blocks in bounded transactions. The
// coarse scope is activated only after all exact blocks have been stored.
func (c *gatewayCache) SetCyberSessionBlocked(ctx context.Context, scopeKey string, keys []string, ttl time.Duration) error {
	if len(keys) == 0 {
		return nil
	}
	exactKeys := make([]string, 0, cyberSessionRedisCommandMaxKeys)
	flush := func() error {
		if len(exactKeys) == 0 {
			return nil
		}
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
	if scopeKey != "" {
		return c.rdb.Set(ctx, cyberSessionScopePrefix+scopeKey, "1", ttl).Err()
	}
	return nil
}

func (c *gatewayCache) IsCyberSessionScopeActive(ctx context.Context, scopeKey string) (bool, error) {
	n, err := c.rdb.Exists(ctx, cyberSessionScopePrefix+scopeKey).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FindCyberSessionBlocked checks bounded batches in caller order and stops at
// the first blocked key.
func (c *gatewayCache) FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error) {
	if len(keys) == 0 {
		return "", nil
	}
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

func liveCallKey(callHash string) string {
	return liveCallPrefix + callHash
}

// HashLiveCallID hashes the call ID for use in a Redis key.
func HashLiveCallID(callID string) string {
	sum := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(sum[:])
}

func (c *gatewayCache) SaveLiveCall(ctx context.Context, record *session.LiveCallRecord, ttl time.Duration) error {
	if record == nil || record.CallHash == "" || record.CallID == "" {
		return fmt.Errorf("invalid live call record")
	}
	modelMapping, err := json.Marshal(record.APIKeyModelMapping)
	if err != nil {
		return fmt.Errorf("marshal live API key model mapping: %w", err)
	}
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
	pipe := c.rdb.TxPipeline()
	pipe.HSet(ctx, key, values)
	pipe.Expire(ctx, key, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *gatewayCache) GetLiveCall(ctx context.Context, callHash string) (*session.LiveCallRecord, error) {
	values, err := c.rdb.HGetAll(ctx, liveCallKey(callHash)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, session.ErrLiveCallNotFound
	}
	parseInt := func(field string) int64 {
		value, _ := strconv.ParseInt(values[field], 10, 64)
		return value
	}
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

func (c *gatewayCache) ClaimLiveController(ctx context.Context, callHash, controller, owner string) (bool, error) {
	result, err := claimLiveControllerScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, controller, owner).Int()
	return result == 1, err
}

func (c *gatewayCache) GetLiveController(ctx context.Context, callHash string) (string, error) {
	value, err := c.rdb.HGet(ctx, liveCallKey(callHash), "controller").Result()
	if err == redis.Nil {
		return "", session.ErrLiveCallNotFound
	}
	return value, err
}

func (c *gatewayCache) ReleaseLiveController(ctx context.Context, callHash, owner string) (bool, error) {
	result, err := releaseLiveControllerScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, owner).Int()
	return result == 1, err
}

func (c *gatewayCache) MarkLiveCallClosed(ctx context.Context, callHash string, ttl time.Duration) (bool, error) {
	result, err := markLiveCallClosedScript.Run(ctx, c.rdb, []string{liveCallKey(callHash)}, int64(ttl.Seconds())).Int()
	return result == 1, err
}

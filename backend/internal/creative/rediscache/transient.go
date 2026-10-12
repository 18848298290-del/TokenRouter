package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// Redis prefixes for expiring Creative Studio payloads and media.
// Prompts, source images, masks, and output bytes become unavailable when their keys expire.
const (
	defaultCreativePayloadKeyPrefix = "creative:payload:"
	defaultCreativeInputKeyPrefix   = "creative:input:"
	defaultCreativeMaskKeyPrefix    = "creative:mask:"
	defaultCreativeOutputKeyPrefix  = "creative:output:"
)

var _ creative.CreativeTransientStore = (*creativeTransientStore)(nil)

// creativeTransientStore stores temporary Creative Studio data in Redis.
// Redis write failures return errors so task creation can reject unsaved inputs.
type creativeTransientStore struct {
	rdb           *redis.Client
	payloadPrefix string
	inputPrefix   string
	maskPrefix    string
	outputPrefix  string
	defaultTTL    time.Duration
}

type TransientOptions struct {
	TransientTTLSeconds int
}

// NewCreativeTransientStore creates storage with a 30-minute TTL unless a positive TTL is configured.
func NewCreativeTransientStore(rdb *redis.Client, cfg *TransientOptions) creative.CreativeTransientStore {
	store := &creativeTransientStore{
		rdb:           rdb,
		payloadPrefix: defaultCreativePayloadKeyPrefix,
		inputPrefix:   defaultCreativeInputKeyPrefix,
		maskPrefix:    defaultCreativeMaskKeyPrefix,
		outputPrefix:  defaultCreativeOutputKeyPrefix,
		defaultTTL:    30 * time.Minute,
	}
	if cfg != nil && cfg.TransientTTLSeconds > 0 {
		store.defaultTTL = time.Duration(cfg.TransientTTLSeconds) * time.Second
	}
	return store
}

func (s *creativeTransientStore) SavePayload(ctx context.Context, runID string, payload *creative.CreativeRunPayload) error {
	if s.rdb == nil {
		return fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	key := s.payloadPrefix + runID
	// Replace the payload and reset its TTL in one Redis transaction.
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, key)
	pipe.Set(ctx, key, body, s.defaultTTL)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return nil
}

func (s *creativeTransientStore) LoadPayload(ctx context.Context, runID string) (*creative.CreativeRunPayload, error) {
	if s.rdb == nil {
		return nil, fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	body, err := s.rdb.Get(ctx, s.payloadPrefix+runID).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("%w: %w", creative.ErrCreativeTransientNotFound, creative.ErrCreativeTransientFailed)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	payload := &creative.CreativeRunPayload{}
	if err := json.Unmarshal(body, payload); err != nil {
		return nil, fmt.Errorf("%w: %v", creative.ErrCreativeTransientCorrupt, err)
	}
	return payload, nil
}

func (s *creativeTransientStore) SaveInput(ctx context.Context, runID string, idx int, data []byte) error {
	if s.rdb == nil {
		return fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	if len(data) == 0 {
		return errors.New("creative input is empty")
	}
	if err := s.rdb.Set(ctx, s.inputKey(runID, idx), data, s.defaultTTL).Err(); err != nil {
		return fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return nil
}

func (s *creativeTransientStore) LoadInputs(ctx context.Context, runID string, count int) ([][]byte, error) {
	if s.rdb == nil {
		return nil, fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	if count <= 0 {
		return nil, nil
	}
	keys := make([]string, 0, count)
	for idx := range count {
		keys = append(keys, s.inputKey(runID, idx))
	}
	values, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	out := make([][]byte, 0, count)
	for idx, value := range values {
		raw, ok := value.(string)
		if !ok || raw == "" {
			return nil, fmt.Errorf("%w: %w: input %d for run %s", creative.ErrCreativeTransientNotFound, creative.ErrCreativeTransientFailed, idx, runID)
		}
		out = append(out, []byte(raw))
	}
	return out, nil
}

func (s *creativeTransientStore) SaveMask(ctx context.Context, runID string, data []byte) error {
	if s.rdb == nil {
		return fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	if len(data) == 0 {
		return errors.New("creative mask is empty")
	}
	if err := s.rdb.Set(ctx, s.maskPrefix+runID, data, s.defaultTTL).Err(); err != nil {
		return fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return nil
}

func (s *creativeTransientStore) LoadMask(ctx context.Context, runID string) ([]byte, error) {
	if s.rdb == nil {
		return nil, fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	data, err := s.rdb.Get(ctx, s.maskPrefix+runID).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("%w: %w", creative.ErrCreativeTransientNotFound, creative.ErrCreativeTransientFailed)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return data, nil
}

func (s *creativeTransientStore) SaveOutput(ctx context.Context, runID string, index int, data []byte, ttl time.Duration) error {
	if s.rdb == nil {
		return fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	if len(data) == 0 {
		return errors.New("creative output is empty")
	}
	if ttl <= 0 {
		ttl = s.defaultTTL
	}
	if err := s.rdb.Set(ctx, s.outputKey(runID, index), data, ttl).Err(); err != nil {
		return fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return nil
}

func (s *creativeTransientStore) LoadOutput(ctx context.Context, runID string, index int) ([]byte, error) {
	if s.rdb == nil {
		return nil, fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	data, err := s.rdb.Get(ctx, s.outputKey(runID, index)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("%w: %w", creative.ErrCreativeTransientNotFound, creative.ErrCreativeTransientFailed)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: %w", creative.ErrCreativeTransientCorrupt, creative.ErrCreativeTransientFailed)
	}
	return data, nil
}

func (s *creativeTransientStore) DeleteOutput(ctx context.Context, runID string, index int) error {
	if s.rdb == nil {
		return fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	// DEL succeeds even when the key has already expired or been deleted.
	if err := s.rdb.Del(ctx, s.outputKey(runID, index)).Err(); err != nil {
		return fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return nil
}

// DeleteRunTransient deletes task keys; nonpositive input/output counts use wildcard KEYS lookups.
func (s *creativeTransientStore) DeleteRunTransient(ctx context.Context, runID string, inputCount, outputCount int) error {
	if s.rdb == nil {
		return fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	keys := []string{
		s.payloadPrefix + runID,
		s.maskPrefix + runID,
	}
	for idx := range inputCount {
		keys = append(keys, s.inputKey(runID, idx))
	}
	for index := range outputCount {
		keys = append(keys, s.outputKey(runID, index))
	}
	// Discover keys when counts are unknown; failed lookups are skipped and TTL expiry remains the backstop.
	if inputCount <= 0 {
		if scanned, err := s.rdb.Keys(ctx, s.inputPrefix+runID+":*").Result(); err == nil {
			keys = append(keys, scanned...)
		}
	}
	if outputCount <= 0 {
		if scanned, err := s.rdb.Keys(ctx, s.outputPrefix+runID+":*").Result(); err == nil {
			keys = append(keys, scanned...)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return nil
}

func (s *creativeTransientStore) inputKey(runID string, idx int) string {
	return fmt.Sprintf("%s%s:%d", s.inputPrefix, runID, idx)
}

func (s *creativeTransientStore) outputKey(runID string, index int) string {
	return fmt.Sprintf("%s%s:%d", s.outputPrefix, runID, index)
}

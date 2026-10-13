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

// Creative plaintext prompts and media occupy expiring Redis keys separately
// from persistent task hashes and metadata. Payload JSON carries source counts
// and mask presence, while image bytes occupy indexed input/output keys or the
// mask key. Loading these resources distinguishes confirmed absence, malformed
// data, and Redis availability errors for worker and result recovery.
//
// Each write replaces one temporary value with its configured lifetime, and
// output writes can choose a positive per-call TTL. Payload replacement uses a
// Redis transaction for its delete and set. Run cleanup deletes explicitly
// indexed keys when counts are supplied and discovers keys when counts are
// unknown; discovery errors are skipped, leaving unreached keys to their
// existing TTL rather than blocking deletion of known payload and mask keys.

// Redis prefixes for expiring Creative Studio payloads and media.
// Prompts, source images, masks, and output bytes become unavailable when their
// keys expire.
const (
	defaultCreativePayloadKeyPrefix = "creative:payload:"
	defaultCreativeInputKeyPrefix   = "creative:input:"
	defaultCreativeMaskKeyPrefix    = "creative:mask:"
	defaultCreativeOutputKeyPrefix  = "creative:output:"
)

// The compile-time assertion checks that this Redis adapter implements every
// payload, media, and deletion operation required by creative transient
// storage.
var _ creative.CreativeTransientStore = (*creativeTransientStore)(nil)

// creativeTransientStore stores temporary Creative Studio data in Redis.
// Redis write failures return errors so task creation can reject unsaved inputs.
type creativeTransientStore struct {
	rdb           *redis.Client // borrowed transient Redis client
	payloadPrefix string        // plaintext payload JSON key prefix
	inputPrefix   string        // indexed input image key prefix
	maskPrefix    string        // input mask image key prefix
	outputPrefix  string        // indexed output image key prefix
	defaultTTL    time.Duration // default temporary value lifetime
}

// TransientOptions supplies the temporary payload and image retention duration
// used at construction.
type TransientOptions struct {
	TransientTTLSeconds int // seconds
}

// NewCreativeTransientStore retains the Redis client with a thirty-minute
// default lifetime. Positive configured seconds replace that default, and
// callers own the client lifetime shared by payload and byte operations.
func NewCreativeTransientStore(rdb *redis.Client, cfg *TransientOptions) creative.CreativeTransientStore {
	store := &creativeTransientStore{
		rdb:           rdb,
		payloadPrefix: defaultCreativePayloadKeyPrefix,
		inputPrefix:   defaultCreativeInputKeyPrefix,
		maskPrefix:    defaultCreativeMaskKeyPrefix,
		outputPrefix:  defaultCreativeOutputKeyPrefix,
		defaultTTL:    30 * time.Minute,
	}
	// Convert configured seconds once for payload, source, mask, and default
	// output expiration so these transient media families share a retention budget.
	if cfg != nil && cfg.TransientTTLSeconds > 0 {
		store.defaultTTL = time.Duration(cfg.TransientTTLSeconds) * time.Second
	}
	return store
}

// SavePayload serializes execution metadata and replaces its expiring Redis
// payload key. A missing client or transaction failure returns unavailable;
// source and mask bytes stay in their separate keys through payload JSON tags.
//
// JSON encoding finishes before Redis mutation begins. Delete and Set execute
// in one Redis transaction so readers see the replacement with its configured
// TTL rather than an intermediate missing key between those commands. Media
// writes belong to the caller provisioning sequence and can fail independently
// of this payload replacement.
func (s *creativeTransientStore) SavePayload(ctx context.Context, runID string, payload *creative.CreativeRunPayload) error {
	if s.rdb == nil {
		return fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	key := s.payloadPrefix + runID
	// Delete and set publish the replacement payload in one Redis transaction.
	// Successful execution installs its new lifetime together with the new JSON,
	// so readers cannot observe a standalone deletion between those two commands.
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, key)
	pipe.Set(ctx, key, body, s.defaultTTL)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return nil
}

// LoadPayload decodes stored JSON execution metadata into a newly allocated
// payload. Missing keys return not-found and legacy transient-failed
// sentinels, Redis failures return unavailable, and JSON decoding errors
// return corrupt.
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

// SaveInput stores nonempty source bytes at the supplied run/index key with
// the default TTL. Empty bytes fail locally, while absent Redis or failed
// writes return the unavailable sentinel.
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

// LoadInputs reads indexes zero through count minus one with a single MGet. A
// configured store returns nil for nonpositive counts; missing, non-string, or
// empty values fail the complete load with not-found and legacy
// transient-failed sentinels.
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

// SaveMask stores nonempty mask bytes under the run mask key with the default
// lifetime. Empty bytes fail locally, while missing clients and failed writes
// return unavailable errors.
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

// LoadMask returns the Redis mask bytes for a run. Missing keys report both
// not-found and legacy transient-failed sentinels, while other Redis errors
// report unavailable; an existing empty value is returned as supplied.
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

// SaveOutput replaces nonempty bytes at the run/output index using a positive
// supplied TTL. Nonpositive TTL selects the store default, empty bytes fail
// locally, and Redis errors return the unavailable sentinel.
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

// LoadOutput returns byte data from the indexed output key. Missing values
// report not-found and legacy transient-failed, existing empty bytes report
// corrupt and transient-failed, and other Redis errors report unavailable.
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

// DeleteOutput removes the indexed temporary output key and succeeds when it
// is already absent. Missing clients or Redis deletion failures return
// unavailable so acknowledgment cleanup can retry the operation.
func (s *creativeTransientStore) DeleteOutput(ctx context.Context, runID string, index int) error {
	if s.rdb == nil {
		return fmt.Errorf("%w: redis client is nil", creative.ErrCreativeTransientUnavailable)
	}
	// Client receipt can trigger deletion again after expiry or prior cleanup.
	// Redis DEL treats that absent key as success, allowing the same acknowledged
	// output to be cleaned repeatedly without changing its receipt metadata.
	if err := s.rdb.Del(ctx, s.outputKey(runID, index)).Err(); err != nil {
		return fmt.Errorf("%w: %v", creative.ErrCreativeTransientUnavailable, err)
	}
	return nil
}

// DeleteRunTransient removes known task payload, mask, input, and output keys.
// Positive counts enumerate exact indexes; nonpositive counts use wildcard
// KEYS discovery for their respective media families.
//
// Discovery errors are ignored while known keys still reach the deletion call.
// Consequently a successful result can leave undiscovered input or output keys
// to expire naturally. The subsequent DEL itself returns an unavailable error
// on failure, allowing terminal cleanup to retry rather than claiming those
// known keys were removed.
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
	// Discover keys when counts are unknown; failed lookups are skipped and
	// TTL expiry remains the backstop.
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

// inputKey formats a run identifier and decimal source index under the
// configured input prefix.
func (s *creativeTransientStore) inputKey(runID string, idx int) string {
	return fmt.Sprintf("%s%s:%d", s.inputPrefix, runID, idx)
}

// outputKey formats a run identifier and decimal delivery index under the
// configured output prefix.
func (s *creativeTransientStore) outputKey(runID string, index int) string {
	return fmt.Sprintf("%s%s:%d", s.outputPrefix, runID, index)
}

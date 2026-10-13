package rediscache

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// Snapshot tests work with provider Records while the Redis cache stores
// SnapshotProvider values through its codec. Thin adapters expose those
// records for assertions, and fixed historical JSON fixtures compare storage
// bytes after whitespace compaction.

// schedulerCache adapts core snapshot values to provider Records for storage
// test assertions.
type schedulerCache struct {
	// SnapshotCache is the test's shared storage subject; record adapters keep
	// its Redis client, token rules and publication behavior.
	*SnapshotCache // shared core snapshot cache
}

// SnapshotCoreCache exposes the embedded cache through the scheduler storage
// interface.
func (c *schedulerCache) SnapshotCoreCache() scheduler.SnapshotCache { return c.SnapshotCache }

// GetSnapshot unwraps bucket metadata values for provider-based test
// assertions and forwards cache lookup errors. Nil lists remain nil; an
// unexpected wrapped value aborts conversion and reports a miss with its
// error.
func (c *schedulerCache) GetSnapshot(ctx context.Context, bucket scheduler.SchedulerBucket) ([]*providercore.Record, bool, error) {
	values, hit, err := c.SnapshotCache.GetSnapshot(ctx, bucket)
	if err != nil {
		return nil, hit, err
	}
	if values == nil {
		return nil, hit, nil
	}
	out := make([]*providercore.Record, len(values))
	for i, v := range values {
		out[i], err = codec.RecordValue(v)
		if err != nil {
			return nil, false, err
		}
	}
	return out, hit, nil
}

// GetProvider retrieves and unwraps the full record used by cache tests. A
// missing core value remains nil, and returned records retain the decoded
// data owned by their codec wrapper.
func (c *schedulerCache) GetProvider(ctx context.Context, id int64) (*providercore.Record, error) {
	v, err := c.SnapshotCache.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	return codec.RecordValue(v)
}

// SetProvider wraps the supplied test record and delegates shared provider
// payload storage. Callers keep its referenced maps and pointers stable
// during encoding; nil records are ignored by the core cache.
func (c *schedulerCache) SetProvider(ctx context.Context, v *providercore.Record) error {
	return c.SnapshotCache.SetProvider(ctx, codec.WrapRecord(v))
}

// SetSnapshot wraps the supplied record slice and delegates token-checked
// bucket publication. The wrappers refer to the input elements, so their
// nested data stays stable until the delegated write finishes.
func (c *schedulerCache) SetSnapshot(ctx context.Context, bucket scheduler.SchedulerBucket, token scheduler.SchedulerBucketWriteToken, values []providercore.Record) error {
	return c.SnapshotCache.SetSnapshot(ctx, bucket, token, snapshotRecords(values))
}

// snapshotRecords wraps each supplied record element through the provider
// codec. Nil slices remain nil; returned wrappers keep pointers to the input
// elements and callers coordinate later mutation.
func snapshotRecords(values []providercore.Record) []scheduler.SnapshotProvider {
	if values == nil {
		return nil
	}
	out := make([]scheduler.SnapshotProvider, len(values))
	for i := range values {
		out[i] = codec.WrapRecord(&values[i])
	}
	return out
}

// marshalSchedulerCacheProvider encodes a test Record as full and filtered
// metadata payloads. It delegates to ProviderCodec, keeping fixture
// comparisons on the same storage encoding as production writes.
func marshalSchedulerCacheProvider(value providercore.Record) ([]byte, []byte, error) {
	return (codec.ProviderCodec{}).Encode(codec.WrapRecord(&value))
}

// historicalSchedulerPayload reads the fixture named from the current test
// path and supplied payload kind. It compacts JSON whitespace for exact
// storage-byte comparisons, failing the test on file or JSON errors.
func historicalSchedulerPayload(t *testing.T, kind string) []byte {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_") + "-" + kind + ".json"
	value, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	// Remove fixture whitespace for byte comparisons while retaining field
	// order, escapes and collection shape, so formatting cannot obscure a
	// storage representation change.
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, value))
	return compact.Bytes()
}

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

type schedulerCache struct{ *SnapshotCache }

func (c *schedulerCache) SnapshotCoreCache() scheduler.SnapshotCache { return c.SnapshotCache }

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

func (c *schedulerCache) GetProvider(ctx context.Context, id int64) (*providercore.Record, error) {
	v, err := c.SnapshotCache.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	return codec.RecordValue(v)
}

func (c *schedulerCache) SetProvider(ctx context.Context, v *providercore.Record) error {
	return c.SnapshotCache.SetProvider(ctx, codec.WrapRecord(v))
}

func (c *schedulerCache) SetSnapshot(ctx context.Context, bucket scheduler.SchedulerBucket, token scheduler.SchedulerBucketWriteToken, values []providercore.Record) error {
	return c.SnapshotCache.SetSnapshot(ctx, bucket, token, snapshotRecords(values))
}

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

func marshalSchedulerCacheProvider(value providercore.Record) ([]byte, []byte, error) {
	return (codec.ProviderCodec{}).Encode(codec.WrapRecord(&value))
}

// historicalSchedulerPayload loads a fixed historical snapshot for comparison with encoder output.
func historicalSchedulerPayload(t *testing.T, kind string) []byte {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_") + "-" + kind + ".json"
	value, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	// Compact fixture whitespace while retaining field order, escaping, and nil-versus-empty distinctions.
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, value))
	return compact.Bytes()
}

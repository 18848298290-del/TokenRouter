//go:build integration

package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRequestTimingsStageAndLegacyRoundTrip stores actual access-log JSON in the
// isolated PostgreSQL fixture. Batched timing lookup preserves measured zero,
// applicability flags, and offsets while legacy records retain absent stage durations.
func TestRequestTimingsStageAndLegacyRoundTrip(t *testing.T) {
	ctx := t.Context()
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_system_logs(request_id,component,message,level,extra) VALUES
 ('timing-stage-roundtrip','http.access','http request completed','info','{"timing_stream":1,"timing_admitted":1,"request_body_read_ms":118000,"preprocess_ms":235,"user_slot_wait_ms":0,"provider_slot_wait_ms":0,"provider_slot_acquired_ms":118235,"upstream_first_event_ms":125400}'),
 ('timing-legacy-roundtrip','http.access','http request completed','info','{"provider_slot_acquired_ms":118235,"upstream_first_sse_data_ms":125400}')`)
	require.NoError(t, err)
	// Delete only this fixture's two rows, leaving unrelated integration cases
	// independent when they share the same migrated temporary database instance.
	defer func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_system_logs WHERE request_id IN ('timing-stage-roundtrip','timing-legacy-roundtrip')`)
	}()
	got, err := NewOpsRepository(integrationDB).ListRequestTimings(ctx, []string{"timing-stage-roundtrip", "timing-legacy-roundtrip"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, int64(1), *got["timing-stage-roundtrip"].TimingAdmitted)
	require.Equal(t, int64(0), *got["timing-stage-roundtrip"].ProviderSlotWaitMs)
	require.Equal(t, int64(118000), *got["timing-stage-roundtrip"].RequestBodyReadMs)
	require.Equal(t, int64(125400), *got["timing-stage-roundtrip"].UpstreamFirstEventMs)
	require.Nil(t, got["timing-legacy-roundtrip"].ProviderSlotWaitMs)
	require.Nil(t, got["timing-legacy-roundtrip"].TimingAdmitted)
	require.Nil(t, got["timing-legacy-roundtrip"].UpstreamFirstEventMs)
	require.Equal(t, int64(125400), *got["timing-legacy-roundtrip"].UpstreamFirstSSEDataMs)
}

package ops

import (
	"encoding/json"
	"testing"
)

// TestOpsRequestTimingFromExtra supplies body length, elapsed stages, counters,
// and connection flags in several decoded value types. It checks the accepted
// numeric values and preserves a zero stage as a present measurement.
func TestOpsRequestTimingFromExtra(t *testing.T) {
	timing := OpsRequestTimingFromExtra(map[string]any{
		"request_content_length":          float64(8360000),
		"provider_slot_acquired_ms":       float64(0),
		"upstream_first_response_byte_ms": json.Number("119810"),
		"upstream_first_sse_data_ms":      "119900",
		"upstream_attempt_count":          float64(2),
		"upstream_connection_reused":      true,
		"upstream_wrote_request_error":    false,
		"unrelated_sensitive_field":       "must not be copied",
	})
	if timing == nil {
		t.Fatal("expected timing")
	}
	if timing.RequestContentLength == nil || *timing.RequestContentLength != 8360000 {
		t.Fatalf("request content length = %+v", timing.RequestContentLength)
	}
	if timing.ProviderSlotAcquiredMs == nil || *timing.ProviderSlotAcquiredMs != 0 {
		t.Fatalf("zero stage should be preserved: %+v", timing.ProviderSlotAcquiredMs)
	}
	if timing.UpstreamFirstResponseByteMs == nil || *timing.UpstreamFirstResponseByteMs != 119810 {
		t.Fatalf("first response byte = %+v", timing.UpstreamFirstResponseByteMs)
	}
	if timing.UpstreamFirstSSEDataMs == nil || *timing.UpstreamFirstSSEDataMs != 119900 {
		t.Fatalf("first SSE = %+v", timing.UpstreamFirstSSEDataMs)
	}
	if timing.UpstreamAttemptCount == nil || *timing.UpstreamAttemptCount != 2 || !timing.UpstreamConnectionReused {
		t.Fatalf("transport fields = %+v", timing)
	}
}

// TestOpsRequestTimingFromExtraReturnsNilWithoutTimingFields supplies an
// unrelated message field with no timing observations. It checks the nil result
// used to represent absent timing evidence rather than an allocated empty
// timing object.
func TestOpsRequestTimingFromExtraReturnsNilWithoutTimingFields(t *testing.T) {
	if got := OpsRequestTimingFromExtra(map[string]any{"message": "http request completed"}); got != nil {
		t.Fatalf("expected nil timing, got %+v", got)
	}
}

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

// TestOpsRequestTimingFromExtraRetainsSparseStages supplies each admission field
// independently with measured zero. A sparse record must remain present even
// when no body length or older transport milestone accompanies that observation.
func TestOpsRequestTimingFromExtraRetainsSparseStages(t *testing.T) {
	for _, key := range []string{"timing_stream", "timing_admitted", "request_body_read_ms", "preprocess_ms", "user_slot_wait_ms", "provider_slot_wait_ms", "retry_provider_slot_wait_ms", "upstream_first_event_ms"} {
		t.Run(key, func(t *testing.T) {
			timing := OpsRequestTimingFromExtra(map[string]any{key: float64(0)})
			if timing == nil {
				t.Fatal("measured zero was discarded")
			}
			encoded, err := json.Marshal(timing)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			value, ok := fields[key]
			if !ok || value != float64(0) {
				t.Fatalf("field %s = %v, present=%v", key, value, ok)
			}
		})
	}
}

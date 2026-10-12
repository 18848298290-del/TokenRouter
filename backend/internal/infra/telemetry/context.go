package telemetry

const (
	RequestID ContextKey = "ctx_request_id"
	// ClientRequestID is a compatibility alias for the internal RequestID key.
	ClientRequestID        ContextKey = RequestID
	ParentRequestID        ContextKey = "ctx_parent_request_id"
	ParentClientRequestID  ContextKey = "ctx_parent_client_request_id"
	RequestStartedAt       ContextKey = "ctx_request_started_at"
	ProviderSlotAcquiredAt ContextKey = "ctx_provider_slot_acquired_at"
	FirstSSEDataAt         ContextKey = "ctx_first_sse_data_at"
	FirstDownstreamFlushAt ContextKey = "ctx_first_downstream_flush_at"
	FirstVisibleOutputAt   ContextKey = "ctx_first_visible_output_at"
	Model                  ContextKey = "ctx_model"
	ClientModel            ContextKey = "ctx_client_model"
	Platform               ContextKey = "ctx_platform"
	ProviderID             ContextKey = "ctx_provider_id"
)

// ContextKey separates telemetry keys from ordinary string context keys.
type ContextKey string

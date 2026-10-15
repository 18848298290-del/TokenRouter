package telemetry

// context.go defines context keys for request identity and gateway timing
// observations.
// Request identity and transport-stage observations travel with the request
// context so middleware and detached completion work can read the same
// values. The compatibility ClientRequestID constant shares RequestID's key;
// the caller's external ID has its own parent-client key.

// Telemetry context keys connect request middleware, gateway observation, and
// completion readers while keeping internal identity separate from external caller IDs.
const (
	RequestID ContextKey = "ctx_request_id" // internal request ID string
	// ClientRequestID is a compatibility alias for the internal RequestID key.
	ClientRequestID        ContextKey = RequestID
	ParentRequestID        ContextKey = "ctx_parent_request_id"         // parent logical request ID string
	ParentClientRequestID  ContextKey = "ctx_parent_client_request_id"  // external caller ID string
	RequestStartedAt       ContextKey = "ctx_request_started_at"        // request start time.Time
	ProviderSlotAcquiredAt ContextKey = "ctx_provider_slot_acquired_at" // provider admission time.Time
	FirstSSEDataAt         ContextKey = "ctx_first_sse_data_at"         // first upstream SSE time.Time
	FirstDownstreamFlushAt ContextKey = "ctx_first_downstream_flush_at" // first downstream flush time.Time
	FirstVisibleOutputAt   ContextKey = "ctx_first_visible_output_at"   // first visible output time.Time
	Model                  ContextKey = "ctx_model"                     // observed request model string
	ClientModel            ContextKey = "ctx_client_model"              // client display model override string
	Platform               ContextKey = "ctx_platform"                  // observed upstream platform string
	ProviderID             ContextKey = "ctx_provider_id"               // observed provider int64 ID
)

// ContextKey separates telemetry keys from ordinary string context keys.
type ContextKey string

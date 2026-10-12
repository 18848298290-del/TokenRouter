package ops

// OpsUpstreamErrorEvent describes one upstream error attempt during a single gateway request.
// It is stored in ops_error_logs.upstream_errors as a JSON array.
type OpsUpstreamErrorEvent struct {
	AtUnixMs int64 `json:"at_unix_ms,omitempty"`

	// Passthrough records whether the request used the branch that forwards the payload with replacement authentication.
	// It is persisted in event JSON for diagnosis and rollout evaluation.
	Passthrough bool `json:"passthrough,omitempty"`

	// Context
	Platform     string `json:"platform,omitempty"`
	ProviderID   int64  `json:"provider_id,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`

	// Outcome
	UpstreamStatusCode int    `json:"upstream_status_code,omitempty"`
	UpstreamRequestID  string `json:"upstream_request_id,omitempty"`

	// UpstreamURL is the actual upstream URL that was called (host + path, query/fragment stripped).
	// Helps debug 404/routing errors by showing which endpoint was targeted.
	UpstreamURL string `json:"upstream_url,omitempty"`

	// Best-effort upstream response capture (sanitized+trimmed).
	UpstreamResponseBody string `json:"upstream_response_body,omitempty"`

	// Kind: http_error | request_error | retry_exhausted | failover
	Kind string `json:"kind,omitempty"`
	// Stage, Scope, and Reason distinguish credential acquisition from inference failures when no HTTP status exists.
	Stage  string `json:"stage,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Reason string `json:"reason,omitempty"`

	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`

	// SkipMonitoring is request-local rule state. It is intentionally excluded
	// from persisted attempt JSON. The logger consults it only when this event is
	// the final client-visible failure; recovered attempts remain provider-health
	// telemetry and do not count as failed requests.
	SkipMonitoring bool `json:"-"`
}

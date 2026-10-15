package ops

// upstream_event.go defines the diagnostic fields serialized for individual
// upstream attempts.
// One gateway request can fail several upstream attempts before succeeding or
// returning its final error. Attempt records keep provider identity, upstream
// correlation, stage, and response diagnostics separate from the final client
// status; request-local monitoring suppression is evaluated before the
// persisted JSON drops that flag.

// OpsUpstreamErrorEvent carries one observed upstream attempt for diagnostic
// JSON stored with an Ops error.
type OpsUpstreamErrorEvent struct {
	// AtUnixMs records the attempt timestamp in milliseconds since the Unix epoch.
	// Zero omits the JSON field, and Ops sanitation replaces negative values with
	// zero.
	AtUnixMs int64 `json:"at_unix_ms,omitempty"`

	// Passthrough records a payload-forwarding branch using replacement
	// authentication. Gateway producers set the flag for their chosen execution
	// path, and it is retained in diagnostic attempt JSON.
	Passthrough bool `json:"passthrough,omitempty"`

	Platform     string `json:"platform,omitempty"`      // observed execution platform label
	ProviderID   int64  `json:"provider_id,omitempty"`   // observed execution provider identity
	ProviderName string `json:"provider_name,omitempty"` // observed provider display name

	UpstreamStatusCode int `json:"upstream_status_code,omitempty"` // observed upstream HTTP status
	// upstream request correlation alias
	UpstreamRequestID string `json:"upstream_request_id,omitempty"`

	// UpstreamURL carries supplied endpoint diagnostic text. Append and
	// preparation paths trim it and Ops bounds its bytes; producers use
	// logredact.SafeUpstreamURL when query and fragment removal is required.
	UpstreamURL string `json:"upstream_url,omitempty"`

	// UpstreamResponseBody contains a captured upstream response supplied by the
	// producer. Ops sanitation bounds it before queueing and redacts supported
	// JSON credentials, while non-JSON text follows the byte-truncation path.
	UpstreamResponseBody string `json:"upstream_response_body,omitempty"`

	Kind string `json:"kind,omitempty"` // attempt outcome category label
	// Stage, Scope, and Reason distinguish credential acquisition failures from
	// inference outcomes when no HTTP status is available. prepareErrorLogInput
	// attributes the stored error to provider_auth when the last nonnil attempt
	// event has that stage.
	Stage  string `json:"stage,omitempty"`  // attempt execution stage label
	Scope  string `json:"scope,omitempty"`  // credential failure scope label
	Reason string `json:"reason,omitempty"` // credential failure reason label

	Message string `json:"message,omitempty"` // observed attempt message text
	Detail  string `json:"detail,omitempty"`  // observed attempt diagnostic payload

	// SkipMonitoring carries the attempt-local passthrough-rule decision and is
	// omitted from persisted JSON. Final-failure checks consult the latest event
	// when no request override wins, and recovered telemetry filters out flagged
	// attempts.
	SkipMonitoring bool `json:"-"`
}

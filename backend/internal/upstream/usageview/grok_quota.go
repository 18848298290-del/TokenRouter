package usageview

// A Grok response can contain rate-limit windows, retry delay, or subscription
// headers independently. Older serialized snapshots can also retain those
// values while their HeadersObserved marker is false. HasObservedHeaders
// therefore checks the marker and the retained fields, giving callers a
// presence decision that remains useful for either representation.
//
// Observation timestamps retain different events: an active probe, usable
// headers, and a snapshot write. Status and timestamps alone do not establish
// quota evidence, and the presence helper does not validate header values or
// their freshness. Grok plan consumers separately age header and retained Grok
// 4.5 evidence so later snapshot writes do not make an earlier plan
// observation fresh again.

// QuotaSnapshot retains Grok rate-limit evidence and its observation metadata
// for consumers.
type QuotaSnapshot struct {
	// Requests and Tokens retain independently observed quota dimensions.
	// Missing windows stay nil; HasObservedHeaders accepts either window as
	// evidence even when the serialized HeadersObserved flag came from older data.
	Requests          *QuotaWindow `json:"requests,omitempty"`            // observed request quota window
	Tokens            *QuotaWindow `json:"tokens,omitempty"`              // observed token quota window
	RetryAfterSeconds *int         `json:"retry_after_seconds,omitempty"` // seconds
	SubscriptionTier  string       `json:"subscription_tier,omitempty"`   // reported subscription tier header
	EntitlementStatus string       `json:"entitlement_status,omitempty"`  // reported entitlement status header
	StatusCode        int          `json:"status_code,omitempty"`         // observed HTTP response status
	// Headers retains the Grok parser's allowlisted nonblank response values.
	// Snapshot copies need separate map storage before callers independently
	// change entries.
	Headers map[string]string `json:"headers,omitempty"` // retained allowlisted header values
	// HeadersObserved records current parser evidence. HasObservedHeaders also
	// inspects retained windows, delay, tier, entitlement, and Headers for older
	// snapshots.
	HeadersObserved   bool   `json:"headers_observed"`             // stored header observation flag
	ObservationSource string `json:"observation_source,omitempty"` // header observation source label
	// LastProbeAt, LastHeadersSeenAt, and UpdatedAt have different endpoints.
	// Probe time records active probing, header time records usable header evidence,
	// and update time records snapshot construction; Grok stamps them as UTC RFC3339.
	LastProbeAt       string `json:"last_probe_at,omitempty"`        // latest active probe timestamp
	LastHeadersSeenAt string `json:"last_headers_seen_at,omitempty"` // latest usable headers timestamp
	UpdatedAt         string `json:"updated_at"`                     // latest snapshot update timestamp
	Model             string `json:"model,omitempty"`                // model producing quota headers
	// PlanFrom45Responses and PlanFrom45ResponsesAt retain the Grok 4.5 plan
	// signal and its original observation time across updates from other
	// models. grok.ApplyGrok45ResponsesPlanSignal recognizes Heavy when
	// request limit is at least 8300 or token limit is at least 53000000.
	// grok.Grok45ResponsesPlanHint ages the retained signal using this
	// timestamp before using it for plan inference.
	PlanFrom45Responses   string `json:"plan_from_45_responses,omitempty"`    // retained inferred plan label
	PlanFrom45ResponsesAt string `json:"plan_from_45_responses_at,omitempty"` // retained plan observation timestamp
}

// HasObservedHeaders reports whether a snapshot carries current or retained
// header evidence. Nil input returns false; a true HeadersObserved marker is
// sufficient on its own.
//
// For older or manually constructed snapshots with a false marker, either
// quota-window pointer, retry-delay pointer, nonempty tier or entitlement
// text, or a nonempty Headers map also qualifies. Even a pointed zero or an
// empty nonnil window counts as presence. Status, timestamps, model, and
// retained plan hints are excluded from this test; callers validate values and
// freshness separately.
func (s *QuotaSnapshot) HasObservedHeaders() bool {
	if s == nil {
		return false
	}
	return s.HeadersObserved ||
		s.Requests != nil ||
		s.Tokens != nil ||
		s.RetryAfterSeconds != nil ||
		s.SubscriptionTier != "" ||
		s.EntitlementStatus != "" ||
		len(s.Headers) > 0
}

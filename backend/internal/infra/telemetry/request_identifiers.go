package telemetry

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"
)

// request_identifiers.go records request-query identifiers with their observed
// source and upstream attempt ownership. Provider selection fixes one response
// header before dispatch. Each HTTP exchange then receives an immutable reference
// to its provider and ordinal, so responses from abandoned exchanges cannot write
// their identifiers into a replacement attempt. Empty observations leave an
// earlier identifier intact, and a configured header never falls back to another
// response header.
//
// Response-object identifiers use a separate alias category. The existing alias
// value index continues to serve lookup independently of these descriptive JSON
// fields. This collector neither chooses usage-log identifiers nor changes any
// inbound, outbound, or client-facing HTTP header. Legacy aliases remain readable
// without reconstructing their provenance from their value or current settings.

// Private keys separate provider selection from the immutable reference attached
// to an actual dispatch; neither value changes the HTTP request's wire headers.
const (
	requestProviderKey ContextKey = "request_diagnostic_provider"
	requestAttemptKey  ContextKey = "request_diagnostic_attempt"
)

// RequestAttemptRef identifies the dispatch owning a request-query observation.
type RequestAttemptRef struct {
	Number     int    // recorded attempt ordinal
	ProviderID int64  // selected provider identifier
	Header     string // selected response header name
}

// WithRequestProvider snapshots the diagnostic header for one provider selection.
// Blank configuration selects X-Request-ID. Call before dispatch and retain the
// returned context for that selection; later provider mutations cannot alter it.
// @project-doc docs/operations/request_lookup.md#request_identifier_sources
func WithRequestProvider(ctx context.Context, providerID int64, header string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	header = strings.TrimSpace(header)
	if header == "" {
		header = "X-Request-ID"
	}
	selection := RequestAttemptRef{ProviderID: providerID, Header: http.CanonicalHeaderKey(header)}
	return context.WithValue(ctx, requestProviderKey, selection)
}

// RequestProvider reads the immutable provider selection used by diagnostic capture.
// An unbound context uses its existing provider identity and the default header.
// Reading this snapshot does not inspect live provider settings or response data.
func RequestProvider(ctx context.Context) RequestAttemptRef {
	if ctx == nil {
		return RequestAttemptRef{Header: "X-Request-Id"}
	}
	if selected, ok := ctx.Value(requestProviderKey).(RequestAttemptRef); ok {
		return selected
	}
	id, _ := ctx.Value(ProviderID).(int64)
	return RequestAttemptRef{ProviderID: id, Header: "X-Request-Id"}
}

// BeginRequestAttempt attaches a fixed diagnostic reference to one HTTP dispatch.
// A positive number comes from the existing timing collector. Without timing,
// a captured provider request allocates the next diagnostic ordinal instead.
//
// Registration occurs under the request recorder's lock, before any response can
// arrive. A context without request capture still carries a positive supplied
// ordinal for transport observation. A missing provider with no supplied ordinal
// does not create a synthetic attempt for unrelated HTTP work. Callers use the
// returned context for all observations belonging to this dispatch.
func BeginRequestAttempt(ctx context.Context, number int) context.Context {
	ref := RequestProvider(ctx)
	ref.Number = number
	UpdateRequest(ctx, func(record *RequestRecord) {
		if ref.Number <= 0 {
			if ref.ProviderID <= 0 {
				return
			}
			for _, attempt := range record.Attempts {
				if attempt.Number >= ref.Number {
					ref.Number = attempt.Number + 1
				}
			}
			if ref.Number <= 0 {
				ref.Number = 1
			}
		}
		// Bound retained attempts using the existing diagnostic storage limit.
		if len(record.Attempts) < 256 {
			at := time.Now()
			record.Attempts = append(record.Attempts, RequestAttempt{Number: ref.Number, ProviderID: ref.ProviderID, RequestIDHeader: ref.Header, StartedAt: &at, Outcome: "running"})
		}
	})
	return context.WithValue(ctx, requestAttemptKey, ref)
}

// RequestAttemptFromContext returns the fixed reference attached to a dispatch.
// Missing references return zero; callers must not substitute a mutable current
// attempt when handling an independently arriving response or delayed callback.
func RequestAttemptFromContext(ctx context.Context) RequestAttemptRef {
	if ctx == nil {
		return RequestAttemptRef{}
	}
	ref, _ := ctx.Value(requestAttemptKey).(RequestAttemptRef)
	return ref
}

// RequestAttemptFromResponse reads the dispatch reference retained by net/http.
// Missing response requests return zero, as they provide no transport ownership
// evidence. Executors copy this value into results before response cleanup.
func RequestAttemptFromResponse(response *http.Response) RequestAttemptRef {
	if response == nil || response.Request == nil {
		return RequestAttemptRef{}
	}
	return RequestAttemptFromContext(response.Request.Context())
}

// FindRequestAttempt finds an exact ordinal and provider in a captured request.
// A zero ordinal requires a unique matching attempt; supplied response headers
// narrow that search using the already observed selected-header value. Reused IDs
// remain ambiguous and return zero instead of selecting the latest attempt.
// Completion adapters without timing supply response headers to find their sole
// matching dispatch. Reused upstream IDs or identical absent values cannot prove
// which retry produced a result, so those cases retain request-level evidence
// rather than assigning a false provider attempt.
func FindRequestAttempt(ctx context.Context, number int, providerID int64, headers ...http.Header) RequestAttemptRef {
	if ctx == nil {
		return RequestAttemptRef{}
	}
	capture, _ := ctx.Value(requestCaptureKey).(*RequestCapture)
	if capture == nil {
		return RequestAttemptRef{}
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var found RequestAttemptRef
	for _, attempt := range capture.record.Attempts {
		if attempt.ProviderID != providerID || number > 0 && attempt.Number != number {
			continue
		}
		if number <= 0 && len(headers) > 0 && (attempt.RequestIDHeader == "" || attempt.RequestID != strings.TrimSpace(headers[0].Get(attempt.RequestIDHeader))) {
			continue
		}
		// Conflicting response evidence cannot belong to this numbered attempt;
		// retain unassigned response diagnostics rather than attaching another retry's result.
		if number > 0 && len(headers) > 0 && len(headers[0]) > 0 && attempt.RequestID != "" && attempt.RequestIDHeader != "" && attempt.RequestID != strings.TrimSpace(headers[0].Get(attempt.RequestIDHeader)) {
			continue
		}
		if found.Number > 0 {
			return RequestAttemptRef{}
		}
		found = RequestAttemptRef{Number: attempt.Number, ProviderID: providerID, Header: attempt.RequestIDHeader}
	}
	return found
}

// ObserveRequestHeaders records the selected header on its owning upstream attempt.
// It reads the actual response map without editing it. Missing or invalid values
// cannot clear an earlier value, and an absent selected header has no fallback.
//
// Provider and ordinal must both match before status or identity is updated.
// The first valid identifier owns this attempt; later sparse completion and error
// observations only add evidence. Alias publication occurs in the same mutation,
// so a persisted snapshot contains both the attempt value and its lookup alias.
func ObserveRequestHeaders(ctx context.Context, ref RequestAttemptRef, headers http.Header, status int) {
	if ref.Number <= 0 || ref.Header == "" {
		return
	}
	id := strings.TrimSpace(headers.Get(ref.Header))
	UpdateRequest(ctx, func(record *RequestRecord) {
		for i := range record.Attempts {
			attempt := &record.Attempts[i]
			if attempt.Number != ref.Number || attempt.ProviderID != ref.ProviderID {
				continue
			}
			if attempt.RequestIDHeader != "" && !strings.EqualFold(attempt.RequestIDHeader, ref.Header) {
				return
			}
			if status > 0 {
				attempt.Status = status
			}
			if attempt.RequestID == "" && validRequestAlias(id) {
				attempt.RequestID = id
				attempt.RequestIDHeader = ref.Header
			}
			if attempt.RequestID != "" {
				appendRequestAlias(record, RequestAlias{Kind: "upstream_request", Value: attempt.RequestID, Source: attempt.RequestIDHeader, AttemptNumber: ref.Number, ProviderID: ref.ProviderID})
			}
			return
		}
	})
}

// ObserveResponseID records an already parsed response-object identifier separately.
// The caller supplies its known response source and attempt; an unknown ordinal
// remains request-level evidence and is never assigned to the current attempt.
func ObserveResponseID(ctx context.Context, ref RequestAttemptRef, id string) {
	AddRequestAliasWithSource(ctx, RequestAlias{Kind: "response", Value: id, Source: "response.id", AttemptNumber: ref.Number, ProviderID: ref.ProviderID})
}

// AddRequestAliasWithSource appends bounded source and ownership metadata to aliases.
// Empty or invalid values are ignored. Identical observations are deduplicated,
// while distinct sources or attempts retain their own entries under the existing cap.
func AddRequestAliasWithSource(ctx context.Context, alias RequestAlias) {
	alias.Value = strings.TrimSpace(alias.Value)
	if !validRequestAlias(alias.Value) {
		return
	}
	UpdateRequest(ctx, func(record *RequestRecord) {
		appendRequestAlias(record, alias)
	})
}

// validRequestAlias checks the existing diagnostic identifier size and control rules.
func validRequestAlias(value string) bool {
	// Match storage normalization before publishing a diagnostic snapshot.
	return value != "" && len(value) <= 255 && !strings.ContainsAny(value, "\r\n\x00")
}

// appendRequestAlias adds one distinct observation within the request's existing cap.
// Call while holding the capture lock or while exclusively constructing a snapshot.
// The scalar alias value is copied and does not retain mutable caller data.
func appendRequestAlias(record *RequestRecord, alias RequestAlias) {
	// Keep diagnostic history within the existing request-record alias limit.
	if validRequestAlias(alias.Value) && len(record.Aliases) < 128 && !slices.Contains(record.Aliases, alias) {
		record.Aliases = append(record.Aliases, alias)
	}
}

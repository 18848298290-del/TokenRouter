package searchtools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

// emulator.go evaluates search emulation policy and executes queries through
// the current external search manager. Its structured Result leaves TokenUsage
// zero while wire responses contain a summary-length estimate, keeping display
// numbers distinct from vendor token observations.
//
// Policy requires a manager, one search tool and the global gate before
// provider mode. Enabled mode bypasses group lookup; disabled mode declines;
// default requires the group feature. Search rereads the current manager, so
// configuration can change after the policy check. OnAccepted runs before query
// validation/search to release serial ownership, while output builders handle
// the successful response's JSON or event sequence.

// Searcher executes external search with manager-owned vendor selection and
// quota reservation. Success supplies a nonnil response for synthetic output.
type Searcher interface {
	// SearchWithBestProvider executes the supplied query using external vendor
	// policy and returns response, vendor label or error. Nil error requires a
	// nonnil response; quota admission/compensation and cancellation belong to
	// the manager.
	SearchWithBestProvider(context.Context, contract.SearchRequest) (*contract.SearchResponse, string, error)
}
type (
	// SearchSource exposes the manager currently published for search emulation.
	SearchSource interface {
		// Current returns the currently published search manager or nil while
		// unavailable. Policy and execution read it separately, so each call
		// supplies a manager reference rather than retaining one across those
		// phases.
		Current() Searcher
	}
	// Settings supplies the global feature gate checked before provider mode.
	Settings interface {
		// IsWebSearchEmulationEnabled reads the global gate used before provider
		// mode or group lookup. False declines emulation while the owning forwarder
		// determines its ordinary path for the supplied request.
		IsWebSearchEmulationEnabled(context.Context) bool
	}
	// GroupPolicy reads the selected group feature for a provider platform.
	GroupPolicy interface {
		// Enabled reads the selected group feature for the supplied platform. Its
		// error declines emulation at the consumer; provider/global policy and the
		// ordinary forwarding path stay with their respective callers.
		Enabled(context.Context, int64, string) (bool, error)
	}
)

// PolicyInput carries tool declarations and selected provider/group evidence
// for emulation policy.
type PolicyInput struct {
	// Body remains caller-owned while ShouldEmulate inspects its one-tool
	// declaration shape. Keep the backing storage stable throughout that policy read.
	Body []byte // retained request body bytes
	// Mode selects override after global gating, while Platform is passed to
	// group lookup. ShouldEmulate does not validate platform eligibility itself.
	Mode, Platform string // provider emulation override and group-policy platform input
	// GroupID is needed by default group policy. Enabled or disabled mode is
	// handled before group lookup, after manager/tool/global prerequisites have
	// passed.
	GroupID *int64 // selected request group identifier
}

// Request carries retained search input and gateway-provider observations into
// synchronous emulation.
type Request struct {
	// Body is read for query extraction after OnAccepted returns. Keep its
	// backing storage stable through that callback and subsequent synchronous query read.
	Body       []byte // retained request body bytes
	ProviderID int64  // gateway-provider identifier for diagnostics
	// ProviderName identifies the gateway candidate, ProxyURL is passed to the
	// search manager, and Model supplies output spelling. Exactly empty Model
	// selects the fixed response default.
	ProviderName, ProxyURL, Model string // gateway candidate name, search outbound proxy and synthetic response model
	Stream                        bool   // select event or JSON output
	// OnAccepted runs before query extraction/search, including missing-query
	// failure. Its caller chooses serial-resource release, and invocation
	// supplies no vendor-acceptance evidence.
	OnAccepted func() // optional serial-resource release callback
}

// Result carries synthetic response identity and duration with zero TokenUsage
// from emulation.
type Result struct {
	Model    string        // synthetic response model spelling
	Duration time.Duration // injected-clock execution duration
	// Usage stays zero in both output builders. Synthetic wire estimates are
	// separate and do not populate this execution-result measurement or determine
	// funds persistence.
	Usage protocol.TokenUsage // zero structured token usage
}

// Event carries stage-specific external-search observations to a synchronous
// caller callback.
type Event struct {
	// ProviderID and ProviderName identify the gateway candidate; Provider names
	// the external search vendor. Kind determines which query, result-count or
	// error fields apply to a particular observed stage.
	Kind, Query, Provider, ProviderName string // observed search stage, user search query, external search vendor and gateway candidate name
	ProviderID                          int64  // gateway-provider identifier for diagnostics
	Results                             int    // completed search result count
	Err                                 error  // stage-specific observed operation error
}

// Output supplies the Anthropic JSON and event writer used after search
// succeeds.
type Output interface {
	// StartStream begins synthetic event output after the search succeeds. It
	// runs before the first message event; subsequent write failure stops that
	// sequence through the emulator's observed-error path.
	StartStream()
	// WriteEvent writes one named synthetic event and reports an output error.
	// The builder stops later events on its first error, then calls Flush; this
	// callback does not establish client receipt.
	WriteEvent(string, []byte) error
	// WriteJSON writes a completed synthetic payload through the caller's output
	// adapter. It returns no error, so emulation cannot infer downstream write or
	// delivery success from its callback result.
	WriteJSON([]byte)
	// Flush requests emission of buffered output after the event sequence ends or
	// stops. The adapter defines buffering and delivery behavior, while the
	// stream builder calls it once after its write loop.
	Flush()
}

// ProxyFailure retains the search proxy cause for forwarding failover
// classification.
type ProxyFailure struct {
	Cause error // wrapped search proxy error
}

// Emulator uses captured manager/policy bindings and injected clock,
// identifier and observation helpers.
type Emulator struct {
	source        SearchSource     // current search manager source
	settings      Settings         // global feature gate reader
	groupPolicies GroupPolicy      // optional group feature reader
	now           func() time.Time // execution duration clock callback
	// newID is required by successful output generation. Tool IDs slice its
	// returned string to sixteen bytes, so callers supply a usable generator with
	// sufficiently long results.
	newID    func() string // synthetic output identifier generator
	observer func(Event)   // optional synchronous stage observer
}

// Error returns the wrapped proxy cause's text for inspection. Callers provide
// a nonnil receiver and Cause; sanitization and client-facing conversion stay
// with the forwarding adapter handling that error.
func (e *ProxyFailure) Error() string { return e.Cause.Error() }

// Unwrap exposes the search proxy cause to error-chain inspection. Callers
// provide a nonnil receiver, and a nil Cause remains nil for the chain
// consumer to handle.
func (e *ProxyFailure) Unwrap() error { return e.Cause }

// NewEmulator captures supplied source, policy readers and output helpers; nil
// now selects time.Now. It starts no workers or binding validation, so callers
// supply usable source/settings and output identifiers before their respective
// operations.
func NewEmulator(source SearchSource, settings Settings, groupPolicies GroupPolicy, now func() time.Time, newID func() string, observe func(Event)) *Emulator {
	if now == nil {
		now = time.Now
	}
	return &Emulator{source: source, settings: settings, groupPolicies: groupPolicies, now: now, newID: newID, observer: observe}
}

// observe invokes the optional synchronous callback with stage event fields.
// It returns immediately when absent; callback behavior and any retained
// error/reference ownership stay with the caller providing that hook.
func (s *Emulator) observe(e Event) {
	if s.observer != nil {
		s.observer(e)
	}
}

// ShouldEmulate checks manager availability, one search tool and the global
// gate before provider/group mode. Callers supply usable source/settings and
// stable body bytes; this decision executes no query.
//
// Enabled mode allows after prerequisites and bypasses group lookup, while
// disabled mode declines. Other modes need a group/reader and treat its error
// as disabled emulation. Platform is passed to that reader rather than
// separately validated here. Search later rereads the manager, so a true
// decision retains no particular manager instance for execution.
func (s *Emulator) ShouldEmulate(ctx context.Context, in PolicyInput) bool {
	if s.source.Current() == nil {
		return false
	}
	if !IsOnlyWebSearchToolInBody(in.Body) {
		return false
	}
	if !s.settings.IsWebSearchEmulationEnabled(ctx) {
		return false
	}
	switch in.Mode {
	case "enabled":
		return true
	case "disabled":
		return false
	default:
		if in.GroupID == nil || s.groupPolicies == nil {
			return false
		}
		// Lookup failure declines emulation at this gate; the owning forwarder still
		// chooses its ordinary upstream handling for the complete supplied request.
		enabled, err := s.groupPolicies.Enabled(ctx, *in.GroupID, in.Platform)
		return err == nil && enabled
	}
}

// Search calls the current manager with query, default result limit and proxy
// spelling. An unavailable manager returns an initialization error; manager
// errors are observed and wrapped, while success retains its response and
// vendor label.
func (s *Emulator) Search(ctx context.Context, proxyURL, query string) (*contract.SearchResponse, string, error) {
	manager := s.source.Current()
	if manager == nil {
		return nil, "", fmt.Errorf("web search emulation: manager not initialized")
	}
	response, provider, err := manager.SearchWithBestProvider(ctx, contract.SearchRequest{Query: query, MaxResults: webSearchDefaultMaxResults, ProxyURL: proxyURL})
	if err != nil {
		s.observe(Event{Kind: "search_failed", Err: err})
		return nil, "", fmt.Errorf("web search emulation: %w", err)
	}
	return response, provider, nil
}

// Execute invokes optional serial release, extracts the final user query and
// searches before producing synthetic output. Callers supply usable
// bindings/output and stable body storage; OnAccepted also runs before a
// missing-query failure.
//
// Proxy-unavailable errors are wrapped for forwarding classification.
// Successful search requires nonnil response data and uses supplied Model,
// applying the default only when exactly empty. JSON writes supply no error
// result; stream write errors are observed, stop later events and are
// suppressed while returning zero TokenUsage. Display token estimates remain
// separate from that structured measurement and from the recorder's funds
// outcome.
func (s *Emulator) Execute(ctx context.Context, in Request, out Output) (*Result, error) {
	started := s.now()
	if in.OnAccepted != nil {
		in.OnAccepted()
	}
	query := ExtractSearchQueryFromBody(in.Body)
	if query == "" {
		return nil, fmt.Errorf("web search emulation: no query found in messages")
	}
	s.observe(Event{Kind: "executing", ProviderID: in.ProviderID, ProviderName: in.ProviderName, Query: query})
	response, provider, err := s.Search(ctx, in.ProxyURL, query)
	if err != nil {
		// Retain configured-proxy failure identity for the forwarding adapter's
		// distinct failover decision, before any successful synthetic output is created.
		if errors.Is(err, search.ErrProxyUnavailable) {
			return nil, &ProxyFailure{Cause: err}
		}
		return nil, err
	}
	s.observe(Event{Kind: "completed", Provider: provider, Results: len(response.Results)})
	model := in.Model
	if model == "" {
		model = defaultWebSearchModel
	}
	if in.Stream {
		return s.writeStream(out, query, response, model, started)
	}
	return s.writeJSON(out, query, response, model, started)
}

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

// Searcher is implemented by search.Manager, which owns quota reservation and rollback.
type Searcher interface {
	SearchWithBestProvider(context.Context, contract.SearchRequest) (*contract.SearchResponse, string, error)
}
type (
	SearchSource interface{ Current() Searcher }
	Settings     interface{ IsWebSearchEmulationEnabled(context.Context) bool }
	GroupPolicy  interface {
		Enabled(context.Context, int64, string) (bool, error)
	}
)

type PolicyInput struct {
	Body           []byte
	Mode, Platform string
	GroupID        *int64
}
type Request struct {
	Body                          []byte
	ProviderID                    int64
	ProviderName, ProxyURL, Model string
	Stream                        bool
	OnAccepted                    func()
}

// Result leaves billable usage at zero. Estimates in synthetic responses are not upstream measurements.
type Result struct {
	Model    string
	Duration time.Duration
	Usage    protocol.TokenUsage
}
type Event struct {
	Kind, Query, Provider, ProviderName string
	ProviderID                          int64
	Results                             int
	Err                                 error
}
type Output interface {
	StartStream()
	WriteEvent(string, []byte) error
	WriteJSON([]byte)
	Flush()
}
type ProxyFailure struct{ Cause error }

type Emulator struct {
	source        SearchSource
	settings      Settings
	groupPolicies GroupPolicy
	now           func() time.Time
	newID         func() string
	observer      func(Event)
}

func (e *ProxyFailure) Error() string { return e.Cause.Error() }
func (e *ProxyFailure) Unwrap() error { return e.Cause }

// NewEmulator uses the supplied search source and policy readers; construction starts no workers.
func NewEmulator(source SearchSource, settings Settings, groupPolicies GroupPolicy, now func() time.Time, newID func() string, observe func(Event)) *Emulator {
	if now == nil {
		now = time.Now
	}
	return &Emulator{source: source, settings: settings, groupPolicies: groupPolicies, now: now, newID: newID, observer: observe}
}

func (s *Emulator) observe(e Event) {
	if s.observer != nil {
		s.observer(e)
	}
}

// ShouldEmulate checks the search manager, tool shape, global setting, provider mode, then group policy.
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
		enabled, err := s.groupPolicies.Enabled(ctx, *in.GroupID, in.Platform)
		return err == nil && enabled
	}
}

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

// Execute calls OnAccepted before searching so the caller can release serial resources.
// Stream write failures stop output and return a completion result with zero usage.
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

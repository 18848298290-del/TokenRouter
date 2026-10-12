package searchtools

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

// Selection identifies the selected provider and how to release acquired resources.
type Selection struct {
	ProviderID int64
	Acquired   bool
	Release    func()
	WaitPlan   *scheduler.ProviderWaitPlan
}
type StandalonePorts interface {
	Select(context.Context, string, map[int64]struct{}) (Selection, bool, error)
	Acquire(context.Context, Selection) (func(), bool, error)
	Execute(context.Context, int64, StandaloneRequest, string, int) (*contract.SearchResponse, string, error)
	CanSwitch(error) bool
}
type StandaloneResult struct {
	ProviderID int64
	Response   *contract.SearchResponse
	Provider   string
}
type StandaloneFailure struct {
	Stage string
	Cause error
}

// noAvailableProvidersError reports selection exhaustion with the capitalized HTTP message.
type noAvailableProvidersError struct{}

// StandaloneRequest holds standalone search fields used by platform request builders.
type StandaloneRequest struct {
	Query                    string   `json:"query"`
	Input                    string   `json:"input"`
	MaxResults               *int     `json:"max_results"`
	AllowedXHandles          []string `json:"allowed_x_handles"`
	ExcludedXHandles         []string `json:"excluded_x_handles"`
	FromDate                 string   `json:"from_date"`
	ToDate                   string   `json:"to_date"`
	EnableImageUnderstanding *bool    `json:"enable_image_understanding"`
	EnableVideoUnderstanding *bool    `json:"enable_video_understanding"`
}

func (e *StandaloneFailure) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "web search failed"
}
func (e *StandaloneFailure) Unwrap() error { return e.Cause }

func (noAvailableProvidersError) Error() string { return "No available providers" }

// RunStandalone selects at most four providers. Initial selection errors retain their stage;
// later failures end failover. HTTP releases the request lease after writing the response.
// Switching providers releases the current attempt early.
func RunStandalone(ctx context.Context, request StandaloneRequest, model string, maxResults int, ports StandalonePorts, lease *scheduler.Lease) (StandaloneResult, error) {
	failed := make(map[int64]struct{})
	var result StandaloneResult
	hasProvider := false
	var lastErr error
	for attempt := range 4 {
		selection, present, err := ports.Select(ctx, model, failed)
		if err != nil {
			if attempt == 0 {
				return result, &StandaloneFailure{Stage: "selection", Cause: err}
			}
			break
		}
		if !present {
			if attempt == 0 {
				return result, &StandaloneFailure{Stage: "selection", Cause: noAvailableProvidersError{}}
			}
			break
		}
		release, ok, err := ports.Acquire(ctx, selection)
		if !ok {
			if attempt == 0 && err != nil {
				return result, &StandaloneFailure{Stage: "concurrency", Cause: err}
			}
			failed[selection.ProviderID] = struct{}{}
			continue
		}
		attemptLease := scheduler.NewAttemptLease(lease, nil, release)
		result.ProviderID = selection.ProviderID
		hasProvider = true
		result.Response, result.Provider, lastErr = ports.Execute(ctx, selection.ProviderID, request, model, maxResults)
		if lastErr == nil {
			break
		}
		if !ports.CanSwitch(lastErr) {
			break
		}
		failed[selection.ProviderID] = struct{}{}
		attemptLease.Release()
		result.ProviderID = 0
		hasProvider = false
	}
	if lastErr != nil || result.Response == nil {
		return result, &StandaloneFailure{Stage: "execute", Cause: lastErr}
	}
	if !hasProvider {
		return result, &StandaloneFailure{Stage: "selection", Cause: noAvailableProvidersError{}}
	}
	return result, nil
}

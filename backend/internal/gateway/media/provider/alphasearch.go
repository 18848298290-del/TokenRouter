package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// AlphaSearchOptions holds a prepared request and execution callbacks. Redact its authorization data before logging or serialization.
type AlphaSearchOptions struct {
	ProviderID        int64
	Request           *http.Request `json:"-"`
	ResponsesFallback bool
	Model             string
	Enter             func() (func(), error)
	Do                func(*http.Request) (*http.Response, error)
	Latency           func(time.Duration)
	TransportError    func(error) error
	ReadBody          func(io.Reader) ([]byte, error)
	HTTPError         func(*http.Response, []byte) error
	UpdateQuota       func(http.Header)
	Headers           func(http.Header, http.Header)
}

type AlphaSearch struct{ Options AlphaSearchOptions }

// String formats the provider ID while keeping request credentials out of formatted output.
func (o AlphaSearchOptions) String() string {
	return fmt.Sprintf("media AlphaSearch provider=%d", o.ProviderID)
}
func (o AlphaSearchOptions) GoString() string { return o.String() }

func (e AlphaSearch) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (upstream.AttemptResult, error) {
	o := e.Options
	target := &openai.AlphaSearchTarget{
		ProviderID:        o.ProviderID,
		Request:           o.Request,
		ResponsesFallback: o.ResponsesFallback,
		Model:             o.Model,
		Enter:             o.Enter,
		Do:                o.Do,
		Latency:           o.Latency,
		TransportError:    o.TransportError,
		ReadBody:          o.ReadBody,
		HTTPError:         o.HTTPError,
		UpdateQuota:       o.UpdateQuota,
		Headers:           o.Headers,
	}
	input.Target = target
	return (openai.AlphaSearchExecutor{}).Execute(ctx, input, sink)
}

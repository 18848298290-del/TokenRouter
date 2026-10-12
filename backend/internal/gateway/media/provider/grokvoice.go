package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

type GrokVoiceOptions struct {
	ProviderID                          int64
	Endpoint, BaseEndpoint, ContentType string
	Request                             *http.Request `json:"-"`
	Do                                  func(*http.Request) (*http.Response, error)
	Enter                               func() (func(), error)
	AfterExchange                       func(time.Duration, error) error
	BeforeResponse                      func(*http.Response) (bool, error)
	ReadBody                            func(io.Reader) ([]byte, error)
	CopyHeaders                         func(http.Header, http.Header)
}

type GrokVoice struct{ Options GrokVoiceOptions }

// String formats the provider ID while keeping credentials out of formatted output.
func (o GrokVoiceOptions) String() string {
	return fmt.Sprintf("media GrokVoice provider=%d", o.ProviderID)
}
func (o GrokVoiceOptions) GoString() string { return o.String() }

func (e GrokVoice) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (upstream.AttemptResult, error) {
	o := e.Options
	target := &grok.VoiceTarget{
		ProviderID:     o.ProviderID,
		Endpoint:       o.Endpoint,
		BaseEndpoint:   o.BaseEndpoint,
		ContentType:    o.ContentType,
		Request:        o.Request,
		Do:             o.Do,
		Enter:          o.Enter,
		AfterExchange:  o.AfterExchange,
		BeforeResponse: o.BeforeResponse,
		ReadBody:       o.ReadBody,
		CopyHeaders:    o.CopyHeaders,
	}
	input.Target = target
	return (grok.VoiceExecutor{}).Execute(ctx, input, sink)
}

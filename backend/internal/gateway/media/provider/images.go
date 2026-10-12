package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// ImagesOptions holds a prepared image attempt; Request contains credentials and is excluded from JSON.
type ImagesOptions struct {
	ProviderID                          int64
	OAuth                               bool
	Model, ResponseFormat, StreamPrefix string
	StartedAt                           time.Time
	Request                             *http.Request `json:"-"`
	Options                             openai.ImageResponseOptions
	Enter                               func() (func(), error)
	Do                                  func(*http.Request) (*http.Response, error)
	TransportError                      func(error) error
	ReadErrorBody                       func(*http.Response) []byte
	RedactErrorBody                     func([]byte) []byte
	HTTPError                           func(*http.Response, []byte) error
	ResponseError                       func(*http.Response, int, error) error
}

type Images struct{ Options ImagesOptions }

// String formats the provider ID while keeping request credentials out of formatted output.
func (o ImagesOptions) String() string   { return fmt.Sprintf("media Images provider=%d", o.ProviderID) }
func (o ImagesOptions) GoString() string { return o.String() }

func (e Images) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (upstream.AttemptResult, error) {
	o := e.Options
	target := &openai.ImagesTarget{
		ProviderID:      o.ProviderID,
		OAuth:           o.OAuth,
		Model:           o.Model,
		ResponseFormat:  o.ResponseFormat,
		StreamPrefix:    o.StreamPrefix,
		StartedAt:       o.StartedAt,
		Request:         o.Request,
		Options:         o.Options,
		Enter:           o.Enter,
		Do:              o.Do,
		TransportError:  o.TransportError,
		ReadErrorBody:   o.ReadErrorBody,
		RedactErrorBody: o.RedactErrorBody,
		HTTPError:       o.HTTPError,
		ResponseError:   o.ResponseError,
	}
	input.Target = target
	return (openai.ImagesExecutor{}).Execute(ctx, input, sink)
}

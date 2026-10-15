package grok

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// responses_exchange.go exchanges Grok Responses requests and retries eligible
// encrypted-history failures with a cleaned payload.
// A Grok history replay can fail because the selected deployment cannot decode
// its encrypted or Compact state. The first HTTP 400 response is inspected
// before ordinary error presentation, and a changed cleanup payload permits one
// further exchange on the same provider. Chat bridges and auxiliary image
// descriptions select SingleExchange because their callers already own the
// request conversion.
//
// Inspection consumes an error body, so the exchange closes that reader before
// returning or retrying. Unrecognized errors and cleanup with no change receive
// a replacement reader containing the captured bytes. This lets later response
// policy read the error while keeping replay count and activity ownership
// outside that policy.

// ResponsesExchange provides synchronous request construction, transport and
// replay notification callbacks.
type ResponsesExchange struct {
	// SingleExchange returns the first response for bridge and auxiliary callers
	// that supply already-converted request bytes.
	SingleExchange bool // replay inspection disabled
	// Build receives the current payload each time; replay therefore rebuilds the
	// request from cleaned bytes.
	Build func([]byte) (*http.Request, error) // required request builder
	// Do returns a response with a readable body whenever AfterExchange accepts its
	// final error result.
	Do func(*http.Request) (*http.Response, error) // required HTTP exchange
	// ReadError returns retained bytes for initial 400 inspection when replay is
	// enabled; keep that storage stable while a replacement body or recovery uses
	// it.
	ReadError func(*http.Response) []byte // error body capture
	// AfterExchange runs after every Do result, including failures; its returned
	// error determines whether response processing proceeds.
	AfterExchange func(error) error // optional error translation
	// OnReplay runs after cleanup changes the payload and before the next request
	// is built.
	OnReplay func() // optional replay notice
}

// ExchangeResponses sends body and returns the response with the effective
// request bytes. Build and Do are required, and accepted exchanges need a
// response body. ReadError is needed for initial HTTP 400 inspection when replay
// is enabled; SingleExchange skips that inspection.
//
// The first 400 is consumed and closed before encrypted or Compact cleanup is
// selected. Changed cleanup permits one replay; unchanged or unrecognized errors
// regain a reader for later policy. AfterExchange translates each transport
// result, and OnReplay runs before the second Build.
func ExchangeResponses(body []byte, o ResponsesExchange) (*http.Response, []byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := o.Build(body)
		if err != nil {
			return nil, body, err
		}
		resp, err := o.Do(req)
		if o.AfterExchange != nil {
			err = o.AfterExchange(err)
		}
		if err != nil {
			return nil, body, err
		}
		if o.SingleExchange || attempt > 0 || resp.StatusCode != http.StatusBadRequest {
			return resp, body, nil
		}
		// Capture the first denial before replay selection; ordinary error policy
		// still needs the same bytes when cleanup cannot proceed.
		data := o.ReadError(resp)
		if resp.Body != nil {
			// The consumed response cannot survive into another exchange. Release its
			// transport ownership before constructing a retry or replacement reader.
			_ = resp.Body.Close()
		}
		codec := BodyCodec{}
		invalid := codec.IsGrokInvalidEncryptedContentResponse(resp.StatusCode, data)
		if !invalid && !codec.IsGrokCompactionReplayDecodeError(resp.StatusCode, data) {
			// Replay captured error bytes for later policy because the first reader has
			// already been consumed and closed.
			resp.Body = io.NopCloser(bytes.NewReader(data))
			return resp, body, nil
		}
		var retry []byte
		var changed bool
		// Encrypted rejection takes the stripping path first; other recognized
		// history-decoding failures also remove unusable Compact replay context before
		// retry.
		if invalid {
			retry, changed, err = codec.TrimGrokInvalidEncryptedContentRetryBody(body)
		} else {
			retry, changed, err = codec.SanitizeGrokCompactionReplayBody(body)
		}
		if err != nil {
			return nil, body, fmt.Errorf("prepare Grok replay decode retry: %w", err)
		}
		if !changed {
			// Replay captured error bytes for later policy because the first reader has
			// already been consumed and closed.
			resp.Body = io.NopCloser(bytes.NewReader(data))
			return resp, body, nil
		}
		// Publish cleanup as the effective payload only after a change is confirmed,
		// limiting replay to one subsequent exchange.
		body = retry
		if o.OnReplay != nil {
			o.OnReplay()
		}
	}
}

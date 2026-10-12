package grok

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

type ResponsesExchange struct {
	// SingleExchange disables decryption recovery for bridge and auxiliary requests.
	SingleExchange bool
	Build          func([]byte) (*http.Request, error)
	Do             func(*http.Request) (*http.Response, error)
	ReadError      func(*http.Response) []byte
	AfterExchange  func(error) error
	OnReplay       func()
}

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
		data := o.ReadError(resp)
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		codec := BodyCodec{}
		invalid := codec.IsGrokInvalidEncryptedContentResponse(resp.StatusCode, data)
		if !invalid && !codec.IsGrokCompactionReplayDecodeError(resp.StatusCode, data) {
			resp.Body = io.NopCloser(bytes.NewReader(data))
			return resp, body, nil
		}
		var retry []byte
		var changed bool
		if invalid {
			retry, changed, err = codec.TrimGrokInvalidEncryptedContentRetryBody(body)
		} else {
			retry, changed, err = codec.SanitizeGrokCompactionReplayBody(body)
		}
		if err != nil {
			return nil, body, fmt.Errorf("prepare Grok replay decode retry: %w", err)
		}
		if !changed {
			resp.Body = io.NopCloser(bytes.NewReader(data))
			return resp, body, nil
		}
		body = retry
		if o.OnReplay != nil {
			o.OnReplay()
		}
	}
}

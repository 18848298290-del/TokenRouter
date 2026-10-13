package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Input-token queries have two response forms: a compatibility path extracts
// one count, and the native Responses path forwards the complete upstream JSON.
// The native reader requires a numeric input_tokens field, while the
// compatibility path accepts an existing field and converts it through gjson's
// integer reader.
//
// Both paths hold optional admission until response processing finishes and
// close the acquired response body on every exit. The caller supplies
// authentication, transport, upstream-status handling, and error output
// callbacks. Request construction forwards only the supplied User-Agent and
// language values before custom User-Agent selection and provider overrides,
// keeping inference-session headers out of the token-query request.

// InputTokensOptions supplies admission, transport, and error output for
// compatibility token counting.
type InputTokensOptions struct {
	// Enter optionally admits the query and returns a release callback; the caller
	// remains admitted through response reading, output, and body cleanup.
	Enter          func() (func(), error)
	Do             func(*http.Request) (*http.Response, error) // required HTTP exchange callback
	TransportError func(error) error                           // required transport error translator
	HTTPError      func(*http.Response, []byte) error          // required upstream status handler
	WriteError     func(int, string, string)                   // required local error writer
}

// NativeInputTokensOptions supplies transport and body reading for native token
// queries.
type NativeInputTokensOptions struct {
	// Enter optionally admits the query and returns a release callback; the caller
	// remains admitted through response reading, output, and body cleanup.
	Enter          func() (func(), error)
	Do             func(*http.Request) (*http.Response, error) // required HTTP exchange callback
	TransportError func(error) error                           // required transport error translator
	ReadBody       func(*http.Response) ([]byte, error)        // required native response reader
	HTTPError      func(*http.Response, []byte) error          // required upstream status handler
	WriteError     func(int, string, string)                   // required local error writer
}

// CountInputTokens sends request, extracts input_tokens, and writes a
// compatibility count response. Enter is optional; all other callbacks and
// request are required, response ownership stays here, and missing counts or
// read failures also invoke WriteError.
func CountInputTokens(request *http.Request, options InputTokensOptions, sink upstream.OutputSink) error {
	if options.Enter != nil {
		done, err := options.Enter()
		if err != nil {
			return err
		}
		// Hold admission through token response processing; deferred release runs
		// after the response-body cleanup registered below, including upstream error exits.
		defer done()
	}
	resp, err := options.Do(request)
	if err != nil {
		return options.TransportError(err)
	}
	// Release the acquired response on status, field-validation, and read exits
	// so token-query failures do not keep an HTTP transport connection occupied.
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		options.WriteError(http.StatusBadGateway, "upstream_error", "Failed to read response")
		return fmt.Errorf("read input_tokens response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return options.HTTPError(resp, body)
	}
	tokens := gjson.GetBytes(body, "input_tokens")
	if !tokens.Exists() {
		options.WriteError(http.StatusBadGateway, "upstream_error", "Upstream response missing input_tokens")
		return fmt.Errorf("input_tokens response missing input_tokens field")
	}
	// Compatibility output materializes the observed field as an integer count.
	upstream.NewDeferredOutputContext(sink).JSON(http.StatusOK, map[string]any{"input_tokens": int(tokens.Int())})
	return nil
}

// BuildInputTokensRequest constructs an authenticated JSON POST for the
// input-token endpoint. The options callbacks and customUserAgent reader are
// required; forwarding keeps User-Agent and Accept-Language, custom UA follows
// them, and provider overrides run last.
func BuildInputTokensRequest(ctx context.Context, body []byte, options ResponsesRequestOptions, customUserAgent func() string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, options.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(upstream.WithHTTPUpstreamProfile(req.Context(), upstream.HTTPUpstreamProfileOpenAI))
	authHeaders, err := options.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")

	{
		for key, values := range options.ForwardHeaders() {
			lower := strings.ToLower(strings.TrimSpace(key))
			if lower != "user-agent" && lower != "accept-language" {
				continue
			}
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}
	if customUA := customUserAgent(); customUA != "" {
		req.Header.Set("user-agent", customUA)
	}

	// Provider overrides run after permitted inbound and custom User-Agent values,
	// so enabled API-key configuration gets the final choice of allowed header values.
	options.OverrideHeaders(req.Header)

	return req, nil
}

// CountNativeInputTokens sends request and forwards complete JSON after
// validating a numeric input_tokens field. Enter is optional and other
// callbacks are required; this function closes the response, while ReadBody
// determines its read budget and WriteError emits local failures.
func CountNativeInputTokens(request *http.Request, options NativeInputTokensOptions, sink upstream.OutputSink) error {
	if options.Enter != nil {
		done, err := options.Enter()
		if err != nil {
			return err
		}
		// Hold admission through token response processing; deferred release runs
		// after the response-body cleanup registered below, including upstream error exits.
		defer done()
	}
	resp, err := options.Do(request)
	if err != nil {
		return options.TransportError(err)
	}
	// Release the acquired response on status, field-validation, and read exits
	// so token-query failures do not keep an HTTP transport connection occupied.
	defer func() { _ = resp.Body.Close() }()
	body, err := options.ReadBody(resp)
	if err != nil {
		options.WriteError(http.StatusBadGateway, "upstream_error", "Failed to read response")
		return err
	}
	if resp.StatusCode >= 400 {
		return options.HTTPError(resp, body)
	}
	tokens := gjson.GetBytes(body, "input_tokens")
	if !tokens.Exists() || tokens.Type != gjson.Number {
		options.WriteError(http.StatusBadGateway, "upstream_error", "Upstream response missing input_tokens")
		return fmt.Errorf("responses input_tokens: upstream response missing input_tokens")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		// A native token response lacking a media type still contains the checked
		// JSON field, so downstream clients receive the JSON type expected here.
		contentType = "application/json"
	}
	upstream.NewDeferredOutputContext(sink).Data(http.StatusOK, contentType, body)
	return nil
}

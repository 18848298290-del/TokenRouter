package openai

import (
	"context"
	"net/http"
	"strings"
)

// images_request.go builds authenticated image requests from prepared bodies
// and their JSON or multipart content types.
// Image request bodies can be JSON or multipart, so construction receives their
// existing content type with prepared bytes. Selected authentication precedes
// allowed inbound headers, User-Agent selection, and provider overrides,
// keeping the transport request compatible with the selected image endpoint.

// BuildImagesRequest constructs an authenticated image POST with the supplied
// body and content type. Required options control permitted headers and
// identity; nonblank contentType replaces forwarding, and allowed provider
// overrides run last with ctx retained for sending.
func BuildImagesRequest(ctx context.Context, body []byte, contentType string, options ResponsesRequestOptions) (*http.Request, error) {
	req, err := newAuthenticatedPostRequest(ctx, options.URL, body, options.Authenticate)
	if err != nil {
		return nil, err
	}
	for key, values := range options.ForwardHeaders() {
		if !options.AllowHeader(strings.ToLower(key)) {
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	options.ApplyUserAgent(req)
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Apply the selected provider's eligible overrides after content-type and
	// User-Agent preparation; the gateway's OpenAI OAuth records have no override map.
	options.OverrideHeaders(req.Header)
	return req, nil
}

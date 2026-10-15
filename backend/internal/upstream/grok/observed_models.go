package grok

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// observed_models.go fetches advisory Grok model IDs and deduplicates the
// returned names for catalogue callers.
// Model observations are advisory: a nil response or HTTP error status
// produces no model IDs, while transport and body-read errors remain errors.
// OAuth CLI targets receive subscription identity headers, and response IDs
// are deduplicated in encounter order before callers publish a catalogue.

// ModelsRequest supplies endpoint credentials and request hooks for Grok
// model observations.
type ModelsRequest struct {
	// URL is the validated model endpoint URL. Token is the upstream bearer
	// access token. UserID is the upstream user identifier. Email is the upstream
	// account email text. URL validation precedes this request; UserID and Email
	// are attached only for OAuth requests to the recognized CLI proxy target.
	URL, Token, UserID, Email string // model request identity values
	OAuth                     bool   // use OAuth request identity
	// ApplyOverrides is optional and runs before OAuth CLI proxy headers, whose
	// fixed identity values can replace the callback's values on that host.
	ApplyOverrides func(http.Header) // provider header override callback
	// Do is required and receives the caller-context request. A nil response
	// with nil error is accepted as an empty optional catalogue observation.
	Do func(*http.Request) (*http.Response, error) // model request transport callback
}

// FetchObservedModels reads at most one MiB of a model-list response and
// closes its body. Supply a validated URL and nonnil Do callback returning a
// body for nonnil responses. Transport/read errors propagate; a nil response
// or HTTP status at least 400 returns no observation.
//
// OAuth CLI-target user identity is installed before optional provider
// overrides, and ApplyCLIProxyHeaders runs afterward for OAuth requests.
// Consequently CLI proxy identity wins over overrides for its fixed client
// headers, while other request headers keep the override callback's result.
func FetchObservedModels(ctx context.Context, o ModelsRequest) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DefaultGrokUpstreamUserAgent())
	if o.OAuth && (MediaCodec{}).IsGrokCLIProxyTarget(req.URL.String()) {
		ApplyCLIHeaders(req.Header)
		if v := strings.TrimSpace(o.UserID); v != "" {
			req.Header.Set("X-UserID", v)
		}
		if v := strings.TrimSpace(o.Email); v != "" {
			req.Header.Set("X-Email", v)
		}
	}
	if o.ApplyOverrides != nil {
		o.ApplyOverrides(req.Header)
	}
	if o.OAuth {
		ApplyCLIProxyHeaders(req)
	}
	resp, err := o.Do(req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	defer func() { _ = resp.Body.Close() }()
	// Model observation needs bounded metadata, so reading stops at one MiB;
	// an oversized payload can remain incomplete and yield no usable model array.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	// A rejected model endpoint supplies no catalogue observation. Provider callers
	// can retain their existing model data while this optional request yields none.
	if resp.StatusCode >= 400 {
		return nil, nil
	}
	return ExtractModelIDs(body), nil
}

// ExtractModelIDs reads model IDs from a data array, otherwise iterates the
// root JSON value. Each item prefers its trimmed id field, then gjson string
// conversion. Blank values are skipped and exact duplicates keep their first
// encounter order.
func ExtractModelIDs(body []byte) []string {
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		// Compatible responses can put the model collection at the root. Select that
		// representation when data is absent or is not an array before extracting IDs.
		data = gjson.ParseBytes(body)
	}
	seen := make(map[string]struct{})
	var out []string
	data.ForEach(func(_, v gjson.Result) bool {
		id := strings.TrimSpace(v.Get("id").String())
		// When id supplies no text, use the item's string representation so scalar
		// entries can carry a model ID in the alternate collection shape.
		if id == "" {
			id = strings.TrimSpace(v.String())
		}
		if id == "" {
			return true
		}
		if _, ok := seen[id]; ok {
			return true
		}
		seen[id] = struct{}{}
		out = append(out, id)
		return true
	})
	return out
}

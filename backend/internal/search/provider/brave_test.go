package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// Brave request tests route the shared endpoint to a local HTTP server and
// inspect authentication, query parameters, and decoded results. Endpoint
// replacement is process-wide, so these scenarios run serially and restore the
// prior URL before another Brave case uses it.

// TestBraveProvider_Name constructs an adapter with the default client and a
// fixture key. It reports the Brave identifier without making a supplier HTTP
// request or testing credentials.
func TestBraveProvider_Name(t *testing.T) {
	p := NewBraveProvider("key", nil)
	require.Equal(t, "brave", p.Name())
}

// TestBraveProvider_Search_Success serves three local Brave results after
// inspecting the token, Accept header, query, and count. The client retains
// result count, first URL, snippet, and age.
func TestBraveProvider_Search_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-key", r.Header.Get("X-Subscription-Token"))
		require.Equal(t, "application/json", r.Header.Get("Accept"))
		require.Equal(t, "golang", r.URL.Query().Get("q"))
		require.Equal(t, "3", r.URL.Query().Get("count"))

		resp := braveResponse{}
		resp.Web.Results = []braveResult{
			{URL: "https://go.dev", Title: "Go", Description: "Go lang", Age: "1 day"},
			{URL: "https://pkg.go.dev", Title: "Pkg", Description: "Packages"},
			{URL: "https://tour.go.dev", Title: "Tour", Description: "A Tour of Go", Age: "3 days"},
		}
		w.Header().Set("Content-Type", "application/json")
		// Suppress response encoding errors for this encodable supplier fixture; the
		// outer search and decoded-result assertions expose a failed response
		// transfer.
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewBraveProvider("test-key", srv.Client())
	// Replace and later restore the shared endpoint for this serial case; other
	// Brave searches would otherwise observe the temporary local-server address.
	origURL := *braveSearchURL
	// Use the known-valid local server URL to obtain its parsed address; this
	// fixture suppresses the request-construction error and then uses that URL.
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	resp, err := p.Search(context.Background(), SearchRequest{Query: "golang", MaxResults: 3})
	require.NoError(t, err)
	require.Len(t, resp.Results, 3)
	require.Equal(t, "https://go.dev", resp.Results[0].URL)
	require.Equal(t, "Go lang", resp.Results[0].Snippet)
	require.Equal(t, "1 day", resp.Results[0].PageAge)
}

// TestBraveProvider_Search_DefaultMaxResults sends a zero requested count to
// the local server. The server observes five; this case ignores the search
// result and error while asserting the outgoing parameter.
func TestBraveProvider_Search_DefaultMaxResults(t *testing.T) {
	var receivedCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCount = r.URL.Query().Get("count")
		resp := braveResponse{}
		// Default-count capture precedes response encoding, so an ignored encoder
		// error does not prevent checking the outgoing parameter observed by this
		// fixture.
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewBraveProvider("key", srv.Client())
	// Replace and later restore the shared endpoint for this serial case; other
	// Brave searches would otherwise observe the temporary local-server address.
	origURL := *braveSearchURL
	// Use the known-valid local server URL to obtain its parsed address; this
	// fixture suppresses the request-construction error and then uses that URL.
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	// Ignore the search outcome because this scenario observes only the outgoing
	// default count already captured by the local request handler.
	_, _ = p.Search(context.Background(), SearchRequest{Query: "test", MaxResults: 0})
	require.Equal(t, "5", receivedCount)
}

// TestBraveProvider_Search_HTTPError returns a local rate-limit status with
// diagnostic text. Search exposes the supplier status prefix containing 429;
// the exact body excerpt is outside this assertion.
func TestBraveProvider_Search_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		// Supply diagnostic content while suppressing write errors; this test
		// protects the 429 status prefix rather than the returned excerpt bytes.
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	p := NewBraveProvider("key", srv.Client())
	// Replace and later restore the shared endpoint for this serial case; other
	// Brave searches would otherwise observe the temporary local-server address.
	origURL := *braveSearchURL
	// Use the known-valid local server URL to obtain its parsed address; this
	// fixture suppresses the request-construction error and then uses that URL.
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	_, err := p.Search(context.Background(), SearchRequest{Query: "test"})
	require.ErrorContains(t, err, "brave: status 429")
}

// TestBraveProvider_Search_InvalidJSON serves malformed response content under
// a success status. Search reports a decode-response error, protecting response
// parsing failure before any normalized result can be returned.
func TestBraveProvider_Search_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Send malformed fixture content while suppressing write errors; client
		// assertions protect a decode failure rather than exact received body bytes.
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	p := NewBraveProvider("key", srv.Client())
	// Replace and later restore the shared endpoint for this serial case; other
	// Brave searches would otherwise observe the temporary local-server address.
	origURL := *braveSearchURL
	// Use the known-valid local server URL to obtain its parsed address; this
	// fixture suppresses the request-construction error and then uses that URL.
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	_, err := p.Search(context.Background(), SearchRequest{Query: "test"})
	require.ErrorContains(t, err, "brave: decode response")
}

// TestBraveProvider_Search_EmptyResults serves an encodable response with no
// web results. Search succeeds and returns an empty normalized result
// collection, distinguishing empty results from supplier or decode errors.
func TestBraveProvider_Search_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := braveResponse{}
		// Suppress encoding errors for the empty supplier fixture; the client success
		// and empty-result assertions depend on receiving and decoding this response.
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewBraveProvider("key", srv.Client())
	// Replace and later restore the shared endpoint for this serial case; other
	// Brave searches would otherwise observe the temporary local-server address.
	origURL := *braveSearchURL
	// Use the known-valid local server URL to obtain its parsed address; this
	// fixture suppresses the request-construction error and then uses that URL.
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	resp, err := p.Search(context.Background(), SearchRequest{Query: "test"})
	require.NoError(t, err)
	require.Empty(t, resp.Results)
}

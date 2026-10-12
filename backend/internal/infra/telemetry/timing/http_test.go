package timing

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// HTTP timing tests use callbacks returning controlled responses so
// instrumentation can be checked without remote requests. The body tracks
// whether it was read, context labels protect hostname privacy, and
// pointer/type assertions establish shallow client copying and wrapper reuse.

// roundTripFunc adapts a test-owned callback to the outbound HTTP transport surface.
type roundTripFunc func(*http.Request) (*http.Response, error)

// trackingBody records whether response instrumentation attempted a body read.
type trackingBody struct {
	read bool // body read was attempted
}

// RoundTrip delegates the test request to its configured fixture callback.
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Read marks the fixture body as accessed and immediately returns EOF with no
// bytes. Tests inspect the flag after response-header instrumentation to
// detect an unwanted attempt to consume the body.
func (b *trackingBody) Read(_ []byte) (int, error) {
	b.read = true
	return 0, io.EOF
}

// Close completes fixture body cleanup successfully without changing its read
// flag.
func (b *trackingBody) Close() error {
	return nil
}

// TestWrapRoundTripperRecordsResponseHeaderLatency returns a body that
// records attempted reads from a fake transport. It requires dependency
// metrics, delegation, and an unread body when the timing wrapper returns
// response headers.
func TestWrapRoundTripperRecordsResponseHeaderLatency(t *testing.T) {
	startedAt := time.Now()
	collector := New(startedAt)
	body := &trackingBody{}
	baseCalled := false
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		baseCalled = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	req, err := http.NewRequestWithContext(WithCollector(context.Background(), collector), http.MethodGet, "https://api.github.com/repos/example/project", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := WrapRoundTripper(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if !baseCalled {
		t.Fatal("base RoundTripper was not called")
	}
	if body.read {
		t.Fatal("RoundTripper instrumentation read the response body; timing must stop at response headers")
	}
	header := collector.HeaderValue(time.Now(), "bypass")
	if !strings.Contains(header, `dep_github;dur=`) || !strings.Contains(header, `deps;dur=`) {
		t.Fatalf("dependency metrics missing from header: %q", header)
	}
}

// TestWrapRoundTripperUsesContextModuleOverride supplies a context module for
// a private-host request. It requires the sanitized override metric and
// absence of the raw hostname in the header text.
func TestWrapRoundTripperUsesContextModuleOverride(t *testing.T) {
	collector := New(time.Now())
	ctx := WithDependencyModule(WithCollector(context.Background(), collector), "data-managementd")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://private.example.test/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
	})

	if _, err := WrapRoundTripper(base).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	header := collector.HeaderValue(time.Now(), "bypass")
	if !strings.Contains(header, "dep_data_managementd") {
		t.Fatalf("module override missing from header: %q", header)
	}
	if strings.Contains(header, "private.example") {
		t.Fatalf("raw host leaked into header: %q", header)
	}
}

// TestWrapRoundTripperSkipsInactiveContext sends a request with no collector
// through the wrapper. It requires the supplied base callback to run
// normally, protecting HTTP execution when timing is inactive.
func TestWrapRoundTripperSkipsInactiveContext(t *testing.T) {
	called := false
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
	})
	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WrapRoundTripper(base).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("inactive request did not reach base RoundTripper")
	}
}

// TestDoRecordsWithoutChangingTransportType invokes Do around a client with a
// callback transport. It requires the original concrete transport type and an
// OpenAI dependency metric after the response returns.
func TestDoRecordsWithoutChangingTransportType(t *testing.T) {
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
	})
	client := &http.Client{Transport: base}
	collector := New(time.Now())
	req, err := http.NewRequestWithContext(WithCollector(context.Background(), collector), http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Do(client, req); err != nil {
		t.Fatal(err)
	}
	if _, ok := client.Transport.(roundTripFunc); !ok {
		t.Fatalf("Do changed client transport type to %T", client.Transport)
	}
	if header := collector.HeaderValue(time.Now(), "bypass"); !strings.Contains(header, "dep_openai;dur=") {
		t.Fatalf("dependency metric missing from header: %q", header)
	}
}

// TestDependencyModuleClassification supplies representative service
// hostnames and an unknown host in request URLs. It requires the
// corresponding fixed dependency labels, protecting the ordered hostname
// classification used for timing metrics.
func TestDependencyModuleClassification(t *testing.T) {
	tests := map[string]string{
		"https://api.github.com/repos/a/b":                    "github",
		"https://api.openai.com/v1/models":                    "openai",
		"https://api.anthropic.com/v1/messages":               "anthropic",
		"https://generativelanguage.googleapis.com/v1/models": "gemini",
		"https://cloudcode-pa.googleapis.com/v1internal":      "antigravity",
		"https://storage.googleapis.com/bucket/object":        "google",
		"https://bucket.s3.amazonaws.com/object":              "s3",
		"https://api.stripe.com/v1/refunds":                   "payment",
		"https://dependency.example.test/path":                "http",
	}
	for rawURL, want := range tests {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q): %v", rawURL, err)
		}
		if got := dependencyModule(req); got != want {
			t.Errorf("dependencyModule(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

// TestClientInstrumentationDoesNotMutateOriginal instruments a client with a
// fixed timeout and callback transport. It requires a distinct client,
// retained original transport type and timeout, and reuse of an already
// installed timing wrapper.
func TestClientInstrumentationDoesNotMutateOriginal(t *testing.T) {
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
	})
	original := &http.Client{Transport: base, Timeout: time.Second}
	instrumented := InstrumentClient(original)
	if instrumented == original {
		t.Fatal("InstrumentClient returned the original client")
	}
	if _, ok := original.Transport.(roundTripFunc); !ok {
		t.Fatalf("InstrumentClient mutated the original transport to %T", original.Transport)
	}
	if instrumented.Timeout != original.Timeout {
		t.Fatal("InstrumentClient did not preserve client settings")
	}
	if WrapRoundTripper(instrumented.Transport) != instrumented.Transport {
		t.Fatal("WrapRoundTripper wrapped an already instrumented transport twice")
	}
}

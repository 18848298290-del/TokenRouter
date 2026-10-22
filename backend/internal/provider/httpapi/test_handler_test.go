package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Structured decision input must reach the provider test use case unchanged,
// while malformed JSON must fail before target loading. test_handler.go is
// exercised with a serial Jev target fixture that emits successful completion
// and records the admitted request.

// decisionTestLoader records serial HTTP request binding and emits a successful
// Jev test target.
type decisionTestLoader struct {
	// request is a shallow capture of admitted input. Its SystemOne bytes and
	// Type pointer remain shared, and this serial fixture keeps them stable through
	// the later binding assertions.
	request provider.TestRequest // last admitted test request
	loads   int                  // serial target-loading invocation count
}

// LoadTestTarget records the supplied request and returns this fixture as its
// target. Its serial counter lets tests detect malformed input rejected before
// any target preparation.
func (l *decisionTestLoader) LoadTestTarget(_ context.Context, request provider.TestRequest) (provider.TestTarget, error) {
	l.loads++
	l.request = request
	return l, nil
}

// Information returns the fixed Jev API-key target identity for decision
// request preparation.
func (l *decisionTestLoader) Information() provider.TestTargetInfo {
	return provider.TestTargetInfo{ProviderSnapshot: provider.ProviderSnapshot{Platform: provider.PlatformJev, Type: provider.ProviderTypeAPIKey}}
}

// Execute commits the stream and emits a successful terminal fixture event.
// Preparation and write errors propagate, while this target performs no
// upstream work or decision calculation.
func (l *decisionTestLoader) Execute(ctx context.Context, _ provider.PreparedTestRequest, sink provider.TestEventSink) error {
	if err := sink.Begin(ctx, true); err != nil {
		return err
	}
	return sink.Emit(ctx, provider.TestEvent{Type: "test_complete", Success: true})
}

// TestTestHandlerDecisionJSON submits structured decision input, an empty body
// and malformed JSON to the HTTP test route. Valid input reaches target loading
// with its exact structured payload, while malformed input stops before
// loading.
func TestTestHandlerDecisionJSON(t *testing.T) {
	for _, tc := range []struct {
		name, body string // name: request scenario description; body: HTTP request JSON text
		status     int    // expected HTTP response status
		loads      int    // serial target-loading invocation count
	}{
		{"decision", `{"group_id":59,"model_id":"jev-latest","test_type":"decision","systemone":{"state":["ready"],"questions":{"x":{"type":"noul","instructions":"Ready?"}}}}`, http.StatusOK, 1},
		{"empty probe", ``, http.StatusOK, 1},
		{"invalid", `{"systemone":`, http.StatusBadRequest, 0},
		{"negative group", `{"group_id":-1}`, http.StatusBadRequest, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loader := &decisionTestLoader{}
			router := gin.New()
			router.POST("/providers/:id/test", NewTestHandler(provider.NewTestService(loader, provider.TestOptions{}), nil).Test)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/providers/42/test", strings.NewReader(tc.body)))
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.loads, loader.loads)
			if tc.name == "decision" {
				require.Equal(t, int64(42), loader.request.ProviderID)
				require.Equal(t, int64(59), loader.request.GroupID)
				require.Equal(t, "decision", *loader.request.Type)
				require.JSONEq(t, `{"state":["ready"],"questions":{"x":{"type":"noul","instructions":"Ready?"}}}`, string(loader.request.SystemOne))
			}
		})
	}
}

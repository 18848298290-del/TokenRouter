package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// shutdownBody detects body reads after the activity barrier rejects admission.
type shutdownBody struct {
	t *testing.T // owning rejection test
}

// Read fails immediately if a rejected request reaches body consumption.
func (b shutdownBody) Read([]byte) (int, error) {
	b.t.Fatal("stopped request read body")
	return 0, io.EOF
}

// Close satisfies the request body interface without additional fixture
// cleanup.
func (shutdownBody) Close() error { return nil }

// TestRequestLifetimeRejectsBeforeBodyAndDependencies installs a stopped
// activity callback on the token handler. It requires the Anthropic 503
// envelope while a body-read trap protects early rejection before request
// parsing.
func TestRequestLifetimeRejectsBeforeBodyAndDependencies(t *testing.T) {
	h := NewCountTokensHandler(1024, 1, nil, nil)
	h.BindRequestActivity(func() (func(), error) { return nil, errors.New("stopped") })
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Body = shutdownBody{t}
	h.CountTokens(c)
	require.Equal(t, 503, rec.Code)
	require.JSONEq(t, `{"type":"error","error":{"type":"api_error","message":"Service is shutting down"}}`, rec.Body.String())
}

// TestRequestLifetimeUsesSharedOwnerWithoutNewState supplies an application-
// style counter and release callback. Beginning a request increments that
// counter, and invoking the returned release restores it to zero.
func TestRequestLifetimeUsesSharedOwnerWithoutNewState(t *testing.T) {
	active := 0
	scope := RequestLifetime{}
	scope.BindRequestActivity(func() (func(), error) { active++; return func() { active-- }, nil })
	done, accepted := scope.BeginRequest(nil, "openai")
	require.True(t, accepted)
	require.Equal(t, 1, active)
	done()
	require.Zero(t, active)
}

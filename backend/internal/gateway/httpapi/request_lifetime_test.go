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

// shutdownBody fails the test if a rejected request reads its body.
type shutdownBody struct{ t *testing.T }

func (b shutdownBody) Read([]byte) (int, error) {
	b.t.Fatal("stopped request read body")
	return 0, io.EOF
}
func (shutdownBody) Close() error { return nil }
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

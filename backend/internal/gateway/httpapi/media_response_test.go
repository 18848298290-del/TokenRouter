package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// mediaReadFailure supplies a download body that immediately fails on
// reading.
type mediaReadFailure struct{}

// TestMediaContentResponsePreservesRangeAndCommit presents a partial video
// response with a private upstream header. Range and length survive, the
// private header is omitted, and the commit callback runs.
func TestMediaContentResponsePreservesRangeAndCommit(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	committed := false
	response := &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{"Content-Range": []string{"bytes 0-3/20"}, "X-Secret": []string{"hidden"}}, ContentLength: 4, Body: io.NopCloser(strings.NewReader("data"))}
	require.NoError(t, WriteGrokMediaContentResponse(c, response, func() { committed = true }))
	require.True(t, committed)
	require.Equal(t, 206, recorder.Code)
	require.Equal(t, "data", recorder.Body.String())
	require.Equal(t, "bytes 0-3/20", recorder.Header().Get("Content-Range"))
	require.Equal(t, "4", recorder.Header().Get("Content-Length"))
	require.Equal(t, "application/octet-stream", recorder.Header().Get("Content-Type"))
	require.Empty(t, recorder.Header().Get("X-Secret"))
}

// Read returns a fixed download error before accepting any bytes.
func (mediaReadFailure) Read([]byte) (int, error) { return 0, errors.New("download failed") }

// Close finishes the stateless failing body without another error.
func (mediaReadFailure) Close() error { return nil }

// TestMediaContentReadFailureRemainsVisible copies a video body that fails
// immediately. The download writer returns that read error to its caller for
// terminal handling.
func TestMediaContentReadFailureRemainsVisible(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := WriteGrokMediaContentResponse(c, &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Header: make(http.Header), Body: mediaReadFailure{}}, nil)
	require.ErrorContains(t, err, "download failed")
}

// TestVoiceMissingBodyPreservesMethodDifference reads requests whose body
// pointer is absent. GET returns empty input successfully, while POST reports
// that a request body is required.
func TestVoiceMissingBodyPreservesMethodDifference(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = &http.Request{Method: http.MethodGet}
	body, err := ReadGrokVoiceBody(c)
	require.NoError(t, err)
	require.Nil(t, body)
	c.Request.Method = http.MethodPost
	_, err = ReadGrokVoiceBody(c)
	require.ErrorContains(t, err, "request body is required")
}

// TestIsExpectedGrokRealtimeClose supplies ordinary departure codes and a
// policy violation from Realtime relay. The departure codes qualify for
// completion, while the policy violation does not.
func TestIsExpectedGrokRealtimeClose(t *testing.T) {
	for _, status := range []coderws.StatusCode{
		coderws.StatusNormalClosure,
		coderws.StatusGoingAway,
		coderws.StatusNoStatusRcvd,
		coderws.StatusAbnormalClosure,
	} {
		if !IsExpectedGrokRealtimeClose(coderws.CloseError{Code: status}) {
			t.Fatalf("status %v should be treated as an expected session close", status)
		}
	}
	if IsExpectedGrokRealtimeClose(coderws.CloseError{Code: coderws.StatusPolicyViolation}) {
		t.Fatal("policy violations must not be treated as billable normal closes")
	}
}

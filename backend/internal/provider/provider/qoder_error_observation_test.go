package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Qoder's agent-limit deadline can arrive as milliseconds inside a nested SSE
// error. These cases exercise qoder_error_observation.go with direct and parsed
// API errors, and check that health state still receives a live context after
// the originating request has already been cancelled.

// qoderRateLimitRepoStub records the last provider limit written by a serial test.
type qoderRateLimitRepoStub struct {
	rateLimitedID int64     // last written provider ID
	resetAt       time.Time // last written reset deadline
}

// qoderPolicyContextRepoStub also records cancellation state at the
// health-write call.
type qoderPolicyContextRepoStub struct {
	qoderRateLimitRepoStub       // embedded limit-write recorder
	rateLimitCtxErr        error // context error at write
}

// SetRateLimited records the provider identity and reset deadline for later
// assertions.
func (r *qoderRateLimitRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitedID = id
	r.resetAt = resetAt
	return nil
}

// SetRateLimited records the call's cancellation state before storing its
// limit deadline.
func (r *qoderPolicyContextRepoStub) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	r.rateLimitCtxErr = ctx.Err()
	return r.qoderRateLimitRepoStub.SetRateLimited(ctx, id, resetAt)
}

// TestQoderGatewayAgentLimitSetsRateLimitedUntilReset supplies an agent-limit API
// error with a millisecond reset timestamp. It checks the written provider ID
// and exact reset milliseconds retained by health observation.
func TestQoderGatewayAgentLimitSetsRateLimitedUntilReset(t *testing.T) {
	repo := &qoderRateLimitRepoStub{}
	providerID := int64(77)
	err := &qoder.APIError{
		StatusCode:          http.StatusTooManyRequests,
		Code:                "115",
		AgentLimitResetTime: 1783841289162,
	}

	ObserveQoderUpstreamError(context.Background(), providerID, repo, err)

	require.Equal(t, int64(77), repo.rateLimitedID)
	require.Equal(t, int64(1783841289162), repo.resetAt.UnixMilli())
}

// TestQoderGatewayUpstreamErrorPolicyIgnoresCanceledRequestContext observes a 429
// after the request is cancelled. It checks the provider limit write and a
// live state context at the repository call.
func TestQoderGatewayUpstreamErrorPolicyIgnoresCanceledRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &qoderPolicyContextRepoStub{}

	ObserveQoderUpstreamError(ctx, 79, repo, &qoder.APIError{StatusCode: http.StatusTooManyRequests})

	require.Equal(t, int64(79), repo.rateLimitedID)
	require.NoError(t, repo.rateLimitCtxErr)
}

// TestQoderGatewayNonStreamingSSEAgentLimitSetsRateLimited parses a nested agent
// error from an HTTP 200 stream. It checks the empty event result, returned
// failure, and exact provider reset passed to health observation.
func TestQoderGatewayNonStreamingSSEAgentLimitSetsRateLimited(t *testing.T) {
	providerID := int64(78)
	repo := &qoderRateLimitRepoStub{}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(bytes.NewBufferString(
			"data: {\"body\":\"{\\\"code\\\":\\\"115\\\",\\\"message\\\":\\\"{\\\\\\\"agentLimitResetTime\\\\\\\":1783841289162}\\\"}\",\"statusCodeValue\":429,\"statusCode\":\"TOO_MANY_REQUESTS\"}\n\n",
		)),
	}

	events, err := qoder.ReadQoderSSEEventsContext(context.Background(), resp, nil)
	if err != nil {
		ObserveQoderUpstreamError(context.Background(), providerID, repo, err)
	}

	require.Error(t, err)
	require.Empty(t, events)
	require.Equal(t, int64(78), repo.rateLimitedID)
	require.Equal(t, int64(1783841289162), repo.resetAt.UnixMilli())
}

// SetOverloaded panics because these rate-limit cases expect no overload write.
// The panic exposes an unexpected classifier branch at the fixture call site,
// rather than allowing that write to pass unnoticed.
func (r *qoderRateLimitRepoStub) SetOverloaded(context.Context, int64, time.Time) error {
	panic("unexpected SetOverloaded call")
}

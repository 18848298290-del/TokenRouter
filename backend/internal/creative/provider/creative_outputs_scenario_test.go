package provider

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// One-image delivery normalizes candidate byte content before the worker
// consumes it. These scenarios compare retained bytes and indexes, reject an
// oversized candidate, and independently check the listed HTTP status classes
// and generic errors used for retry decisions. The scenarios cover
// creative/execution_values.go normalization and creative/execution_error.go
// retry classification.

// TestNormalizeCreativeOutputs supplies duplicate bytes, distinct images,
// oversized bytes, and an empty list. It checks the single retained image and
// zero index, plus failure and retry classification for oversized output.
func TestNormalizeCreativeOutputs(t *testing.T) {
	// Equal SHA-256 hashes identify duplicate image bytes.
	outputs, err := creative.NormalizeCreativeOutputs([]creative.CreativeOutput{
		{Index: 0, Bytes: []byte("same"), Mime: "image/png"},
		{Index: 1, Bytes: []byte("same"), Mime: "image/png"},
		{Index: 2, Bytes: []byte("other"), Mime: "image/png"},
	})
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, []byte("same"), outputs[0].Bytes)

	// Multiple upstream images are reduced to one with index zero.
	outputs, err = creative.NormalizeCreativeOutputs([]creative.CreativeOutput{
		{Index: 0, Bytes: []byte("a"), Mime: "image/png"},
		{Index: 1, Bytes: []byte("b"), Mime: "image/png"},
		{Index: 2, Bytes: []byte("c"), Mime: "image/png"},
	})
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, 0, outputs[0].Index)

	// Allocate one byte beyond the execution image cap so this case checks
	// rejection at the limit, independently of decoding or provider response syntax.
	big := make([]byte, creative.CreativeMaxOutputBytes+1)
	_, err = creative.NormalizeCreativeOutputs([]creative.CreativeOutput{{Index: 0, Bytes: big, Mime: "image/png"}})
	require.Error(t, err)
	require.False(t, creative.IsRetryableCreativeError(err))

	// Empty output fails normalization.
	_, err = creative.NormalizeCreativeOutputs(nil)
	require.Error(t, err)
}

// TestCreativeErrorRetryableMatrix constructs errors for the listed
// connection, rate-limit, server, and client statuses. It checks their retry
// booleans together with nil, generic, and explicitly nonretryable errors.
func TestCreativeErrorRetryableMatrix(t *testing.T) {
	retryable := []int{0, 429, 500, 502, 503}
	for _, status := range retryable {
		err := creative.CreativeHTTPStatusError(status, "boom")
		require.True(t, creative.IsRetryableCreativeError(err), "status %d 应当可重试", status)
	}
	nonRetryable := []int{400, 401, 403, 404, 422}
	for _, status := range nonRetryable {
		err := creative.CreativeHTTPStatusError(status, "bad request")
		require.False(t, creative.IsRetryableCreativeError(err), "status %d 应当不可重试", status)
	}
	require.False(t, creative.IsRetryableCreativeError(nil))
	require.True(t, creative.IsRetryableCreativeError(errors.New("network down")))
	require.True(t, creative.IsRetryableCreativeError(creative.CreativeNonRetryableError("x")) == false)
}

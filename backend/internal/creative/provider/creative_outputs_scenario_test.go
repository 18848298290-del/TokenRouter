package provider

// These scenarios cover output normalization in ../execution_values.go and retry classification in ../execution_error.go.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// TestNormalizeCreativeOutputs checks size limits, duplicate bytes, and the single-image result.
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

	// An image larger than 32 MiB fails normalization.
	big := make([]byte, creative.CreativeMaxOutputBytes+1)
	_, err = creative.NormalizeCreativeOutputs([]creative.CreativeOutput{{Index: 0, Bytes: big, Mime: "image/png"}})
	require.Error(t, err)
	require.False(t, creative.IsRetryableCreativeError(err))

	// Empty output fails normalization.
	_, err = creative.NormalizeCreativeOutputs(nil)
	require.Error(t, err)
}

// TestCreativeErrorRetryableMatrix checks HTTP status retry classification.
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

package openai

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLenientJSONLimitAndOriginalBytes checks the expanded size limit, input ownership, and error wrapping.
func TestLenientJSONLimitAndOriginalBytes(t *testing.T) {
	original := []byte{'{', '"', 'v', '"', ':', '"', '\n', '"', '}'}
	saved := append([]byte(nil), original...)
	normalized, err := NormalizeLenientJSONRequestBody(original, 14)
	require.NoError(t, err)
	require.Equal(t, `{"v":"\u000a"}`, string(normalized))
	require.Equal(t, saved, original)

	_, err = NormalizeLenientJSONRequestBody(original, 13)
	var exceeded *BodyLimitError
	require.True(t, errors.As(fmt.Errorf("request: %w", err), &exceeded))
	require.Equal(t, int64(13), exceeded.Limit)
}

package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestOpenAICompatibleRequestPlatformStaysUnspecifiedBeforeSelection checks that candidates may span platforms.
func TestOpenAICompatibleRequestPlatformStaysUnspecifiedBeforeSelection(t *testing.T) {
	require.Empty(t, OpenAICompatibleRequestPlatform(nil))
	require.Empty(t, OpenAICompatibleRequestPlatform(&apikey.APIKey{Group: &routing.Group{}}))
}

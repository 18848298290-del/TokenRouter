package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestOpenAICompatibleRequestPlatformStaysUnspecifiedBeforeSelection supplies
// absent and present API Key group data. Both produce an unspecified platform
// so provider capability selection remains responsible for the candidate
// platform.
func TestOpenAICompatibleRequestPlatformStaysUnspecifiedBeforeSelection(t *testing.T) {
	require.Empty(t, OpenAICompatibleRequestPlatform(nil))
	require.Empty(t, OpenAICompatibleRequestPlatform(&apikey.APIKey{Group: &routing.Group{}}))
}

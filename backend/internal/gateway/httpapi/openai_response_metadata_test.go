package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestOpenAIGatewayService_BindHTTPResponseProvider stores one response
// provider and HTTP owner in the fixture state store. It checks affinity,
// same-user access across keys, different-user denial and denial of an unknown
// response.
func TestOpenAIGatewayService_BindHTTPResponseProvider(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	groupID := int64(4201)
	c.Set("api_key", &apikey.APIKey{ID: 501, GroupID: &groupID})
	SetHTTPResponseOwner(c, 601, 501)

	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 37001, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	svc.Output.BindResponseProvider(context.Background(), c, provider, "resp_http_001")

	got, err := svc.Lineage.Store.GetResponseProvider(context.Background(), groupID, "resp_http_001")
	require.NoError(t, err)
	require.Equal(t, provider.Record.ID, got)

	owned, err := session.ValidateHTTPResponseOwner(context.Background(), func() session.HTTPResponseOwnerReader { return svc.Lineage.Store }, groupID, "resp_http_001", 601, 501)
	require.NoError(t, err)
	require.True(t, owned)

	owned, err = session.ValidateHTTPResponseOwner(context.Background(), func() session.HTTPResponseOwnerReader { return svc.Lineage.Store }, groupID, "resp_http_001", 601, 502)
	require.NoError(t, err)
	require.True(t, owned, "API keys owned by the same downstream user remain interoperable")

	owned, err = session.ValidateHTTPResponseOwner(context.Background(), func() session.HTTPResponseOwnerReader { return svc.Lineage.Store }, groupID, "resp_http_001", 602, 501)
	require.NoError(t, err)
	require.False(t, owned)

	owned, err = session.ValidateHTTPResponseOwner(context.Background(), func() session.HTTPResponseOwnerReader { return svc.Lineage.Store }, groupID, "resp_unknown", 601, 501)
	require.NoError(t, err)
	require.False(t, owned)
}

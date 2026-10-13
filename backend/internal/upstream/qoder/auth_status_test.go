package qoder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// Gateway identity fixtures inspect the decoded inner JSON and the outer
// encodeVersion envelope. They distinguish status responses that omit existing
// request tokens from legacy CN refresh responses that replace them, keeping
// protocol shape and token-selection assertions separate.

// decodeAuthStatusRequest decodes a captured Gateway body and asserts its
// expected envelope fields. It requires encodeVersion one, absent requestId, and
// valid inner parameters before returning both decoded objects to the fixture.
func decodeAuthStatusRequest(t *testing.T, r *http.Request) (gatewayHTTPPayload, AuthStatusParams) {
	t.Helper()
	decoded, err := DecodeString(readAllString(t, r))
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(decoded), &fields))
	require.ElementsMatch(t, []string{"payload", "encodeVersion"}, mapKeys(fields))

	var envelope gatewayHTTPPayload
	require.NoError(t, json.Unmarshal([]byte(decoded), &envelope))
	require.Equal(t, "1", envelope.EncodeVersion)
	require.Empty(t, envelope.RequestID)

	var params AuthStatusParams
	require.NoError(t, json.Unmarshal([]byte(envelope.Payload), &params))
	return envelope, params
}

// mapKeys returns raw JSON field names for unordered envelope-shape assertions.
func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// TestMarshalAuthStatusRequestUsesOfficialEnvelope marshals a status request
// with user and token fields. The JSON assertion protects the exact outer keys
// and the serialized empty token, refresh marker, and authInfo fields.
func TestMarshalAuthStatusRequestUsesOfficialEnvelope(t *testing.T) {
	body, err := marshalAuthStatusRequest(AuthStatusParams{
		UserID:             "uid-1",
		SecurityOauthToken: "access-1",
		RefreshToken:       "refresh-1",
	})
	require.NoError(t, err)
	require.JSONEq(t, `{
		"payload": "{\"userId\":\"uid-1\",\"personalToken\":\"\",\"securityOauthToken\":\"access-1\",\"refreshToken\":\"refresh-1\",\"needRefresh\":false,\"authInfo\":{}}",
		"encodeVersion": "1"
	}`, string(body))
}

// TestIdentityFromAuthStatusFallsBackToRequestToken supplies status identity
// without returned token fields. It checks that the request access and refresh
// tokens remain in the assembled authentication identity.
func TestIdentityFromAuthStatusFallsBackToRequestToken(t *testing.T) {
	identity, err := identityFromAuthStatus(&AuthStatusResult{
		ID:       "uid-1",
		UserType: "personal_standard",
	}, &AuthIdentity{
		SecurityOauthToken: "request-access",
		RefreshToken:       "request-refresh",
	})

	require.NoError(t, err)
	require.Equal(t, "request-access", identity.SecurityOauthToken)
	require.Equal(t, "request-refresh", identity.RefreshToken)
}

// TestRefreshCosySessionForProfileUsesGatewayRefreshPath captures the CN legacy
// refresh URL and encoded request. It checks user/organization/token inputs and
// the replacement token pair and user type from the wrapped response.
func TestRefreshCosySessionForProfileUsesGatewayRefreshPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/algo"+CosyRefreshTokenPath, r.URL.Path)
		require.Equal(t, "1", r.URL.Query().Get("Encode"))
		envelope, params := decodeAuthStatusRequest(t, r)
		require.JSONEq(t, `{
			"userId": "uid-1",
			"orgId": "org-1",
			"personalToken": "",
			"securityOauthToken": "old-token",
			"refreshToken": "old-refresh",
			"needRefresh": false,
			"authInfo": {}
		}`, envelope.Payload)
		require.Equal(t, "uid-1", params.UserID)
		require.Equal(t, "org-1", params.OrganizationID)
		require.Equal(t, "old-token", params.SecurityOauthToken)
		require.Equal(t, "old-refresh", params.RefreshToken)

		inner, err := json.Marshal(AuthStatusResult{
			ID:                 "uid-1",
			ProviderID:         "aid-1",
			OrganizationID:     "org-1",
			SecurityOauthToken: "new-token",
			RefreshToken:       "new-refresh",
			UserType:           "enterprise_standard",
		})
		require.NoError(t, err)
		// Send the fixture response after request assertions. A response-writer
		// error is left to the client operation, whose response or error is checked
		// by this test.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statusCodeValue": http.StatusOK,
			"body":            string(inner),
		})
	}))
	defer server.Close()

	profile := MustProfileForSite(SiteCN)
	profile.GatewayBaseURL = server.URL
	identity, err := RefreshCosySessionForProfileContext(
		context.Background(),
		profile,
		"old-refresh",
		"old-token",
		"uid-1",
		"org-1",
		&MachineIdentity{MachineID: "machine-1", MachineToken: "machine-token", MachineType: "machine-type"},
		server.Client().Do,
	)
	require.NoError(t, err)
	require.Equal(t, "new-token", identity.SecurityOauthToken)
	require.Equal(t, "new-refresh", identity.RefreshToken)
	require.Equal(t, "enterprise_standard", identity.UserType)
}

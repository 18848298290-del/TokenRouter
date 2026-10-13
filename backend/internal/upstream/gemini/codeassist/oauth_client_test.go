package codeassist

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

// forceHTTPVersion reads the private req transport override for the
// protocol-negotiation assertion. It fails the test if the pinned field is
// unavailable and uses reflection to access its current value.
func forceHTTPVersion(t *testing.T, client *req.Client) string {
	t.Helper()
	transport := client.GetTransport()
	field := reflect.ValueOf(transport).Elem().FieldByName("forceHttpVersion")
	require.True(t, field.IsValid(), "forceHttpVersion field not found")
	require.True(t, field.CanAddr(), "forceHttpVersion field not addressable")
	// Read the addressable private transport field from the pinned req version so
	// this assertion inspects actual negotiation configuration rather than a
	// public wrapper default.
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().String()
}

// TestCreateGeminiReqClient_ForceHTTP2Disabled constructs the OAuth req client
// with an HTTP proxy URL. It checks that the private transport override
// remains empty so configured protocol negotiation can select the HTTP
// version.
func TestCreateGeminiReqClient_ForceHTTP2Disabled(t *testing.T) {
	client, err := CreateOAuthReqClient("http://proxy.local:8080")
	require.NoError(t, err)
	require.Equal(t, "", forceHTTPVersion(t, client))
}

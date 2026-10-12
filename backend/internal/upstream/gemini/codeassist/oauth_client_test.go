package codeassist

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

// forceHTTPVersion reads the HTTP version override from the req transport.
func forceHTTPVersion(t *testing.T, client *req.Client) string {
	t.Helper()
	transport := client.GetTransport()
	field := reflect.ValueOf(transport).Elem().FieldByName("forceHttpVersion")
	require.True(t, field.IsValid(), "forceHttpVersion field not found")
	require.True(t, field.CanAddr(), "forceHttpVersion field not addressable")
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().String()
}

func TestCreateGeminiReqClient_ForceHTTP2Disabled(t *testing.T) {
	client, err := CreateOAuthReqClient("http://proxy.local:8080")
	require.NoError(t, err)
	require.Equal(t, "", forceHTTPVersion(t, client))
}

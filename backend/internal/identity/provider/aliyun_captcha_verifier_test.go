package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// newAliyunCaptchaTestTarget routes the real SDK to a local HTTP server with
// fixture credentials. The verifier uses a two-second wait budget, and test
// cleanup closes the server after requests complete.
func newAliyunCaptchaTestTarget(t *testing.T, handler http.HandlerFunc) (*aliyunCaptchaVerifier, identity.AliyunCaptchaCredentials) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	verifier := &aliyunCaptchaVerifier{protocol: "HTTP", timeoutMillis: 2_000}
	cred := identity.AliyunCaptchaCredentials{
		AccessKeyID:     "test-ak-id",
		AccessKeySecret: "test-ak-secret",
		SceneID:         "scene-1",
		Endpoint:        strings.TrimPrefix(server.URL, "http://"),
	}
	return verifier, cred
}

// TestAliyunCaptchaVerifier_VerifySuccess sends a challenge through the SDK to a
// local successful response. The result and captured form retain the verification
// parameter, scene identifier, and provider decision code.
func TestAliyunCaptchaVerifier_VerifySuccess(t *testing.T) {
	var capturedParam, capturedSceneID string
	verifier, cred := newAliyunCaptchaTestTarget(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		capturedParam = r.Form.Get("CaptchaVerifyParam")
		capturedSceneID = r.Form.Get("SceneId")
		w.Header().Set("Content-Type", "application/json")
		// Return a complete success envelope for SDK decoding. Write errors are
		// discarded; assertions on the returned decision and captured request expose the observed result.
		_, _ = w.Write([]byte(`{"Code":"Success","Message":"success","RequestId":"req-1","Success":true,"Result":{"VerifyResult":true,"VerifyCode":"T001"}}`))
	})

	result, err := verifier.VerifyCaptcha(context.Background(), cred, "the-verify-param")
	require.NoError(t, err)
	require.True(t, result.VerifyResult)
	require.Equal(t, "T001", result.VerifyCode)
	require.Equal(t, "the-verify-param", capturedParam)
	require.Equal(t, "scene-1", capturedSceneID)
}

// TestAliyunCaptchaVerifier_VerifyResultFalse returns a successful API envelope
// containing a rejected challenge. The adapter returns false and its decision code
// with a nil transport error for core interpretation.
func TestAliyunCaptchaVerifier_VerifyResultFalse(t *testing.T) {
	verifier, cred := newAliyunCaptchaTestTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Send the accepted API response containing a rejected challenge. The fixture
		// discards write errors and observes rejection through the adapter's decoded false result.
		_, _ = w.Write([]byte(`{"Code":"Success","RequestId":"req-2","Success":true,"Result":{"VerifyResult":false,"VerifyCode":"F002"}}`))
	})

	result, err := verifier.VerifyCaptcha(context.Background(), cred, "bad-param")
	require.NoError(t, err)
	require.False(t, result.VerifyResult)
	require.Equal(t, "F002", result.VerifyCode)
}

// TestAliyunCaptchaVerifier_APIErrorNormalized responds with an HTTP signature
// failure from the local SDK endpoint. The resulting error has the identity API
// error type and retains the provider's signature-error code.
func TestAliyunCaptchaVerifier_APIErrorNormalized(t *testing.T) {
	verifier, cred := newAliyunCaptchaTestTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		// Supply the provider error envelope after its failure status. Write errors
		// are discarded, and the test inspects the error type and code returned by the SDK adapter.
		_, _ = w.Write([]byte(`{"Code":"SignatureDoesNotMatch","Message":"Specified signature is not matched with our calculation.","RequestId":"req-3"}`))
	})

	_, err := verifier.VerifyCaptcha(context.Background(), cred, "param")
	require.Error(t, err)
	var apiErr *identity.AliyunCaptchaAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "SignatureDoesNotMatch", apiErr.Code)
}

// TestAliyunCaptchaVerifier_TransportError closes the local listener before the
// SDK request attempts to connect. The returned connection failure remains an
// error outside the identity API-error classification.
func TestAliyunCaptchaVerifier_TransportError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint := strings.TrimPrefix(server.URL, "http://")
	// Close the fixture listener before verification so the request fails during
	// transport setup and never obtains a provider JSON error response.
	server.Close()

	verifier := &aliyunCaptchaVerifier{protocol: "HTTP", timeoutMillis: 2_000}
	cred := identity.AliyunCaptchaCredentials{
		AccessKeyID:     "test-ak-id",
		AccessKeySecret: "test-ak-secret",
		SceneID:         "scene-1",
		Endpoint:        endpoint,
	}

	_, err := verifier.VerifyCaptcha(context.Background(), cred, "param")
	require.Error(t, err)
	var apiErr *identity.AliyunCaptchaAPIError
	require.False(t, errors.As(err, &apiErr), "transport errors must not be normalized to API errors")
}

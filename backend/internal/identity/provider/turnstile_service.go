package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// turnstile_service.go submits Turnstile challenge proofs and decodes
// verification responses for the identity service.
// Turnstile sends challenge decisions as JSON after a form-encoded verification
// request. The adapter returns that decision even when success is false; the
// identity service interprets rejection and secret-key probe results using the
// same transport implementation.

// turnstileVerifyURL selects the Cloudflare verification endpoint for adapters
// constructed with the production HTTP client and request settings.
const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// turnstileVerifier carries the HTTP transport and endpoint for Turnstile verification.
type turnstileVerifier struct {
	httpClient *http.Client // HTTP verification request transport
	verifyURL  string       // challenge verification endpoint URL
}

// NewTurnstileVerifier constructs an adapter with a ten-second HTTP budget and
// the Cloudflare verification endpoint. Shared-client setup failure selects a
// standard HTTP client; the constructor still returns a usable adapter.
func NewTurnstileVerifier() identity.TurnstileVerifier {
	sharedClient, err := httpclient.GetClient(httpclient.Options{
		Timeout:            10 * time.Second,
		ValidateResolvedIP: true,
	})
	if err != nil {
		// Continue verification after shared-client setup fails using the same timeout.
		// The replacement uses the default transport and omits the shared resolved-IP validator.
		sharedClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &turnstileVerifier{
		httpClient: sharedClient,
		verifyURL:  turnstileVerifyURL,
	}
}

// VerifyToken posts the supplied secret and challenge, adding remoteip when
// nonempty. It decodes the response body regardless of HTTP status and returns
// provider success and error codes for the identity service to interpret.
func (v *turnstileVerifier) VerifyToken(ctx context.Context, secretKey, token, remoteIP string) (*identity.TurnstileVerifyResponse, error) {
	formData := url.Values{}
	formData.Set("secret", secretKey)
	formData.Set("response", token)
	if remoteIP != "" {
		formData.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.verifyURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result identity.TurnstileVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

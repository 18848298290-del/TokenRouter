package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Personal access token import validates the at- prefix before calling the
// Codex whoami endpoint with canonical authentication identity. Successful
// metadata must contain user and account identifiers, email, plan type, and a
// present FedRAMP boolean, so false remains distinguishable from an omitted
// field.

// ValidatePersonalAccessToken validates a trimmed at- token against the
// caller-supplied whoami endpoint. The exchange has a twenty-second client
// timeout, uses proxyURL and ctx, and closes the response after status
// handling, bounded metadata decoding, and required-field validation.
func ValidatePersonalAccessToken(ctx context.Context, accessToken, proxyURL, endpoint string) (*wire.PATWhoamiResponse, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_PAT_REQUIRED", "access token is required")
	}
	if !strings.HasPrefix(accessToken, "at-") {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_PAT_INVALID_PREFIX", "Codex personal access token must start with at-")
	}

	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:              proxyURL,
		Timeout:               20 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	})
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadRequest, "OPENAI_CODEX_PAT_PROXY_INVALID", "invalid proxy configuration: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_PAT_REQUEST_FAILED", "failed to build validation request: %v", err)
	}
	req.Header.Set("authorization", "Bearer "+accessToken)
	req.Header.Set("accept", "application/json")
	ApplyCodexCanonicalAuthIdentity(req.Header)

	resp, err := client.Do(req)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_PAT_VALIDATE_FAILED", "failed to validate Codex personal access token: %v", err)
	}
	// Release the validation response on every status and metadata-validation exit,
	// ensuring failed PAT imports do not retain the shared client connection.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_PAT_INVALID", "Codex personal access token is invalid or expired")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Capture a small diagnostic prefix when validation fails; read errors
		// leave any available bytes usable, with HTTP status supplying the empty-body message.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_PAT_VALIDATE_FAILED", "Codex personal access token validation failed: %s", message)
	}

	// Bound whoami parsing to its small metadata envelope before required-field
	// validation; bytes beyond the limit cannot supply missing account attributes.
	var whoami wire.PATWhoamiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&whoami); err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_PAT_RESPONSE_INVALID", "invalid Codex personal access token validation response: %v", err)
	}
	if err := ValidatePATWhoami(whoami); err != nil {
		return nil, err
	}

	return &whoami, nil
}

// ValidatePATWhoami rejects missing trimmed email, user ID, account ID, plan
// type, or FedRAMP presence. A false pointed-to FedRAMP value is valid;
// validation leaves the returned metadata fields as supplied.
func ValidatePATWhoami(whoami wire.PATWhoamiResponse) error {
	required := map[string]string{
		"email":              whoami.Email,
		"chatgpt_user_id":    whoami.ChatGPTUserID,
		"chatgpt_account_id": whoami.ChatGPTAccountID,
		"chatgpt_plan_type":  whoami.ChatGPTPlanType,
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			return infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_PAT_RESPONSE_INVALID", "Codex personal access token validation response is missing %s", key)
		}
	}
	if whoami.ChatGPTAccountIsFedRAMP == nil {
		return infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_PAT_RESPONSE_INVALID", "Codex personal access token validation response is missing chatgpt_account_is_fedramp")
	}
	return nil
}

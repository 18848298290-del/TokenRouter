package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	urlvalidator "github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

// crs_client.go authenticates to CRS and retrieves account exports containing
// credentials for synchronization. URL validation runs before credential
// validation, and the selected trust mode determines HTTPS, host allowlist,
// and resolved-IP checks before either HTTP operation begins.

// CRSClientOptions selects URL validation and host trust for CRS import
// requests.
type CRSClientOptions struct {
	// Configured, AllowlistEnabled, AllowInsecureHTTP, and AllowPrivateHosts
	// jointly determine Fetch's validation path. URL validation runs before
	// credentials are checked and before the HTTP client is created.
	//
	// Configured reports available application configuration. Fetch rejects an
	// unconfigured client before inspecting its root or credentials.
	//
	// AllowlistEnabled selects HTTPS and private-host validation, plus resolved-IP
	// checks. Hosts is required to match only when its list is nonempty.
	//
	// AllowInsecureHTTP permits HTTP roots in the format-validation path used when
	// AllowlistEnabled is false. The allowlist path still requires HTTPS.
	//
	// AllowPrivateHosts permits private destinations in the selected URL and
	// resolved-IP checks. Fetch passes it into the HTTP client options as well.
	// Configured: available application settings; AllowlistEnabled: selected trust checks.
	// AllowInsecureHTTP: permitted format-path scheme; AllowPrivateHosts: permitted private destinations.
	Configured, AllowlistEnabled, AllowInsecureHTTP, AllowPrivateHosts bool
	// Hosts lists allowed CRS destinations when nonempty. NewCRSClient copies it,
	// and the enabled trust path accepts other validated hosts when the list is
	// empty.
	Hosts []string
}

// CRSClient fetches authenticated CRS exports using a retained URL trust
// configuration.
type CRSClient struct {
	options CRSClientOptions // retained CRS URL trust options
}

// crsLoginResponse represents CRS login success, token, and available failure
// details.
type crsLoginResponse struct {
	Success bool `json:"success"` // CRS login success flag
	// Token authenticates the following export. Login rejects whitespace-only
	// tokens but returns a usable token with its original spelling.
	Token string `json:"token"`
	// Message supplies the first failure description. Login consults Error and
	// then its generic text only when Message is empty.
	Message  string `json:"message"`
	Error    string `json:"error"`    // alternate login failure text
	Username string `json:"username"` // username echoed by CRS
}

// NewCRSClient retains CRS trust options and clones the allowed-host slice.
// Later mutations to the caller's Hosts slice cannot change the client's
// validation policy, and construction performs no login request.
func NewCRSClient(options CRSClientOptions) *CRSClient {
	// The client retains these options across synchronization and preview calls.
	// Copy the host list so caller edits cannot change its accepted destinations
	// later.
	options.Hosts = slices.Clone(options.Hosts)
	return &CRSClient{options: options}
}

// Fetch validates the CRS root, logs in, and retrieves an export including
// secrets. Missing configuration fails first, URL validation precedes
// blank-credential checks, and both HTTP operations use the caller context and
// a twenty-second client timeout.
func (s *CRSClient) Fetch(ctx context.Context, baseURL, username, password string) (*transfer.CRSExportResponse, error) {
	if !s.options.Configured {
		return nil, errors.New("config is not available")
	}
	normalizedURL := strings.TrimSpace(baseURL)
	if s.options.AllowlistEnabled {
		normalized, err := NormalizeCRSBaseURL(normalizedURL, s.options.Hosts, s.options.AllowPrivateHosts)
		if err != nil {
			return nil, err
		}
		normalizedURL = normalized
	} else {
		normalized, err := urlvalidator.ValidateURLFormat(normalizedURL, s.options.AllowInsecureHTTP)
		if err != nil {
			return nil, fmt.Errorf("invalid base_url: %w", err)
		}
		normalizedURL = normalized
	}
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil, errors.New("username and password are required")
	}

	client, err := httpclient.GetClient(httpclient.Options{
		Timeout:            20 * time.Second,
		ValidateResolvedIP: s.options.AllowlistEnabled,
		AllowPrivateHosts:  s.options.AllowPrivateHosts,
	})
	if err != nil {
		return nil, fmt.Errorf("create http client failed: %w", err)
	}

	adminToken, err := crsLogin(ctx, client, normalizedURL, username, password)
	if err != nil {
		return nil, err
	}

	return crsExportProviders(ctx, client, normalizedURL, adminToken)
}

// NormalizeCRSBaseURL validates an HTTPS root and returns its normalized form.
// A nonempty allowlist requires a host match; an empty list still applies URL
// and private-host validation according to allowPrivate.
func NormalizeCRSBaseURL(raw string, allowlist []string, allowPrivate bool) (string, error) {
	// An empty host list keeps HTTPS and private-address validation active.
	// Requiring a list match only for configured entries avoids rejecting every
	// otherwise valid CRS root.
	requireAllowlist := len(allowlist) > 0
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     allowlist,
		RequireAllowlist: requireAllowlist,
		AllowPrivate:     allowPrivate,
	})
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}

// crsLogin posts username and password and returns the untrimmed token from a
// successful login result. It closes the response body and reads at most one
// MiB; read errors are discarded, so status and the captured prefix determine
// the result. Failure text prefers Message, then Error, then a generic description.
func crsLogin(ctx context.Context, client *http.Client, baseURL, username, password string) (string, error) {
	payload := map[string]any{
		"username": username,
		"password": password,
	}
	// The fixed login payload contains string values that encoding/json supports.
	// Its marshal result can be used directly without a separate unsupported-value
	// failure path.
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/web/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	// Login reads at most one MiB and discards read errors. The available prefix
	// is used for status diagnostics or JSON parsing, so oversized incomplete JSON
	// fails at decoding.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("crs login failed: status=%d body=%s", resp.StatusCode, string(raw))
	}

	var parsed crsLoginResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("crs login parse failed: %w", err)
	}
	if !parsed.Success || strings.TrimSpace(parsed.Token) == "" {
		// CRS failures can expose either message field. Prefer Message, retain Error
		// when Message is absent, and supply diagnostic text when both fields are
		// empty.
		msg := parsed.Message
		if msg == "" {
			msg = parsed.Error
		}
		if msg == "" {
			msg = "unknown error"
		}
		return "", errors.New("crs login failed: " + msg)
	}
	return parsed.Token, nil
}

// crsExportProviders requests the account export including secrets with the
// supplied administrator token. It closes the response body and reads at most
// five MiB; read errors are discarded, so status and the captured prefix
// determine the result. Unsuccessful HTTP, JSON, or CRS success status fails.
func crsExportProviders(ctx context.Context, client *http.Client, baseURL, adminToken string) (*transfer.CRSExportResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/admin/sync/export-accounts?include_secrets=true", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Export reads at most five MiB and discards read errors. The available prefix
	// is used for status diagnostics or JSON parsing, so oversized incomplete JSON
	// fails at decoding.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("crs export failed: status=%d body=%s", resp.StatusCode, string(raw))
	}

	var parsed transfer.CRSExportResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("crs export parse failed: %w", err)
	}
	if !parsed.Success {
		// CRS failures can expose either message field. Prefer Message, retain Error
		// when Message is absent, and supply diagnostic text when both fields are
		// empty.
		msg := parsed.Message
		if msg == "" {
			msg = parsed.Error
		}
		if msg == "" {
			msg = "unknown error"
		}
		return nil, errors.New("crs export failed: " + msg)
	}
	return &parsed, nil
}

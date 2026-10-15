package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
)

// request.go builds authenticated Gemini requests from credential snapshots
// and protocol-specific endpoint plans.
// Gemini API-key, OAuth, and service-account requests carry the same
// generation data through different endpoints. An OAuth token snapshot
// includes the project returned with that token, so Code Assist wrapping uses
// one credential observation instead of separately reading a project while
// token state may change.
//
// Native plans retain their selected action and upstream stream flag.
// Compatibility plans derive generation actions from client streaming, with
// OAuth allowed to use a different upstream stream. Project-bearing OAuth uses
// the internal wrapper unless ForceAIStudio is set; service accounts use the
// caller Vertex URL, and the remaining plans use a validated AI Studio base.
// REST normalization applies to unwrapped compatibility payloads.

const (
	// Credential mode values select the credential source, upstream endpoint
	// family, payload wrapping, and authentication header used by BuildRequest.
	APIKeyCredential         CredentialMode = "apikey"          // API-key authentication
	OAuthCredential          CredentialMode = "oauth"           // OAuth bearer authentication
	ServiceAccountCredential CredentialMode = "service_account" // service-account bearer authentication
)

// CredentialMode selects the credential source and corresponding Gemini
// upstream request family.
type CredentialMode string

type (
	// TokenSnapshot pairs AccessToken with ProjectID from one acquisition.
	// AccessToken carries bearer credential text, and ProjectID identifies its
	// Code Assist project; request construction uses them from that same
	// snapshot.
	TokenSnapshot struct{ AccessToken, ProjectID string }
	// RequestPlan selects credentials, endpoint, payload dialect, and stream
	// behavior for one request.
	RequestPlan struct {
		Mode CredentialMode // selected upstream credential family
		// Model identifies the upstream model; Action selects its API operation.
		Model, Action string
		// Native retains Action and UpstreamStream directly. Compatibility uses
		// ClientStream, with OAuth instead using UpstreamStream; ForceAIStudio
		// bypasses project-based OAuth wrapping while preserving that credential
		// mode.
		// Native retains action input; ForceAIStudio selects REST; ClientStream
		// describes client output; UpstreamStream describes upstream output.
		Native, ForceAIStudio, ClientStream, UpstreamStream bool
		// APIKey is required for APIKeyCredential and returns the header value.
		// Blank trimmed text fails configuration, while the supplied nonblank
		// spelling is retained.
		// API-key credential acquisition callback
		APIKey func() string
		// Token supplies OAuth or service-account credentials as one snapshot; a
		// missing callback or acquisition error returns before endpoint construction
		// and request creation.
		// coupled token/project acquisition callback
		Token func(context.Context) (TokenSnapshot, error)
		// BaseURL and ValidateURL are required for the REST path. ValidateURL also
		// checks the fixed Code Assist base before constructing a project-bearing
		// OAuth request.
		BaseURL func() string // configured AI Studio base
		// upstream base policy validation
		ValidateURL func(string) (string, error)
		// VertexURL is required in service-account mode and receives the selected
		// action and stream flag. Its caller implementation owns project and
		// location endpoint policy.
		// service-account model endpoint builder
		VertexURL func(string, bool) (string, error)
	}
)

// BuildRequest returns the authenticated generation request and request-ID
// header name for plan. Callers supply callbacks for the selected credential
// and endpoint path, and leave borrowed body bytes unchanged until the request
// has been sent.
//
// OAuth acquisition couples the token and project before choosing an internal
// wrapper or REST route. Compatibility streaming controls the generation
// action, with the OAuth upstream stream choice taking precedence. Internal
// wrapping reencodes decoded JSON, while unwrapped compatibility requests
// delegate REST normalization. Credential, base-validation, payload-decode, or
// URL-construction errors return before network exchange.
func BuildRequest(ctx context.Context, body []byte, plan RequestPlan) (*http.Request, string, error) {
	action, stream := plan.Action, plan.UpstreamStream
	if !plan.Native {
		stream = plan.ClientStream
		if plan.Mode == OAuthCredential {
			stream = plan.UpstreamStream
		}
		action = "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
	}
	var apiKey string
	var token TokenSnapshot
	var err error
	switch plan.Mode {
	case APIKeyCredential:
		apiKey = plan.APIKey()
		if strings.TrimSpace(apiKey) == "" {
			return nil, "", errors.New("gemini api_key not configured")
		}
	case OAuthCredential, ServiceAccountCredential:
		if plan.Token == nil {
			return nil, "", errors.New("gemini token provider not configured")
		}
		token, err = plan.Token(ctx)
		if err != nil {
			return nil, "", err
		}
	default:
		return nil, "", fmt.Errorf("unsupported provider type: %s", plan.Mode)
	}
	var target string
	wire := body
	wrapped := false
	if plan.Mode == ServiceAccountCredential {
		target, err = plan.VertexURL(action, stream)
	} else if plan.Mode == OAuthCredential && strings.TrimSpace(token.ProjectID) != "" && !plan.ForceAIStudio {
		base, validationErr := plan.ValidateURL(codeassist.GeminiCliBaseURL)
		if validationErr != nil {
			return nil, "", validationErr
		}
		target = fmt.Sprintf("%s/v1internal:%s", strings.TrimRight(base, "/"), action)
		if stream {
			target += "?alt=sse"
		}
		// Wrap the generation body together with the acquired project and selected
		// model so the internal endpoint receives one routing envelope tied to that
		// token snapshot.
		var inner any
		if err := json.Unmarshal(body, &inner); err != nil {
			return nil, "", fmt.Errorf("failed to parse gemini request: %w", err)
		}
		// The envelope contains only decoded JSON values and string metadata, so
		// encoding cannot encounter unsupported values or a reference cycle.
		wire, _ = json.Marshal(map[string]any{"model": plan.Model, "project": strings.TrimSpace(token.ProjectID), "request": inner})
		wrapped = true
	} else {
		base, validationErr := plan.ValidateURL(plan.BaseURL())
		if validationErr != nil {
			return nil, "", validationErr
		}
		target, err = BuildGeminiAIStudioModelActionURL(base, plan.Model, action, stream)
	}
	if err != nil {
		return nil, "", err
	}
	// Normalize the compatibility dialect only for REST payloads; the internal
	// Code Assist wrapper carries its separately prepared request representation
	// and routing fields.
	if !plan.Native && !wrapped {
		wire = bridge.NativeNormalizeGeminiRequestForAIStudio(wire)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(wire))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if plan.Mode == APIKeyCredential {
		req.Header.Set("x-goog-api-key", apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}
	if wrapped {
		req.Header.Set("User-Agent", codeassist.GeminiCLIUserAgent)
	}
	return req, "x-request-id", nil
}

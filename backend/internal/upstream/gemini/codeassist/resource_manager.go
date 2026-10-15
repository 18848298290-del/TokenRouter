package codeassist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// resource_manager.go discovers an active Google Cloud project and ranks
// returned projects for Code Assist authorization.
// Code Assist project discovery can fall back to Resource Manager when account
// metadata omits a project. Only ACTIVE entries with a nonblank identifier
// qualify, and selection prefers companion names, then default names, then the
// first remaining project in server order.
//
// The technical read uses bearer authentication and asks its client factory to
// validate resolved IPs. An optional endpoint and factory support controlled
// local transport tests. Selection returns a trimmed project ID from the
// single fetched projects response; lifecycle errors, HTTP failures and
// decoding failures return to authorization, which decides onboarding or
// manual project entry.

// googleCloudProject represents one Resource Manager project and its lifecycle
// state.
type googleCloudProject struct {
	ProjectID      string `json:"projectId"`      // reported project identifier
	DisplayName    string `json:"name"`           // reported project display name
	LifecycleState string `json:"lifecycleState"` // reported project lifecycle state
}

// googleCloudProjectsResponse wraps the projects from one Resource Manager
// response.
type googleCloudProjectsResponse struct {
	Projects []googleCloudProject `json:"projects"` // server ordered project entries
}

// ResourceManagerOptions supplies an endpoint and HTTP-client factory for
// project discovery.
type ResourceManagerOptions struct {
	// Endpoint overrides the project listing URL. An empty value selects the
	// fixed Resource Manager projects endpoint used by authorization discovery.
	Endpoint string // project listing endpoint override
	// Client constructs the request transport with the supplied timeout, proxy,
	// and validation flags. Nil selects httpclient.GetClient for the ordinary
	// discovery path.
	Client func(httpclient.Options) (*http.Client, error) // project transport construction callback
}

// FetchProjectIDFromResourceManager reads and selects an ACTIVE project with
// default endpoint and HTTP-client settings. It returns a trimmed identifier
// or the discovery error; provider authorization decides subsequent recovery.
func FetchProjectIDFromResourceManager(ctx context.Context, accessToken, proxyURL string) (string, error) {
	return FetchProjectWithOptions(ctx, accessToken, proxyURL, ResourceManagerOptions{})
}

// FetchProjectWithOptions returns the preferred ACTIVE project from one
// Resource Manager response. Empty endpoint and nil client factory select
// their defaults; the client receives the trimmed proxy and resolved-IP
// validation flag.
//
// Nonblank ACTIVE identifiers are filtered before comparing lowercased IDs and
// display names. Companion or Code Assist wording takes priority over default
// wording, then server order chooses the first eligible entry. An empty
// eligible set is an error, and HTTP, body-read, and JSON failures return
// before any project selection.
func FetchProjectWithOptions(ctx context.Context, accessToken, proxyURL string, options ResourceManagerOptions) (string, error) {
	endpoint := options.Endpoint
	if endpoint == "" {
		endpoint = "https://cloudresourcemanager.googleapis.com/v1/projects"
	}
	factory := options.Client
	if factory == nil {
		factory = httpclient.GetClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create resource manager request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", GeminiCLIUserAgent)

	client, err := factory(httpclient.Options{
		ProxyURL:           strings.TrimSpace(proxyURL),
		Timeout:            30 * time.Second,
		ValidateResolvedIP: true,
	})
	if err != nil {
		return "", fmt.Errorf("create http client failed: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resource manager request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read resource manager response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resource manager HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var projectsResp googleCloudProjectsResponse
	if err := json.Unmarshal(bodyBytes, &projectsResp); err != nil {
		return "", fmt.Errorf("failed to parse resource manager response: %w", err)
	}

	active := make([]googleCloudProject, 0, len(projectsResp.Projects))
	for _, p := range projectsResp.Projects {
		if p.LifecycleState == "ACTIVE" && strings.TrimSpace(p.ProjectID) != "" {
			active = append(active, p)
		}
	}
	if len(active) == 0 {
		return "", errors.New("no ACTIVE projects found from resource manager")
	}

	// Account discovery prefers a companion project over generic defaults because
	// its ID or display name supplies the Code Assist wording used for project
	// selection.
	for _, p := range active {
		id := strings.ToLower(strings.TrimSpace(p.ProjectID))
		name := strings.ToLower(strings.TrimSpace(p.DisplayName))
		if strings.Contains(id, "cloud-ai-companion") || strings.Contains(name, "cloud ai companion") || strings.Contains(name, "code assist") {
			return strings.TrimSpace(p.ProjectID), nil
		}
	}
	// When no companion wording matches, prefer a default-named ACTIVE project;
	// otherwise the first ACTIVE entry keeps the server ordering as the final
	// choice.
	for _, p := range active {
		id := strings.ToLower(strings.TrimSpace(p.ProjectID))
		name := strings.ToLower(strings.TrimSpace(p.DisplayName))
		if strings.Contains(id, "default") || strings.Contains(name, "default") {
			return strings.TrimSpace(p.ProjectID), nil
		}
	}

	return strings.TrimSpace(active[0].ProjectID), nil
}

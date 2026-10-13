package codeassist

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// TestResourceManagerProjectSelectionLocal returns companion, default, and
// first-ACTIVE fixture lists through a local HTTP endpoint. It checks selected
// project priority, bearer/CLI headers, proxy forwarding to the factory, and
// resolved-IP validation options.
func TestResourceManagerProjectSelectionLocal(t *testing.T) {
	// name labels the project fixture, body supplies its Resource Manager JSON,
	// and want identifies the project selected from that list.
	cases := []struct{ name, body, want string }{
		{"companion", `{"projects":[{"projectId":"plain","lifecycleState":"ACTIVE"},{"projectId":"default-one","lifecycleState":"ACTIVE"},{"projectId":"cloud-ai-companion-one","lifecycleState":"ACTIVE"}]}`, "cloud-ai-companion-one"},
		{"default", `{"projects":[{"projectId":"plain","lifecycleState":"ACTIVE"},{"projectId":"default-one","lifecycleState":"ACTIVE"}]}`, "default-one"},
		{"first-active", `{"projects":[{"projectId":"deleted","lifecycleState":"DELETE_REQUESTED"},{"projectId":"plain","lifecycleState":"ACTIVE"}]}`, "plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
				require.Equal(t, GeminiCLIUserAgent, r.Header.Get("User-Agent"))
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			project, err := FetchProjectWithOptions(context.Background(), "fixture-token", "fixture-proxy", ResourceManagerOptions{Endpoint: server.URL, Client: func(options httpclient.Options) (*http.Client, error) {
				require.True(t, options.ValidateResolvedIP)
				require.Equal(t, "fixture-proxy", options.ProxyURL)
				return server.Client(), nil
			}})
			require.NoError(t, err)
			require.Equal(t, tc.want, project)
		})
	}
}

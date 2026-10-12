package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

// An empty CRS export isolates HTTP defaults and error mapping from provider
// creation. CRSHandler calls through CRSSync so these tests can observe whether
// proxy synchronization was requested and distinguish preview, invalid input
// and injected source failures.

// crsHTTPStore supplies source identity indexing while other embedded store
// operations remain unused.
type crsHTTPStore struct {
	provider.CRSProviderStore // unused embedded provider operations
}

// crsHTTPProxy records serial proxy-list calls for default synchronization
// selection.
type crsHTTPProxy struct {
	reads int // serial proxy-list invocation count
}

// crsHTTPExport supplies an empty source payload and optional top-level fetch
// failure.
type crsHTTPExport struct {
	failure error // configured source fetch error
}

// ListCRSAccountIDs returns a fixed existing source identity map for these
// handler tests. The result makes an empty export a valid synchronization
// scenario without invoking other embedded provider operations.
func (crsHTTPStore) ListCRSAccountIDs(context.Context) (map[string]int64, error) {
	return map[string]int64{"old": 1}, nil
}

// ListActive counts a logical proxy-list invocation and returns no configured
// proxies. Tests use this serial counter to distinguish default synchronization
// from disabled and preview paths.
func (p *crsHTTPProxy) ListActive(context.Context) ([]egress.Proxy, error) {
	p.reads++
	return nil, nil
}

// Create panics when unexpected proxy creation reaches this empty-export
// fixture.
func (*crsHTTPProxy) Create(context.Context, *egress.Proxy) error { panic("未请求创建代理") }

// Fetch returns an empty CRS export together with its configured failure. The
// fixture ignores source login and context because tests observe handler
// response mapping and proxy-read dispatch.
func (e crsHTTPExport) Fetch(context.Context, string, string, string) (*transfer.CRSExportResponse, error) {
	return &transfer.CRSExportResponse{}, e.failure
}

// TestCRSHandlerDefaultsAndErrorEnvelope exercises default proxy
// synchronization, explicit disabling, preview and injected source failures.
// Successful responses use the common envelope, invalid input returns 400 and
// source failures retain the endpoint-specific 500 text.
func TestCRSHandlerDefaultsAndErrorEnvelope(t *testing.T) {
	for _, mode := range []string{"default", "disabled", "preview", "sync_failure", "preview_failure", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			proxies := &crsHTTPProxy{}
			exporter := crsHTTPExport{}
			if strings.HasSuffix(mode, "failure") {
				exporter.failure = errors.New("fixture source failed")
			}
			h := NewCRSHandler(provider.NewCRSSync(crsHTTPStore{}, proxies, exporter, provider.CRSOptions{}))
			r := gin.New()
			r.POST("/sync", h.SyncFromCRS)
			r.POST("/preview", h.PreviewFromCRS)
			payload := `{"base_url":"https://local.test","username":"local","password":"fixture"}`
			if mode == "disabled" {
				payload = `{"base_url":"https://local.test","username":"local","password":"fixture","sync_proxies":false}`
			}
			if mode == "invalid" {
				payload = `{}`
			}
			path := "/sync"
			if strings.HasPrefix(mode, "preview") {
				path = "/preview"
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			switch mode {
			case "invalid":
				require.Equal(t, 400, w.Code)
				require.Contains(t, w.Body.String(), "Invalid request:")
			case "sync_failure":
				require.Equal(t, 500, w.Code)
				require.Contains(t, w.Body.String(), "CRS sync failed: fixture source failed")
			case "preview_failure":
				require.Equal(t, 500, w.Code)
				require.Contains(t, w.Body.String(), "CRS preview failed: fixture source failed")
			default:
				require.Equal(t, 200, w.Code)
				require.Contains(t, w.Body.String(), `"code":0`)
			}
			if mode == "default" {
				require.Equal(t, 1, proxies.reads)
			} else {
				require.Zero(t, proxies.reads)
			}
		})
	}
}

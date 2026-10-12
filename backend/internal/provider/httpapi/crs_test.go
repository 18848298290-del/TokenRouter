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

// These fixtures exercise defaults, response fields, and failures through the handler and use case.
type crsHTTPStore struct{ provider.CRSProviderStore }

type crsHTTPProxy struct{ reads int }

type crsHTTPExport struct{ failure error }

func (crsHTTPStore) ListCRSAccountIDs(context.Context) (map[string]int64, error) {
	return map[string]int64{"old": 1}, nil
}

func (p *crsHTTPProxy) ListActive(context.Context) ([]egress.Proxy, error) {
	p.reads++
	return nil, nil
}
func (*crsHTTPProxy) Create(context.Context, *egress.Proxy) error { panic("未请求创建代理") }

func (e crsHTTPExport) Fetch(context.Context, string, string, string) (*transfer.CRSExportResponse, error) {
	return &transfer.CRSExportResponse{}, e.failure
}

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

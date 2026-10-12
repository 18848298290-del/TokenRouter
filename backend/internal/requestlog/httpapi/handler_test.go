package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
)

type requestRepositoryStub struct {
	userID int64
	admin  bool
	calls  int
}

func (r *requestRepositoryStub) Save(context.Context, []telemetry.RequestRecord) error { return nil }

func (r *requestRepositoryStub) Cleanup(context.Context, time.Time) error { return nil }

func (r *requestRepositoryStub) Find(_ context.Context, _ string, userID int64, admin bool) ([]requestlog.Detail, error) {
	r.userID, r.admin = userID, admin
	r.calls++
	return []requestlog.Detail{{RequestRecord: telemetry.RequestRecord{RequestID: "request", UserID: 42, ProviderID: 99, Aliases: []telemetry.RequestAlias{{Kind: "upstream", Value: "supplier"}}, Attempts: []telemetry.RequestAttempt{{Number: 1, ProviderID: 99, RequestID: "supplier"}}}, Errors: []requestlog.Failure{{ID: 7, Status: 500}}, AuditIDs: []int64{8}}}, nil
}

// TestRequestDiagnosticsRequireAdmin checks authorization before reading details or writer health.
func TestRequestDiagnosticsRequireAdmin(t *testing.T) {
	for _, test := range []struct {
		name   string
		role   string
		status int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"user", "user", http.StatusForbidden},
		{"admin", "admin", http.StatusOK},
	} {
		for _, path := range []string{"/requests?request_id=request&user_id=99&admin=true", "/requests/request", "/requests/health"} {
			t.Run(test.name+path, func(t *testing.T) {
				repo := &requestRepositoryStub{}
				handler := NewHandler(requestlog.NewService(repo, 30, nil))
				router := gin.New()
				router.Use(func(c *gin.Context) {
					if test.role != "" {
						c.Set(authctx.ContextKeyUser, authctx.AuthSubject{UserID: 42})
						c.Set(authctx.ContextKeyUserRole, test.role)
					}
					c.Next()
				})
				router.GET("/requests", handler.Find)
				router.GET("/requests/health", handler.Health)
				router.GET("/requests/:request_id", handler.Find)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				require.Equal(t, test.status, response.Code)
				if test.role != "admin" || path == "/requests/health" {
					require.Zero(t, repo.calls)
					return
				}
				require.Equal(t, int64(42), repo.userID)
				require.True(t, repo.admin)
				require.Equal(t, 1, repo.calls)
				var body struct {
					Data struct {
						Items []requestlog.Detail `json:"items"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
				require.Len(t, body.Data.Items, 1)
				require.Equal(t, int64(99), body.Data.Items[0].ProviderID)
				require.Len(t, body.Data.Items[0].Aliases, 1)
			})
		}
	}
}

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/backup"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

type emptyBackupSettings struct{}

func (emptyBackupSettings) GetValue(context.Context, string) (string, error) { return "", nil }
func (emptyBackupSettings) Set(context.Context, string, string) error        { return nil }

// TestRestorePasswordBoundary checks password verification in the handler; router tests cover administrator and step-up checks.
func TestRestorePasswordBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		subject, valid bool
		status, calls  int
	}{
		{"missing-password", `{}`, true, false, http.StatusBadRequest, 0},
		{"missing-subject", `{"password":"fixture"}`, false, false, http.StatusUnauthorized, 0},
		{"wrong-password", `{"password":"fixture"}`, true, false, http.StatusBadRequest, 1},
		{"valid-password-enters-use-case", `{"password":"fixture"}`, true, true, http.StatusNotFound, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := backup.New(emptyBackupSettings{}, backup.Options{}, nil, nil, nil, nil, nil)
			defer core.Stop()
			calls := 0
			handler := NewBackupHandler(core, func(ctx context.Context, id int64, password string) (bool, error) {
				calls++
				require.Equal(t, int64(42), id)
				require.Equal(t, "fixture", password)
				return tc.valid, nil
			})
			router := gin.New()
			router.POST("/:id/restore", func(c *gin.Context) {
				if tc.subject {
					authctx.SetPrincipal(c, identity.Principal{UserID: 42, Role: "admin"}, 1, "")
				}
				handler.RestoreBackup(c)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/fixture/restore", strings.NewReader(tc.body)))
			require.Equal(t, tc.status, response.Code, response.Body.String())
			require.Equal(t, tc.calls, calls)
			if tc.valid {
				require.Contains(t, response.Body.String(), "BACKUP_NOT_FOUND")
			}
		})
	}
}

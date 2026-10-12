package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

type partialErrorBody struct {
	data  []byte
	first bool
}

type auditCaptureRepository struct {
	mu   sync.Mutex
	logs []*audit.AuditLog
}

// clearHTTPRepository implements ClearWithTrace so tests fail if the handler switches to separate clear and insert calls.
type clearHTTPRepository struct {
	audit.AuditLogRepository
	trace *audit.AuditLog
	err   error
	order *[]string
}

// clearTOTPVerifier records fresh TOTP verification for the clearing operation.
type clearTOTPVerifier struct {
	err   error
	user  int64
	code  string
	order *[]string
}

func (b *partialErrorBody) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	limit := len(b.data)
	if !b.first && limit > 5 {
		limit = 5
	}
	n := copy(p, b.data[:limit])
	b.data = b.data[n:]
	if !b.first {
		b.first = true
		return n, errors.New("injected read error")
	}
	return n, nil
}

func (b *partialErrorBody) Close() error { return nil }

func (r *auditCaptureRepository) BatchInsert(_ context.Context, logs []*audit.AuditLog) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, logs...)
	return int64(len(logs)), nil
}

func (r *auditCaptureRepository) Insert(_ context.Context, log *audit.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
	return nil
}

func (r *auditCaptureRepository) List(context.Context, *audit.AuditLogFilter) (*audit.AuditLogList, error) {
	return &audit.AuditLogList{}, nil
}

func (r *auditCaptureRepository) GetByID(context.Context, int64) (*audit.AuditLog, error) {
	return nil, audit.ErrAuditLogNotFound
}

func (r *auditCaptureRepository) Count(context.Context) (int64, error) { return 0, nil }
func (r *auditCaptureRepository) TruncateAll(context.Context) error    { return nil }
func (r *auditCaptureRepository) DeleteBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func TestDeriveAuditAction(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
	}{
		{"PUT", "/api/v1/admin/providers/:id", "admin.providers.update"},
		{"POST", "/api/v1/admin/providers", "admin.providers.create"},
		{"DELETE", "/api/v1/admin/backups/:id", "admin.backups.delete"},
		{"GET", "/api/v1/admin/users/:id/api-keys", "admin.users.api_keys.read"},
		{"POST", "/api/v1/admin/redeem-codes/batch", "admin.redeem_codes.batch.create"},
	}
	for _, tc := range cases {
		if got := deriveAuditAction(tc.method, tc.path); got != tc.want {
			t.Fatalf("deriveAuditAction(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
	if got := auditActionOverrides["POST /api/v1/subscriptions/:id/revoke"]; got != audit.AuditActionUserSubscriptionRevoke {
		t.Fatalf("subscription revoke audit action = %q, want %q", got, audit.AuditActionUserSubscriptionRevoke)
	}
}

func TestAuditSensitiveReadsIncludesForkBackupRoutes(t *testing.T) {
	want := map[string]string{
		"GET /api/v1/admin/backups/:id/download":   "admin.backups.download",
		"GET /api/v1/admin/backups/storage-config": "admin.backups.storage_config.read",
	}
	for route, action := range want {
		if got := auditSensitiveReads[route]; got != action {
			t.Fatalf("auditSensitiveReads[%q] = %q, want %q", route, got, action)
		}
	}
}

func TestAuditMiddlewareRestoresPartialBodyAfterReadError(t *testing.T) {
	want := []byte(`{"name":"完整请求"}`)
	var got []byte

	router := gin.New()
	router.POST("/api/v1/admin/test", gin.HandlerFunc(NewAuditLogMiddleware(nil, audit.NewRedactor(nil))), func(c *gin.Context) {
		var err error
		got, err = io.ReadAll(c.Request.Body)
		if err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = &partialErrorBody{data: append([]byte(nil), want...)}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
	}
	if string(got) != string(want) {
		t.Fatalf("restored body = %q, want %q", got, want)
	}
}

// TestOllamaCloudUsageSessionRouteOmitsAuditBody checks that browser cookies in the session body
// are omitted entirely from audit storage, including credentials under the "session" key.
func TestOllamaCloudUsageSessionRouteOmitsAuditBody(t *testing.T) {
	require.Contains(t, auditBodyOmittedRoutes, "PUT /api/v1/admin/providers/:id/ollama-cloud-usage/session")

	repository := &auditCaptureRepository{}
	auditService := audit.NewAuditLogService(repository, nil)
	auditService.Start()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 77})
		c.Set(string(authctx.ContextKeyUserRole), "admin")
		c.Next()
	})
	router.Use(gin.HandlerFunc(NewAuditLogMiddleware(auditService, audit.NewRedactor(nil))))
	router.PUT("/api/v1/admin/providers/:id/ollama-cloud-usage/session", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/providers/7/ollama-cloud-usage/session",
		bytes.NewBufferString(`{"session":"wos-session=audit-canary-cookie; __Secure-authjs.session-token.0=audit-canary-shard"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	auditService.Stop()

	repository.mu.Lock()
	logs := append([]*audit.AuditLog(nil), repository.logs...)
	repository.mu.Unlock()
	require.Len(t, logs, 1)
	require.Equal(t, "<credential-bearing body omitted>", logs[0].RequestBody)
	require.NotContains(t, logs[0].RequestBody, "audit-canary")
}

func TestPasskeyFinishRoutesOmitAuditBody(t *testing.T) {
	// WebAuthn finish bodies contain full assertions or attestations and must be omitted from audit storage.
	for _, route := range []string{
		"POST /api/v1/auth/passkey/login/finish",
		"POST /api/v1/user/passkeys/register/finish",
	} {
		require.Contains(t, auditBodyOmittedRoutes, route)
	}
}

func (r *auditCaptureRepository) ClearWithTrace(_ context.Context, trace *audit.AuditLog) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.logs)
	r.logs = []*audit.AuditLog{trace}
	return int64(n), nil
}

func (r *clearHTTPRepository) ClearWithTrace(_ context.Context, trace *audit.AuditLog) (int64, error) {
	*r.order = append(*r.order, "clear-with-trace")
	r.trace = trace
	return 7, r.err
}

func (v *clearTOTPVerifier) VerifyCode(_ context.Context, user int64, code string) error {
	*v.order = append(*v.order, "totp")
	v.user, v.code = user, code
	return v.err
}

func TestAuditClearHTTPAuthorizationAndTrace(t *testing.T) {
	cases := []struct {
		name, auth, body, reason string
		user                     int64
		verifyErr, storeErr      error
		status                   int
		wantOrder                []string
	}{
		{name: "admin-key-rejected-before-body", auth: audit.AuditAuthMethodAdminAPIKey, status: http.StatusForbidden, reason: "STEP_UP_ADMIN_API_KEY_FORBIDDEN"},
		{name: "missing-subject", auth: audit.AuditAuthMethodJWT, status: http.StatusUnauthorized},
		{name: "malformed-body", user: 77, body: "{", status: http.StatusBadRequest, reason: "TOTP_CODE_REQUIRED"},
		{name: "missing-code", user: 77, body: `{}`, status: http.StatusBadRequest, reason: "TOTP_CODE_REQUIRED"},
		{name: "totp-not-enabled", user: 77, body: `{"totp_code":"123456"}`, verifyErr: apperror.Forbidden("TOTP_NOT_SETUP", "not enabled"), status: http.StatusForbidden, reason: "TOTP_NOT_SETUP", wantOrder: []string{"totp"}},
		{name: "totp-invalid", user: 77, body: `{"totp_code":"123456"}`, verifyErr: apperror.Forbidden("TOTP_INVALID_CODE", "invalid code"), status: http.StatusForbidden, reason: "TOTP_INVALID_CODE", wantOrder: []string{"totp"}},
		{name: "trace-failure", user: 77, body: `{"totp_code":"123456"}`, storeErr: errors.New("trace failed"), status: http.StatusInternalServerError, wantOrder: []string{"totp", "clear-with-trace"}},
		{name: "success", user: 77, body: `{"totp_code":" 123456 "}`, status: http.StatusOK, wantOrder: []string{"totp", "clear-with-trace"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			repo := &clearHTTPRepository{err: tc.storeErr, order: &order}
			verifier := &clearTOTPVerifier{err: tc.verifyErr, order: &order}
			svc := audit.NewAuditLogService(repo, nil)
			t.Cleanup(svc.Stop)
			h := NewAuditLogHandler(svc, verifier)
			router := gin.New()
			var skipped bool
			router.POST("/api/v1/admin/audit-logs/clear", func(c *gin.Context) {
				c.Set("auth_method", tc.auth)
				if tc.user > 0 {
					c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: tc.user})
					c.Set("user_role", "admin")
					c.Set(ContextKeyAuthEmail, "admin@example.test")
				}
				c.Header("X-Request-ID", "test-clear")
				h.Clear(c)
				skipped = c.GetBool(auditCtxKeySkip)
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/audit-logs/clear", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-contract-credential")
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, tc.wantOrder, order)
			if tc.reason != "" {
				require.Contains(t, w.Body.String(), tc.reason)
			}
			if len(order) > 0 {
				require.Equal(t, tc.user, verifier.user)
				require.Equal(t, "123456", verifier.code)
			}
			if repo.trace != nil {
				require.Equal(t, tc.user, *repo.trace.ActorUserID)
				require.Equal(t, "admin", repo.trace.ActorRole)
				require.Equal(t, "test-clear", repo.trace.RequestID)
				require.Equal(t, audit.AuditActionAuditLogClear, repo.trace.Action)
				require.NotEqual(t, "test-contract-credential", repo.trace.CredentialMasked)
			}
			require.Equal(t, tc.status == http.StatusOK, skipped)
			if skipped {
				require.Contains(t, w.Body.String(), `"deleted":7`)
			}
		})
	}
}

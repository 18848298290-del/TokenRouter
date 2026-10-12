package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/notification"
)

// TestTestSMTPRequestUseTLSOmissionFallsBackToSavedSetting binds an empty connection request against a saved true TLS flag.
// It verifies omission remains a nil pointer and resolution selects the saved
// setting, keeping absence distinguishable from an explicitly disabled TLS option.
func TestTestSMTPRequestUseTLSOmissionFallsBackToSavedSetting(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{}`)

	require.Nil(t, req.SMTPUseTLS)
	require.True(t, ResolveSMTPUseTLS(req.SMTPUseTLS, &notification.SMTPConfig{UseTLS: true}))
}

// TestTestSMTPRequestExplicitFalseOverridesSavedUseTLS binds an explicit false TLS value while stored configuration enables it.
// It verifies the non-nil request pointer remains authoritative and TLS resolution
// returns false for that requested administrator connection test.
func TestTestSMTPRequestExplicitFalseOverridesSavedUseTLS(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{"smtp_use_tls":false}`)

	require.NotNil(t, req.SMTPUseTLS)
	require.False(t, ResolveSMTPUseTLS(req.SMTPUseTLS, &notification.SMTPConfig{UseTLS: true}))
}

// TestTestSMTPRequestExplicitTrueOverridesMissingSavedUseTLS binds a requested true TLS value with no saved configuration.
// It verifies the explicit pointer is present and resolution selects true,
// allowing complete request input to determine the connection's transport mode.
func TestTestSMTPRequestExplicitTrueOverridesMissingSavedUseTLS(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{"smtp_use_tls":true}`)

	require.NotNil(t, req.SMTPUseTLS)
	require.True(t, ResolveSMTPUseTLS(req.SMTPUseTLS, nil))
}

// TestSendTestEmailRequestPreservesUseTLSOmissionSemantics binds test-message requests with omitted and explicitly false TLS input.
// It verifies omission selects saved true while the explicit false value overrides
// it, using the same pointer rules as connection testing.
func TestSendTestEmailRequestPreservesUseTLSOmissionSemantics(t *testing.T) {
	omitted := bindSMTPRequest[SendTestEmailRequest](t, `{"email":"admin@example.com"}`)
	explicitFalse := bindSMTPRequest[SendTestEmailRequest](t, `{"email":"admin@example.com","smtp_use_tls":false}`)
	saved := &notification.SMTPConfig{UseTLS: true}

	require.True(t, ResolveSMTPUseTLS(omitted.SMTPUseTLS, saved))
	require.False(t, ResolveSMTPUseTLS(explicitFalse.SMTPUseTLS, saved))
}

// bindSMTPRequest decodes JSON through Gin into the requested SMTP test payload type.
// It marks itself as a helper and fails the owning test when binding rejects
// the fixture, returning the typed request for omission and explicit-value assertions.
func bindSMTPRequest[T any](t *testing.T, body string) T {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	var payload T
	require.NoError(t, c.ShouldBindJSON(&payload))
	return payload
}

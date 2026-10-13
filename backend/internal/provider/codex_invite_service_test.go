package provider

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// TestNormalizeCodexInviteResetGrantType combines reward presence with
// recognized, absent, and future actions. It checks explicit no-reward
// precedence, two known administrator values, and unknown results for the
// configured unresolved cases.
func TestNormalizeCodexInviteResetGrantType(t *testing.T) {
	hasRewards := true
	hasNoRewards := false
	require.Equal(t, "none", normalizeCodexInviteResetGrantType(&hasNoRewards, "workspace_credits"))
	require.Equal(t, "rate_limit_reset", normalizeCodexInviteResetGrantType(&hasRewards, "rate_limit_reset_credit"))
	require.Equal(t, "workspace_credits", normalizeCodexInviteResetGrantType(&hasRewards, "workspace_credits"))
	require.Equal(t, "unknown", normalizeCodexInviteResetGrantType(&hasRewards, "future_reward"))
	require.Equal(t, "unknown", normalizeCodexInviteResetGrantType(nil, ""))
}

// TestNormalizeCodexInviteEmailsRejectsInvalidAndTooMany supplies malformed
// text and six unique addresses in one input. It checks bad-request
// classification and the configured five-address-limit reason for these
// rejected invitation lists.
func TestNormalizeCodexInviteEmailsRejectsInvalidAndTooMany(t *testing.T) {
	_, err := normalizeCodexInviteEmails([]string{"bad-email"})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))

	_, err = normalizeCodexInviteEmails([]string{"a@x.com,b@x.com,c@x.com,d@x.com,e@x.com,f@x.com"})
	require.Error(t, err)
	require.Equal(t, "CODEX_INVITE_RESET_EMAIL_LIMIT", apperror.Reason(err))
}

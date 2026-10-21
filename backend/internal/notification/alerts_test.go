package notification

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	mailtest "github.com/TokenFlux/TokenRouter/internal/notification/testkit"
)

// alerts_test.go checks balance and provider alert bodies with captured business
// values. Default currency, configurable account units, escaped links, and quota
// calculations remain observable in rendered text, including the direct body
// builder used by standalone callers.

// TestBuildBalanceLowEmailBody_CustomUnit supplies a configured symbol containing
// HTML-sensitive text. Both balance and threshold use escaped account units while
// the standalone builder continues to format numeric values to two decimals.
func TestBuildBalanceLowEmailBody_CustomUnit(t *testing.T) {
	repo := mailtest.NewMemorySettings()
	require.NoError(t, repo.Set(context.Background(), "balance_unit_symbol", "<C>%"))
	s := NewAlertDelivery(nil, NewNotificationEmailService(repo, nil))
	body := s.BuildBalanceLowEmailBody("Alice", 3.14, 10, "Site", "")
	require.Contains(t, body, "&lt;C&gt;%3.14")
	require.Contains(t, body, "&lt;C&gt;%10.00")
	require.NotContains(t, body, "$3.14")
}

// TestBuildBalanceLowEmailBody_ContainsRequiredFields renders supplied branding, recipient, balance, and threshold values.
// It verifies required values are present and the formatting output contains
// no missing, extra, or argument-type error markers.
func TestBuildBalanceLowEmailBody_ContainsRequiredFields(t *testing.T) {
	s := &AlertDelivery{}
	body := s.BuildBalanceLowEmailBody("Alice", 3.14, 10.0, "MySite", "")

	require.Contains(t, body, "MySite")
	require.Contains(t, body, "Alice")
	require.Contains(t, body, "$3.14")
	require.Contains(t, body, "$10.00")

	// An argument-count mismatch leaves formatting error markers in the output.
	require.NotContains(t, body, "%!")
	require.NotContains(t, body, "MISSING")
	require.NotContains(t, body, "EXTRA")
}

// TestBuildBalanceLowEmailBody_WithRechargeURL supplies a recharge URL alongside ordinary balance data.
// It verifies the rendered anchor and recharge text are present and formatting
// introduces no argument error marker in the built-in body.
func TestBuildBalanceLowEmailBody_WithRechargeURL(t *testing.T) {
	s := &AlertDelivery{}
	body := s.BuildBalanceLowEmailBody("Bob", 5.0, 20.0, "Site", "https://example.com/pay")

	require.Contains(t, body, `href="https://example.com/pay"`)
	require.Contains(t, body, "立即充值")
	require.NotContains(t, body, "%!")
}

// TestBuildBalanceLowEmailBody_RechargeURLEscaped supplies ampersands and tag-like text inside the recharge destination.
// It verifies HTML entity escaping retains that link content and the raw
// script tag spelling does not appear in the rendered body.
func TestBuildBalanceLowEmailBody_RechargeURLEscaped(t *testing.T) {
	s := &AlertDelivery{}
	// The URL contains characters that require HTML escaping.
	body := s.BuildBalanceLowEmailBody("u", 1.0, 5.0, "Site", `https://example.com/?a=1&b=<script>`)

	require.Contains(t, body, "&amp;")
	require.Contains(t, body, "&lt;script&gt;")
	require.NotContains(t, body, "<script>")
}

// TestBuildBalanceLowEmailBody_NoRechargeURLOmitsButton renders a low-balance body with an empty recharge destination.
// It verifies the optional anchor and recharge caption are absent, leaving
// ordinary balance information available without a blank button.
func TestBuildBalanceLowEmailBody_NoRechargeURLOmitsButton(t *testing.T) {
	s := &AlertDelivery{}
	body := s.BuildBalanceLowEmailBody("u", 1.0, 5.0, "Site", "")
	require.NotContains(t, body, `<a href`)
	require.NotContains(t, body, "立即充值")
}

// TestBuildQuotaAlertEmailBody_AllFieldsPresent renders provider identity and all supplied quota display values.
// It verifies branding, IDs, labels, amounts, and remainder appear, and no
// formatting argument-count/type markers remain in the resulting body.
func TestBuildQuotaAlertEmailBody_AllFieldsPresent(t *testing.T) {
	s := &AlertDelivery{}
	body := s.BuildQuotaAlertEmailBody(
		42,            // providerID
		"acc-foo",     // providerName
		"anthropic",   // platform
		"日限额 / Daily", // dimLabel
		750.50,        // used
		1000.0,        // limit
		249.50,        // remaining
		"$249.50",     // thresholdDisplay
		"MySite",      // siteName
	)

	require.Contains(t, body, "MySite")
	require.Contains(t, body, "#42")
	require.Contains(t, body, "acc-foo")
	require.Contains(t, body, "anthropic")
	require.Contains(t, body, "Daily")
	require.Contains(t, body, "$750.50")
	require.Contains(t, body, "$1000.00")
	require.Contains(t, body, "$249.50")

	// An argument-count mismatch leaves formatting error markers in the output.
	require.NotContains(t, body, "%!")
	require.NotContains(t, body, "MISSING")
	require.NotContains(t, body, "EXTRA")
}

// TestBuildQuotaAlertEmailBody_UnlimitedDisplay supplies a zero quota limit to the raw alert body helper.
// It verifies both language spellings of the unlimited label are present,
// protecting the display representation selected for a nonpositive configured limit.
func TestBuildQuotaAlertEmailBody_UnlimitedDisplay(t *testing.T) {
	s := &AlertDelivery{}
	body := s.BuildQuotaAlertEmailBody(
		1, "n", "p", "dim",
		100.0, 0.0, // limit=0 means unlimited quota
		0.0, "30%", "Site",
	)
	require.Contains(t, body, "无限制")
	require.Contains(t, body, "Unlimited")
}

// TestBuildQuotaAlertEmailBody_PercentageThresholdDisplay passes preformatted percentage threshold text to the raw body formatter.
// It verifies that text appears unchanged in the body and does not create
// a formatting error marker through its percent character.
func TestBuildQuotaAlertEmailBody_PercentageThresholdDisplay(t *testing.T) {
	s := &AlertDelivery{}
	body := s.BuildQuotaAlertEmailBody(
		1, "n", "p", "dim",
		700.0, 1000.0, 300.0,
		"30%", // percentage threshold
		"Site",
	)
	require.Contains(t, body, "30%")
	require.NotContains(t, body, "%!")
}

// TestBuildQuotaAlertEmailBody_RemainingClampedAtZero passes a zero remainder with usage greater than the supplied limit.
// It verifies the body displays zero; the caller supplies that clamped value,
// so this assertion checks formatting rather than performing the clamp itself.
func TestBuildQuotaAlertEmailBody_RemainingClampedAtZero(t *testing.T) {
	// The caller clamps remaining quota to zero; this test checks how zero is displayed.
	s := &AlertDelivery{}
	body := s.BuildQuotaAlertEmailBody(
		1, "n", "p", "dim",
		1500.0, 1000.0, 0.0, // usage exceeds the limit
		"$100.00", "Site",
	)
	require.Contains(t, body, "$0.00")
}

// TestBuildBalanceLowEmailBody_NoCSSFormatError renders the built-in balance body containing escaped gradient percentages.
// It verifies both zero and one-hundred percent text survive formatting as
// literal CSS percentages for the provided fixture values.
func TestBuildBalanceLowEmailBody_NoCSSFormatError(t *testing.T) {
	s := &AlertDelivery{}
	body := s.BuildBalanceLowEmailBody("u", 1.0, 5.0, "Site", "")
	// Template %% escapes must render as literal percentages in CSS gradients.
	require.True(t,
		strings.Contains(body, "0%") && strings.Contains(body, "100%"),
		"CSS gradient percentages not rendered; got: %s", body)
}

// TestBuildQuotaAlertEmailBody_NoCSSFormatError renders the quota body containing escaped CSS gradient percentages.
// It verifies the formatted HTML retains zero and one-hundred percent spellings,
// protecting the template's doubled-percent formatting of those gradient endpoints.
func TestBuildQuotaAlertEmailBody_NoCSSFormatError(t *testing.T) {
	s := &AlertDelivery{}
	body := s.BuildQuotaAlertEmailBody(1, "n", "p", "d", 0, 0, 0, "$0.00", "Site")
	require.True(t,
		strings.Contains(body, "0%") && strings.Contains(body, "100%"),
		"CSS gradient percentages not rendered; got: %s", body)
}

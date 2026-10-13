package ops

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	mailtest "github.com/TokenFlux/TokenRouter/internal/notification/testkit"
)

// Summary template variables carry queried dashboard values, while detailed
// reports hide that metric section and supply HTML through notification's
// authorized raw field. These tests check formatting, localization, ordinary
// delivery identities, and one legacy-template SMTP message using supplied
// report data.

// TestOpsScheduledReportDeliverySourceIDIncludesReportIdentity compares daily
// and weekly report definitions and the nil fallback. It checks that their
// ordinary identifiers include type, name, and schedule components.
func TestOpsScheduledReportDeliverySourceIDIncludesReportIdentity(t *testing.T) {
	report := &opsScheduledReport{Name: "日报", ReportType: "daily_summary", Schedule: "0 9 * * *"}
	sourceID := opsScheduledReportDeliverySourceID(report)
	require.Contains(t, sourceID, "daily_summary")
	require.Contains(t, sourceID, "日报")
	require.Contains(t, sourceID, "0 9 * * *")
	require.NotEqual(t, sourceID, opsScheduledReportDeliverySourceID(&opsScheduledReport{Name: "周报", ReportType: "weekly_summary", Schedule: "0 9 * * 1"}))
	require.Equal(t, "scheduled_report", opsScheduledReportDeliverySourceID(nil))
}

// TestOpsSummaryReportEmailVariables supplies counts, fractions, latency
// pointers, and rate measurements to summary formatting. It checks localized
// names, interval strings, missing-overview display flags, and authorized
// legacy HTML rendering.
func TestOpsSummaryReportEmailVariables(t *testing.T) {
	latencyP50 := 8231
	latencyP99 := 151260
	ttftP50 := 1674
	ttftP99 := 11222
	now := time.Date(2026, time.July, 19, 1, 0, 26, 0, time.UTC)
	report := &opsScheduledReport{
		Name:       "日报",
		ReportType: "daily_summary",
		TimeRange:  24 * time.Hour,
	}
	overview := &OpsDashboardOverview{
		RequestCountTotal:            2374,
		SuccessCount:                 1451,
		ErrorCountSLA:                2,
		BusinessLimitedCount:         921,
		SLA:                          0.9986,
		ErrorRate:                    0.0014,
		UpstreamErrorRate:            0.0028,
		UpstreamErrorCountExcl429529: 4,
		Upstream429Count:             0,
		Upstream529Count:             0,
		TokenConsumed:                121550190,
		Duration:                     OpsPercentiles{P50: &latencyP50, P99: &latencyP99},
		TTFT:                         OpsPercentiles{P50: &ttftP50, P99: &ttftP99},
		QPS:                          OpsRateSummary{Current: 0, Peak: 1.2, Avg: 0},
		TPS:                          OpsRateSummary{Current: 0, Peak: 133421.2, Avg: 1406.8},
	}

	variables := opsSummaryReportEmailVariables(report, now, overview, "en")
	require.Equal(t, "Daily summary", variables["report_name"])
	require.Equal(t, "daily_summary", variables["report_type"])
	require.Equal(t, "2026-07-18T01:00:26Z", variables["report_start_time"])
	require.Equal(t, "2026-07-19T01:00:26Z", variables["report_end_time"])
	require.Equal(t, "2,374", variables["report_total_requests"])
	require.Equal(t, "1,451", variables["report_success_count"])
	require.Equal(t, "2", variables["report_sla_error_count"])
	require.Equal(t, "921", variables["report_business_limited_count"])
	require.Equal(t, "99.86%", variables["report_sla"])
	require.Equal(t, "0.14%", variables["report_error_rate"])
	require.Equal(t, "0.28%", variables["report_upstream_error_rate"])
	require.Equal(t, "4", variables["report_upstream_error_count_excl_429_529"])
	require.Equal(t, "8,231 ms", variables["report_latency_p50"])
	require.Equal(t, "151,260 ms", variables["report_latency_p99"])
	require.Equal(t, "1,674 ms", variables["report_ttft_p50"])
	require.Equal(t, "11,222 ms", variables["report_ttft_p99"])
	require.Equal(t, "121,550,190", variables["report_tokens"])
	require.Equal(t, "1.2", variables["report_qps_peak"])
	require.Equal(t, "133421.2", variables["report_tps_peak"])
	require.Equal(t, "1406.8", variables["report_tps_avg"])
	require.Empty(t, variables["report_html"])
	require.Equal(t, "none", variables["report_detail_display"])

	zhVariables := opsSummaryReportEmailVariables(report, now, overview, "zh-CN")
	require.Equal(t, "日报", zhVariables["report_name"])
	emptyVariables := opsSummaryReportEmailVariables(report, now, nil, "en")
	require.Equal(t, "block", emptyVariables["report_summary_display"])
	require.Equal(t, "none", emptyVariables["report_detail_display"])

	// Pass the generated HTML through notification's authorized raw
	// variable map so the legacy placeholder renders markup alongside the
	// escaped summary variables.
	legacyTemplate, err := notification.RenderNotificationEmail(
		notification.NotificationEmailEventOpsScheduledReport,
		"Report",
		`<section>{{report_html}}</section>`,
		variables,
		map[string]string{"report_html": `<h2>generated summary</h2>`},
	)
	require.NoError(t, err)
	require.Contains(t, legacyTemplate.HTML, `<h2>generated summary</h2>`)
}

// TestOpsScheduledReportVariablesDoNotUsePreviewMetrics prepares an error
// digest without queried summary data. Every shared metric placeholder stays a
// dash, the detail section is visible, and the summary section is hidden.
func TestOpsScheduledReportVariablesDoNotUsePreviewMetrics(t *testing.T) {
	now := time.Date(2026, time.July, 19, 1, 0, 26, 0, time.UTC)
	report := &opsScheduledReport{
		Name:       "错误摘要",
		ReportType: "error_digest",
		TimeRange:  24 * time.Hour,
	}

	variables := opsScheduledReportLocalizedEmailVariables(report, now, "en")
	require.Equal(t, "Error digest", variables["report_name"])
	require.Empty(t, variables["report_html"])
	require.Equal(t, "block", variables["report_detail_display"])
	for _, placeholder := range contract.SummaryPlaceholders() {
		if placeholder == "report_summary_display" {
			require.Equal(t, "none", variables[placeholder])
			continue
		}
		require.Equal(t, "-", variables[placeholder])
	}
}

// TestOpsScheduledReportLegacyTemplateReceivesSummaryHTML sends a supplied
// daily report through notification's editable template to a local SMTP
// fixture. It checks one recipient attempt, one received message, and inclusion
// of the generated summary HTML.
func TestOpsScheduledReportLegacyTemplateReceivesSummaryHTML(t *testing.T) {
	ctx := context.Background()
	repo := mailtest.NewMemorySettings()
	smtpServer := mailtest.StartSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtpServer.Settings()))

	emailService := notification.NewMailer(repo, smtp.New())
	notificationService := notification.NewNotificationEmailService(repo, emailService)
	emailService.SetNotificationEmailService(notificationService)
	_, err := notificationService.UpdateTemplate(
		ctx,
		notification.NotificationEmailEventOpsScheduledReport,
		"en",
		"Legacy report {{report_name}}",
		`<section data-template="legacy">{{report_html}}</section>`,
	)
	require.NoError(t, err)

	// Forward the complete report request to notification, including raw
	// HTML and reminder identity, so this SMTP fixture exercises the
	// editable template's actual send path.
	delivery := &EmailDelivery{TemplatesEnabled: true, SendEmail: emailService.SendEmail, RecipientName: notification.EmailRecipientName, ShouldFallback: notification.ShouldFallbackNotificationEmail, ResolveRecipientLocale: notificationService.ResolveRecipientLocale, SendTemplate: func(ctx context.Context, v Notification) error {
		return notificationService.Send(ctx, notification.SendRequest{Event: v.Event, Locale: v.Locale, RecipientEmail: v.RecipientEmail, RecipientName: v.RecipientName, SourceType: v.SourceType, SourceID: v.SourceID, ReminderKey: v.ReminderKey, Variables: v.Variables, RawHTMLVariables: v.RawHTMLVariables})
	}}
	svc := NewOpsScheduledReportService(NewOpsService(&opsRepoMock{}, nil, nil, nil, nil, nil, nil, nil), nil, delivery, nil, nil)
	report := &opsScheduledReport{
		Name:       "日报",
		ReportType: "daily_summary",
		TimeRange:  24 * time.Hour,
		Recipients: []string{"ops@example.com"},
	}

	attempts, err := svc.RunReport(ctx, report, time.Date(2026, time.July, 19, 1, 0, 26, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1, attempts)
	require.Equal(t, int64(1), smtpServer.MessageCount())
	messageBody := smtpServer.LastMessageBody(t)
	require.Contains(t, messageBody, `<section data-template="legacy">`)
	require.Contains(t, messageBody, `<h2>日报</h2>`)
}

// TestFormatOpsReportIntegerGroupsDigits formats a positive four-digit count,
// its negative counterpart, and a short count. Expected strings retain the sign
// and insert separators for those larger values.
func TestFormatOpsReportIntegerGroupsDigits(t *testing.T) {
	require.Equal(t, "2,374", formatOpsReportInteger(2374))
	require.Equal(t, "-1,234", formatOpsReportInteger(-1234))
	require.Equal(t, "42", formatOpsReportInteger(42))
}

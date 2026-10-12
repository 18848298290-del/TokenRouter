package notification

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	mailtest "github.com/TokenFlux/TokenRouter/internal/notification/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

type localeDelivery struct {
	language, subject, body string
	count                   int
}

func TestBuildVerifyCodeEmailBody_EscapesSiteName(t *testing.T) {
	t.Run("escapes_script_injection", func(t *testing.T) {
		body := renderIdentityBody(t, NotificationEmailEventAuthVerifyCode, "123456", `</h1><script>alert(1)</script><h1>`)

		assert.NotContains(t, body, "<script>")
		assert.Contains(t, body, "&lt;script&gt;")
	})

	t.Run("escapes_html_entities", func(t *testing.T) {
		body := renderIdentityBody(t, NotificationEmailEventAuthVerifyCode, "123456", `A&B<C>"D`)

		assert.Contains(t, body, "A&amp;B&lt;C&gt;&#34;D")
	})

	t.Run("keeps_normal_site_name", func(t *testing.T) {
		body := renderIdentityBody(t, NotificationEmailEventAuthVerifyCode, "654321", "My Site")

		assert.Contains(t, body, "My Site")
		assert.Contains(t, body, "654321")
	})
}

func TestBuildPasswordResetEmailBody_EscapesHTMLValues(t *testing.T) {
	t.Run("escapes_html_tags_in_site_name", func(t *testing.T) {
		body := renderIdentityBody(t, NotificationEmailEventAuthPasswordReset, "https://example.com/reset?token=abc", `</h1><img src=x onerror=alert(1)>`)

		assert.NotContains(t, body, "<img src=x")
		assert.Contains(t, body, "&lt;img")
	})

	t.Run("escapes_html_entities", func(t *testing.T) {
		body := renderIdentityBody(t, NotificationEmailEventAuthPasswordReset, "https://example.com/reset", `A&B<C>`)

		assert.Contains(t, body, "A&amp;B&lt;C&gt;")
	})

	t.Run("keeps_normal_site_name_and_url", func(t *testing.T) {
		resetURL := "https://example.com/reset?token=xyz"
		body := renderIdentityBody(t, NotificationEmailEventAuthPasswordReset, resetURL, "TokenRouter")

		assert.Contains(t, body, "TokenRouter")
		assert.Contains(t, body, resetURL)
	})

	t.Run("escapes_ampersand_in_reset_url", func(t *testing.T) {
		resetURL := "https://example.com/reset?a=1&b=2"
		body := renderIdentityBody(t, NotificationEmailEventAuthPasswordReset, resetURL, "Site")

		assert.NotContains(t, body, `href="https://example.com/reset?a=1&b=2"`)
		assert.Contains(t, body, `href="https://example.com/reset?a=1&amp;b=2"`)
	})
}

// TestConcurrentNotificationDelivery verifies that concurrent requests for one delivery send once and release
// the coordination entry.
func TestConcurrentNotificationDelivery(t *testing.T) {
	repo := mailtest.NewMemorySettings()
	server := mailtest.StartSMTPServer(t)
	require.NoError(t, repo.SetMultiple(context.Background(), server.Settings()))
	n := NewNotificationEmailService(repo, NewMailer(repo, smtp.New()))
	input := SendRequest{Event: NotificationEmailEventSubscriptionExpiryReminder, RecipientEmail: "fixture@example.com", SourceType: "subscription", SourceID: "1", ReminderKey: "7d"}
	start := make(chan struct{})
	errs := make(chan error, 16)
	for range 16 {
		go func() { <-start; errs <- n.Send(context.Background(), input) }()
	}
	close(start)
	for range 16 {
		require.NoError(t, <-errs)
	}
	require.Equal(t, int64(1), server.MessageCount())
	require.Empty(t, n.locks.entries)
}

func TestConcurrentFirstUnsubscribeSecret(t *testing.T) {
	n := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)
	start := make(chan struct{})
	tokens := make(chan string, 16)
	errs := make(chan error, 16)
	for range 16 {
		go func() {
			<-start
			token, err := n.createUnsubscribeToken(context.Background(), "fixture@example.com", NotificationEmailEventBalanceLow)
			tokens <- token
			errs <- err
		}()
	}
	close(start)
	for range 16 {
		token := <-tokens
		require.NoError(t, <-errs)
		_, err := n.parseUnsubscribeToken(context.Background(), token)
		require.NoError(t, err)
	}
	require.Empty(t, n.locks.entries)
}

// TestUserNotificationLocales covers user events, invalid template overrides, and event locale precedence.
func TestUserNotificationLocales(t *testing.T) {
	events := []string{NotificationEmailEventAuthVerifyCode, NotificationEmailEventAuthPasswordReset, NotificationEmailEventNotificationEmailVerifyCode, NotificationEmailEventTeamInvitation, NotificationEmailEventTeamOwnershipTransfer, NotificationEmailEventSubscriptionPurchaseSuccess, NotificationEmailEventSubscriptionExpiryReminder, NotificationEmailEventBalanceLow, NotificationEmailEventBalanceRechargeSuccess, NotificationEmailEventContentModerationViolation, NotificationEmailEventContentModerationDisabled}
	for _, event := range events {
		t.Run(event, func(t *testing.T) {
			ctx := context.Background()
			repo := mailtest.NewMemorySettings()
			sender := &localeDelivery{}
			service := NewNotificationEmailService(repo, sender)
			service.SetRecipientLocaleReader(func(context.Context, int64, string) string { return "zh" })
			require.NoError(t, repo.Set(ctx, notificationEmailTemplateKey(event, "zh-Hans"), `{"subject":"broken","html":"{{invalid_placeholder}}"}`))
			input := SendRequest{Event: event, RecipientEmail: "user@example.com", RecipientName: "User", UserID: 12}
			require.NoError(t, service.Send(ctx, input))
			require.Equal(t, "zh-Hans", sender.language)
			require.NotContains(t, sender.body, "invalid_placeholder")
			require.NotEqual(t, "broken", sender.subject)
			input.Locale = "en"
			require.NoError(t, service.Send(ctx, input))
			require.Equal(t, "en", sender.language)
			require.Equal(t, 2, sender.count)
		})
	}
}

// TestRecipientLocalePriority checks account, remembered recipient, and site locale precedence.
func TestRecipientLocalePriority(t *testing.T) {
	ctx := context.Background()
	repo := mailtest.NewMemorySettings()
	service := NewNotificationEmailService(repo, nil)
	require.NoError(t, repo.Set(ctx, "default_locale", "zh-Hans"))
	require.Equal(t, "zh-Hans", service.ResolveRecipientLocale(ctx, 5, "u@example.com"))
	service.RememberRecipientLocale(ctx, 5, "u@example.com", "en")
	require.Equal(t, "en", service.ResolveRecipientLocale(ctx, 5, "u@example.com"))
	service.SetRecipientLocaleReader(func(context.Context, int64, string) string { return "zh-Hans" })
	require.Equal(t, "zh-Hans", service.ResolveRecipientLocale(ctx, 5, "u@example.com"))
}

func TestNotificationEmailPreviewEscapesHTMLAndSanitizesSubject(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
		Event:   NotificationEmailEventBalanceLow,
		Locale:  "en-US,en;q=0.9",
		Subject: "Low balance for {{recipient_name}}\r\nInjected",
		HTML:    `<p>{{recipient_name}}</p><a href="{{recharge_url}}">Recharge</a>`,
		Variables: map[string]string{
			"recipient_name": `<script>alert("x")</script>`,
			"recharge_url":   `javascript:alert(1)`,
		},
	})
	require.NoError(t, err)
	require.NotContains(t, preview.Subject, "\r")
	require.NotContains(t, preview.Subject, "\n")
	require.Contains(t, preview.Subject, `Low balance for <script>alert("x")</script>Injected`)
	require.Contains(t, preview.HTML, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`)
	require.NotContains(t, preview.HTML, `javascript:alert(1)`)
	require.Contains(t, preview.HTML, `href=""`)
}

func TestNotificationEmailTemplateOverrideAndRestore(t *testing.T) {
	ctx := context.Background()
	repo := mailtest.NewMemorySettings()
	svc := NewNotificationEmailService(repo, nil)

	official, err := svc.GetTemplate(ctx, NotificationEmailEventBalanceRechargeSuccess, "en")
	require.NoError(t, err)
	require.False(t, official.IsCustom)

	updated, err := svc.UpdateTemplate(
		ctx,
		NotificationEmailEventBalanceRechargeSuccess,
		"zh-Hans",
		"充值完成：{{recharge_amount}}",
		"<p>{{recipient_name}} 已充值 {{recharge_amount}}</p>",
	)
	require.NoError(t, err)
	require.True(t, updated.IsCustom)
	require.Equal(t, "zh-Hans", updated.Locale)
	require.Equal(t, "充值完成：{{recharge_amount}}", updated.Subject)
	require.NotNil(t, updated.UpdatedAt)

	restored, err := svc.RestoreOfficialTemplate(ctx, NotificationEmailEventBalanceRechargeSuccess, "zh")
	require.NoError(t, err)
	require.False(t, restored.IsCustom)
	require.NotEqual(t, updated.Subject, restored.Subject)
	_, err = repo.GetValue(ctx, notificationEmailTemplateKey(NotificationEmailEventBalanceRechargeSuccess, "zh"))
	require.ErrorIs(t, err, ErrSettingNotFound)
}

func TestNotificationEmailTemplateRejectsUnsupportedPlaceholder(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	_, err := svc.UpdateTemplate(
		ctx,
		NotificationEmailEventSubscriptionPurchaseSuccess,
		"en",
		"Purchased {{not_allowed}}",
		"<p>{{subscription_group}}</p>",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported placeholder")
}

func TestNotificationEmailAuthTemplatesAreListedAndPreviewable(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	infos := svc.ListEventInfos()
	events := make(map[string]NotificationEmailEventInfo, len(infos))
	for _, info := range infos {
		events[info.Event] = info
	}
	require.Contains(t, events, NotificationEmailEventAuthVerifyCode)
	require.Contains(t, events, NotificationEmailEventAuthPasswordReset)
	require.False(t, events[NotificationEmailEventAuthVerifyCode].Optional)
	require.False(t, events[NotificationEmailEventAuthPasswordReset].Optional)
	require.Contains(t, events[NotificationEmailEventAuthVerifyCode].Placeholders, "verification_code")
	require.Contains(t, events[NotificationEmailEventAuthPasswordReset].Placeholders, "reset_url")

	verifyPreview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
		Event:  NotificationEmailEventAuthVerifyCode,
		Locale: "zh-CN",
		Variables: map[string]string{
			"verification_code":  "654321",
			"expires_in_minutes": "15",
		},
	})
	require.NoError(t, err)
	require.Contains(t, verifyPreview.Subject, "邮箱验证码")
	require.Contains(t, verifyPreview.HTML, "654321")

	resetPreview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
		Event:  NotificationEmailEventAuthPasswordReset,
		Locale: "en",
		Variables: map[string]string{
			"reset_url":          "https://example.com/reset?token=abc",
			"expires_in_minutes": "30",
		},
	})
	require.NoError(t, err)
	require.Contains(t, resetPreview.Subject, "Password reset")
	require.Contains(t, resetPreview.HTML, "https://example.com/reset?token=abc")
}

func TestNotificationEmailEventOrderAndCopies(t *testing.T) {
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)
	want := []string{
		NotificationEmailEventAuthVerifyCode,
		NotificationEmailEventAuthPasswordReset,
		NotificationEmailEventNotificationEmailVerifyCode,
		NotificationEmailEventTeamInvitation,
		NotificationEmailEventTeamOwnershipTransfer,
		NotificationEmailEventSubscriptionPurchaseSuccess,
		NotificationEmailEventSubscriptionExpiryReminder,
		NotificationEmailEventBalanceLow,
		NotificationEmailEventBalanceRechargeSuccess,
		NotificationEmailEventProviderQuotaAlert,
		NotificationEmailEventContentModerationViolation,
		NotificationEmailEventContentModerationDisabled,
		NotificationEmailEventOpsAlert,
		NotificationEmailEventOpsScheduledReport,
	}
	infos := svc.ListEventInfos()
	events := make([]string, 0, len(infos))
	seen := make(map[string]bool, len(infos))
	for _, info := range infos {
		require.False(t, seen[info.Event], "重复事件：%s", info.Event)
		seen[info.Event] = true
		events = append(events, info.Event)
	}
	require.Equal(t, want, events)

	infos[0].Event = "changed"
	infos[0].Placeholders[0] = "changed"
	again := svc.ListEventInfos()
	require.Equal(t, want[0], again[0].Event)
	require.Equal(t, "site_name", again[0].Placeholders[0])

	templates, err := svc.ListTemplates(context.Background())
	require.NoError(t, err)
	locales := svc.SupportedLocales()
	require.Len(t, templates, len(want)*len(locales))
	for eventIndex, event := range want {
		for localeIndex, language := range locales {
			template := templates[eventIndex*len(locales)+localeIndex]
			require.Equal(t, event, template.Event)
			require.Equal(t, language, template.Locale)
		}
	}
}

func TestNotificationEmailAdditionalEventsAreListedAndPreviewable(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	infos := svc.ListEventInfos()
	events := make(map[string]NotificationEmailEventInfo, len(infos))
	for _, info := range infos {
		events[info.Event] = info
	}

	checks := []struct {
		event       string
		placeholder string
	}{
		{NotificationEmailEventNotificationEmailVerifyCode, "verification_code"},
		{NotificationEmailEventTeamInvitation, "invitation_url"},
		{NotificationEmailEventProviderQuotaAlert, "provider_name"},
		{NotificationEmailEventContentModerationViolation, "moderation_category"},
		{NotificationEmailEventContentModerationDisabled, "violation_count"},
		{NotificationEmailEventOpsAlert, "rule_name"},
		{NotificationEmailEventOpsScheduledReport, "report_html"},
	}

	for _, check := range checks {
		info, ok := events[check.event]
		require.Truef(t, ok, "expected %s to be listed", check.event)
		require.False(t, info.Optional)
		require.Contains(t, info.Placeholders, check.placeholder)

		preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{Event: check.event, Locale: "zh"})
		require.NoError(t, err)
		require.NotEmpty(t, preview.Subject)
		require.NotEmpty(t, preview.HTML)
	}
}

func TestNotificationEmailTeamInvitationTemplates(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	for _, locale := range []string{"en", "zh"} {
		tmpl, err := svc.GetTemplate(ctx, NotificationEmailEventTeamInvitation, locale)
		require.NoError(t, err)
		require.False(t, tmpl.IsCustom)
		require.Contains(t, tmpl.Placeholders, "team_name")
		require.Contains(t, tmpl.Placeholders, "invitation_url")
		require.Contains(t, tmpl.Placeholders, "expires_at")

		preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
			Event:  NotificationEmailEventTeamInvitation,
			Locale: locale,
			Variables: map[string]string{
				"team_name":      "Codex Review",
				"invitation_url": "https://example.com/team?invitation=test-token",
				"expires_at":     "2026-08-03T12:00:00+09:00",
			},
		})
		require.NoError(t, err)
		require.Contains(t, preview.Subject, "Codex Review")
		require.Contains(t, preview.HTML, "Codex Review")
		require.Contains(t, preview.HTML, "https://example.com/team?invitation=test-token")
		require.NotContains(t, preview.HTML, "{{invitation_url}}")
	}
}

func TestOpsScheduledReportTemplateExposesEditableSummaryMetrics(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	requiredPlaceholders := []string{
		"report_summary_display",
		"report_detail_display",
		"report_total_requests",
		"report_success_count",
		"report_sla_error_count",
		"report_business_limited_count",
		"report_sla",
		"report_error_rate",
		"report_upstream_error_rate",
		"report_upstream_error_count_excl_429_529",
		"report_upstream_429_count",
		"report_upstream_529_count",
		"report_latency_p50",
		"report_latency_p99",
		"report_ttft_p50",
		"report_ttft_p99",
		"report_tokens",
		"report_qps_current",
		"report_qps_peak",
		"report_qps_avg",
		"report_tps_current",
		"report_tps_peak",
		"report_tps_avg",
	}

	for _, locale := range []string{"en", "zh"} {
		tmpl, err := svc.GetTemplate(ctx, NotificationEmailEventOpsScheduledReport, locale)
		require.NoError(t, err)
		for _, placeholder := range requiredPlaceholders {
			require.Contains(t, tmpl.Placeholders, placeholder)
			require.Contains(t, tmpl.HTML, "{{"+placeholder+"}}")
		}

		preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
			Event:  NotificationEmailEventOpsScheduledReport,
			Locale: locale,
		})
		require.NoError(t, err)
		require.Contains(t, preview.HTML, "2,374")
		require.Contains(t, preview.HTML, "99.86%")
		require.Contains(t, preview.HTML, "151,260 ms")
		require.Contains(t, preview.HTML, `style="display: none;"`)
		require.NotContains(t, preview.HTML, "{{report_total_requests}}")
	}
}

func TestOpsScheduledReportRuntimeVariablesDoNotLeakPreviewSamples(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	variables := svc.runtimeVariables(ctx, NotificationEmailEventOpsScheduledReport, "en", NotificationEmailSendInput{})
	require.Equal(t, "none", variables["report_summary_display"])
	require.Equal(t, "block", variables["report_detail_display"])
	require.Empty(t, variables["report_html"])
	for _, placeholder := range notificationEmailOpsSummaryPlaceholders {
		if placeholder == "report_summary_display" {
			continue
		}
		require.Equal(t, "-", variables[placeholder])
	}

	rendered, err := RenderNotificationEmail(
		NotificationEmailEventOpsScheduledReport,
		"Report",
		`<div style="display: {{report_detail_display}};">{{report_html}}</div>`,
		variables,
		nil,
	)
	require.NoError(t, err)
	require.NotContains(t, rendered.HTML, "<h2>Daily summary</h2>")
}

func TestNotificationEmailRawHTMLVariablesAreTrustedOnlyForHTMLPlaceholders(t *testing.T) {
	require.True(t, notificationEmailRawHTMLAllowed(NotificationEmailEventOpsScheduledReport, "report_html"))
	require.False(t, notificationEmailRawHTMLAllowed(NotificationEmailEventOpsScheduledReport, "recipient_name"))
	require.False(t, notificationEmailRawHTMLAllowed(NotificationEmailEventOpsAlert, "report_html"))

	preview, err := RenderNotificationEmail(
		NotificationEmailEventOpsScheduledReport,
		"Report for {{recipient_name}}",
		`<section>{{report_html}}</section><p>{{recipient_name}}</p>`,
		map[string]string{
			"recipient_name": `<script>alert("x")</script>`,
			"report_html":    `<p>escaped report</p>`,
		},
		map[string]string{
			"report_html": `<table><tr><td>trusted report</td></tr></table>`,
		},
	)
	require.NoError(t, err)
	require.Contains(t, preview.HTML, `<table><tr><td>trusted report</td></tr></table>`)
	require.NotContains(t, preview.HTML, `escaped report`)
	require.Contains(t, preview.HTML, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`)
	require.Contains(t, preview.Subject, `<script>alert("x")</script>`)

	preview, err = RenderNotificationEmail(
		NotificationEmailEventOpsScheduledReport,
		"Recipient {{recipient_name}}",
		`<p>{{recipient_name}}</p>`,
		map[string]string{"recipient_name": `<em>escaped</em>`},
		map[string]string{"recipient_name": `<strong>raw</strong>`},
	)
	require.NoError(t, err)
	require.Contains(t, preview.HTML, `&lt;em&gt;escaped&lt;/em&gt;`)
	require.NotContains(t, preview.HTML, `<strong>raw</strong>`)
}

func TestNotificationEmailFallbackClassification(t *testing.T) {
	templateErr := NotificationEmailTemplateErr(errors.New("bad template"))
	configErr := NotificationEmailConfigErr(errors.New("missing email service"))
	deliveryErr := NotificationEmailDeliveryErr(errors.New("smtp timeout"))

	require.True(t, ShouldFallbackNotificationEmail(templateErr))
	require.True(t, ShouldFallbackNotificationEmail(configErr))
	require.False(t, ShouldFallbackNotificationEmail(deliveryErr))
	require.True(t, IsNotificationEmailDeliveryError(deliveryErr))
	require.False(t, IsNotificationEmailDeliveryError(templateErr))
	require.False(t, ShouldFallbackNotificationEmail(nil))
}

func TestNotificationEmailUnsubscribeOnlyAllowsOptionalEvents(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	token, err := svc.createUnsubscribeToken(ctx, "User@Example.com", NotificationEmailEventBalanceLow)
	require.NoError(t, err)
	result, err := svc.Unsubscribe(ctx, token)
	require.NoError(t, err)
	require.True(t, result.Done)
	require.Equal(t, NotificationEmailEventBalanceLow, result.Event)
	unsubscribed, err := svc.IsUnsubscribed(ctx, "user@example.com", NotificationEmailEventBalanceLow)
	require.NoError(t, err)
	require.True(t, unsubscribed)

	transactionalToken, err := svc.createUnsubscribeToken(ctx, "user@example.com", NotificationEmailEventBalanceRechargeSuccess)
	require.NoError(t, err)
	_, err = svc.Unsubscribe(ctx, transactionalToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transactional")

	authToken, err := svc.createUnsubscribeToken(ctx, "user@example.com", NotificationEmailEventAuthVerifyCode)
	require.NoError(t, err)
	_, err = svc.Unsubscribe(ctx, authToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transactional")
}

func TestNotificationEmailLocaleMemoryNormalizesAcceptLanguage(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(mailtest.NewMemorySettings(), nil)

	svc.RememberRecipientLocale(ctx, 42, "User@Example.com", "zh-CN,zh;q=0.9,en;q=0.8")
	require.Equal(t, "zh-Hans", svc.ResolveRecipientLocale(ctx, 42, "user@example.com"))
	require.Equal(t, "zh-Hans", svc.ResolveRecipientLocale(ctx, 0, "user@example.com"))
}

func TestNotificationEmailDeliveryKeyUsesShortStableHash(t *testing.T) {
	key := notificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		"1234567890",
		"User@Example.com",
		"7d",
	)
	require.NotEmpty(t, key)
	require.LessOrEqual(t, len(key), 100)
	require.True(t, strings.HasPrefix(key, notificationEmailDeliveryKeyPrefix+"v2:"))
	require.Equal(t, key, notificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		"1234567890",
		"user@example.com",
		"7d",
	))
	require.NotEqual(t, key, notificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		"1234567890",
		"user@example.com",
		"3d",
	))

	legacyKey := legacyNotificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		"1234567890",
		"user@example.com",
		"7d",
	)
	require.Greater(t, len(legacyKey), 100)
}

func TestNotificationEmailPreferenceKeyUsesShortStableHashAndReadsLegacyKey(t *testing.T) {
	ctx := context.Background()
	repo := mailtest.NewMemorySettings()
	svc := NewNotificationEmailService(repo, nil)

	key := notificationEmailPreferenceKey(NotificationEmailEventSubscriptionExpiryReminder, "User@Example.com")
	require.NotEmpty(t, key)
	require.LessOrEqual(t, len(key), 100)
	require.True(t, strings.HasPrefix(key, notificationEmailPreferenceKeyPrefix+"v2:"))
	require.Equal(t, key, notificationEmailPreferenceKey(NotificationEmailEventSubscriptionExpiryReminder, "user@example.com"))

	legacyKey := legacyNotificationEmailPreferenceKey(NotificationEmailEventSubscriptionExpiryReminder, "user@example.com")
	require.Greater(t, len(legacyKey), 100)
	require.NoError(t, repo.Set(ctx, legacyKey, "unsubscribed"))

	unsubscribed, err := svc.IsUnsubscribed(ctx, "User@Example.com", NotificationEmailEventSubscriptionExpiryReminder)
	require.NoError(t, err)
	require.True(t, unsubscribed)
}

func TestNotificationEmailSendDeduplicatesSubscriptionExpiryReminder(t *testing.T) {
	ctx := context.Background()
	repo := mailtest.NewMemorySettings()
	smtpServer := mailtest.StartSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtpServer.Settings()))

	emailSvc := NewMailer(repo, smtp.New())
	svc := NewNotificationEmailService(repo, emailSvc)
	input := NotificationEmailSendInput{
		Event:          NotificationEmailEventSubscriptionExpiryReminder,
		RecipientEmail: "User@Example.com",
		RecipientName:  "User",
		UserID:         42,
		SourceType:     "user_subscription",
		SourceID:       "1234567890",
		ReminderKey:    "7d",
		Variables: map[string]string{
			"subscription_group": "Codex",
			"expiry_time":        "2026-05-27 12:00",
			"days_remaining":     "7",
		},
	}

	require.NoError(t, svc.Send(ctx, input))
	require.Equal(t, int64(1), smtpServer.MessageCount())

	key := notificationEmailDeliveryKey(input.Event, input.SourceType, input.SourceID, input.RecipientEmail, input.ReminderKey)
	require.LessOrEqual(t, len(key), 100)
	_, err := repo.GetValue(ctx, key)
	require.NoError(t, err)

	require.NoError(t, svc.Send(ctx, input))
	require.Equal(t, int64(1), smtpServer.MessageCount())
}

func TestNotificationEmailSendRespectsLegacyDeliveryKey(t *testing.T) {
	ctx := context.Background()
	repo := mailtest.NewMemorySettings()
	svc := NewNotificationEmailService(repo, nil)
	input := NotificationEmailSendInput{
		Event:          NotificationEmailEventSubscriptionExpiryReminder,
		RecipientEmail: "user@example.com",
		SourceType:     "user_subscription",
		SourceID:       "1234567890",
		ReminderKey:    "7d",
	}
	legacyKey := legacyNotificationEmailDeliveryKey(input.Event, input.SourceType, input.SourceID, input.RecipientEmail, input.ReminderKey)
	require.NoError(t, repo.Set(ctx, legacyKey, "sent"))

	require.NoError(t, svc.Send(ctx, input))
}

func TestNotificationEmailMemorySettingRepoSatisfiesInterface(t *testing.T) {
	var _ SettingRepository = (*mailtest.MemorySettings)(nil)
	require.False(t, strings.Contains(notificationEmailPreferenceKey(NotificationEmailEventBalanceLow, "User@Example.com"), "User@Example.com"))
}

func TestEmailQueueTasksPreserveLocaleHints(t *testing.T) {
	queue := &EmailQueueService{taskChan: make(chan EmailTask, 2)}
	require.NoError(t, queue.EnqueueVerifyCode("user@example.com", "TokenRouter", "zh-CN"))
	require.NoError(t, queue.EnqueuePasswordReset("user@example.com", "TokenRouter", "https://example.com/reset", "en-US"))

	verifyTask := <-queue.taskChan
	require.Equal(t, TaskTypeVerifyCode, verifyTask.TaskType)
	require.Equal(t, "zh-CN", verifyTask.Locale)

	resetTask := <-queue.taskChan
	require.Equal(t, TaskTypePasswordReset, resetTask.TaskType)
	require.Equal(t, "en-US", resetTask.Locale)
}

// TestNotificationAccountPreference checks that an account's chosen language takes precedence over a
// remembered request locale.
func TestNotificationAccountPreference(t *testing.T) {
	ctx := context.Background()
	repo := mailtest.NewMemorySettings()
	svc := NewNotificationEmailService(repo, nil)
	svc.SetRecipientLocaleReader(func(context.Context, int64, string) string { return "en" })
	svc.RememberRecipientLocale(ctx, 42, "user@example.com", "zh-CN")
	require.Equal(t, "en", svc.ResolveRecipientLocale(ctx, 42, "user@example.com"))
}

// renderIdentityBody renders a built-in delivery template for escaping assertions.
func renderIdentityBody(t *testing.T, event, value, siteName string) string {
	t.Helper()
	template := notificationEmailOfficialTemplates[event][notificationEmailLocaleChinese]
	rendered, err := RenderNotificationEmail(event, template.Subject, template.HTML, map[string]string{"site_name": siteName, "verification_code": value, "reset_url": value, "expires_in_minutes": "15"}, nil)
	assert.NoError(t, err)
	return rendered.HTML
}

func (s *localeDelivery) SendEmail(ctx context.Context, _, subject, body string) error {
	s.language, s.subject, s.body = locale.FromContext(ctx), subject, body
	s.count++
	return nil
}

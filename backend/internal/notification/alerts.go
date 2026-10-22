package notification

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
)

// alerts.go renders and delivers balance and provider-quota alerts.
// Balance and provider-quota alerts arrive with recipients and business
// observations already selected by their callers. Delivery prefers the shared
// notification service, which resolves templates, recipient language, and source
// markers; standalone senders can use built-in content without that shared state.
//
// Quota template failures select raw built-in delivery only when their error
// chain has template or configuration classification. Other send outcomes end
// that recipient's attempt so transport uncertainty does not automatically cause
// another message. Each recipient gets an independent context budget, and the
// formatted quota remainder is clamped at zero while threshold kind chooses
// percentage or currency display.

const (
	// Recipient send budgets bound each alert attempt independently, while the
	// threshold kind selects percentage display for caller-supplied remaining-quota alert limits.
	emailSendTimeout        = 30 * time.Second
	thresholdTypePercentage = "percentage"

	// balanceLowEmailTemplate is the HTML template for balance low notifications.
	// Format args: siteName, userName, userName, formattedBalance,
	// formattedThreshold, formattedThreshold, rechargeBlock.
	// The recharge button is appended dynamically when rechargeURL is set.
	balanceLowEmailTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background-color: #f5f5f5; margin: 0; padding: 20px; }
        .container { max-width: 600px; margin: 0 auto; background-color: #fff; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
        .header { background: linear-gradient(135deg, #f59e0b 0%%, #d97706 100%%); color: white; padding: 30px; text-align: center; }
        .header h1 { margin: 0; font-size: 24px; }
        .content { padding: 40px 30px; text-align: center; }
        .balance { font-size: 36px; font-weight: bold; color: #dc2626; margin: 20px 0; }
        .info { color: #666; font-size: 14px; line-height: 1.6; margin-top: 20px; }
        .recharge-btn { display: inline-block; margin-top: 24px; padding: 12px 32px; background: linear-gradient(135deg, #f59e0b 0%%, #d97706 100%%); color: #fff; text-decoration: none; border-radius: 6px; font-size: 16px; font-weight: bold; }
        .footer { background-color: #f8f9fa; padding: 20px; text-align: center; color: #999; font-size: 12px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header"><h1>%s</h1></div>
        <div class="content">
            <p style="font-size: 18px; color: #333;">%s，您的余额不足</p>
            <p style="color: #666;">Dear %s, your balance is running low</p>
            <div class="balance">%s</div>
            <div class="info">
                <p>您的账户余额已低于提醒阈值 <strong>%s</strong>。</p>
                <p>Your account balance has fallen below the alert threshold of <strong>%s</strong>.</p>
                <p>请及时充值以免服务中断。</p>
                <p>Please top up to avoid service interruption.</p>
            </div>
            %s
        </div>
        <div class="footer"><p>此邮件由系统自动发送，请勿回复。</p></div>
    </div>
</body>
</html>`

	// quotaAlertEmailTemplate is the HTML template for provider quota alert notifications.
	// Format args: siteName, providerID, providerName, platform, dimLabel, used, limitStr, remaining,
	// thresholdDisplay.
	quotaAlertEmailTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background-color: #f5f5f5; margin: 0; padding: 20px; }
        .container { max-width: 600px; margin: 0 auto; background-color: #fff; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
        .header { background: linear-gradient(135deg, #ef4444 0%%, #dc2626 100%%); color: white; padding: 30px; text-align: center; }
        .header h1 { margin: 0; font-size: 24px; }
        .content { padding: 40px 30px; }
        .metric { display: flex; justify-content: space-between; padding: 12px 0; border-bottom: 1px solid #eee; }
        .metric-label { color: #666; }
        .metric-value { font-weight: bold; color: #333; }
        .info { color: #666; font-size: 14px; line-height: 1.6; margin-top: 20px; text-align: center; }
        .footer { background-color: #f8f9fa; padding: 20px; text-align: center; color: #999; font-size: 12px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header"><h1>%s</h1></div>
        <div class="content">
            <p style="font-size: 18px; color: #333; text-align: center;">提供商限额告警 / Provider Quota Alert</p>
            <div class="metric"><span class="metric-label">提供商 ID / Provider ID</span><span class="metric-value">#%d</span></div>
            <div class="metric"><span class="metric-label">提供商 / Provider</span><span class="metric-value">%s</span></div>
            <div class="metric"><span class="metric-label">平台 / Platform</span><span class="metric-value">%s</span></div>
            <div class="metric"><span class="metric-label">维度 / Dimension</span><span class="metric-value">%s</span></div>
            <div class="metric"><span class="metric-label">已使用 / Used</span><span class="metric-value">$%.2f</span></div>
            <div class="metric"><span class="metric-label">限额 / Limit</span><span class="metric-value">%s</span></div>
            <div class="metric"><span class="metric-label">剩余额度 / Remaining</span><span class="metric-value">$%.2f</span></div>
            <div class="metric"><span class="metric-label">提醒阈值 / Alert Threshold</span><span class="metric-value">%s</span></div>
            <div class="info">
                <p>提供商剩余额度已低于提醒阈值，请及时关注。</p>
                <p>Provider remaining quota has fallen below the alert threshold.</p>
            </div>
        </div>
        <div class="footer"><p>此邮件由系统自动发送，请勿回复。</p></div>
    </div>
</body>
</html>`

	// Canonical quota dimensions select display labels for alert content;
	// unknown dimension names keep their caller-provided spelling through the rendering path.
	quotaDimDaily   = "daily"   // daily quota window
	quotaDimWeekly  = "weekly"  // weekly quota window
	quotaDimMonthly = "monthly" // monthly quota window
	quotaDimTotal   = "total"   // cumulative quota total
)

// quotaDimLabels supplies bilingual labels for canonical provider quota
// dimensions while SendQuotaAlertEmails retains unknown caller-supplied dimension names.
var quotaDimLabels = map[string]string{
	quotaDimDaily:   "日限额 / Daily",
	quotaDimWeekly:  "周限额 / Weekly",
	quotaDimMonthly: "月限额 / Monthly",
	quotaDimTotal:   "总限额 / Total",
}

// AlertDelivery selects shared event sending or standalone alert bodies for business-provided recipients.
type AlertDelivery struct {
	// emailService handles standalone bodies and classified quota fallbacks.
	// Supply it whenever those paths can be selected, because they invoke it directly.
	emailService Sender // direct alert sender
	// notificationEmailService supplies preferred templates and source markers;
	// set it before concurrent calls because replacement of this shared field is unsynchronized.
	notificationEmailService *NotificationEmailService
}

// NewAlertDelivery binds direct and optional shared-template senders for balance and quota events.
// Construction performs no sending. Configure a shared service before concurrent
// use when callers need its recipient preferences and delivery-key coordination.
// Supply a direct sender when standalone or classified raw fallback can be
// selected, because those paths invoke the direct dependency.
func NewAlertDelivery(email Sender, n *NotificationEmailService) *AlertDelivery {
	return &AlertDelivery{emailService: email, notificationEmailService: n}
}

// SetNotificationEmailService replaces the alert adapter's preferred shared-template service.
// Call during assembly before concurrent alert sends; the field is read without
// synchronization. Nil selects the adapter's remaining mailer or standalone body paths.
func (s *AlertDelivery) SetNotificationEmailService(n *NotificationEmailService) {
	s.notificationEmailService = n
}

// sendEmails sends one supplied subject/body sequentially to the recipient list with separate budgets.
// Empty recipient lists are logged and return. Each attempt uses a background
// context with the configured timeout; failures are logged and later recipients
// continue, with no per-recipient transport retry in this helper.
func (s *AlertDelivery) sendEmails(recipients []string, subject, body string, logAttrs ...any) {
	if len(recipients) == 0 {
		slog.Warn("sendEmails: no recipients", "subject", subject)
		return
	}
	for _, to := range recipients {
		// Recipient attempts have independent budgets so a slow destination does
		// not consume another recipient's timeout. Release this timer after the current send outcome is known.
		ctx, cancel := context.WithTimeout(context.Background(), emailSendTimeout)
		if err := s.emailService.SendEmail(ctx, to, subject, body); err != nil {
			attrs := append([]any{"to", to, "error", err}, logAttrs...)
			slog.Error("failed to send notification", attrs...)
		} else {
			slog.Info("notification email sent successfully", "to", to, "subject", subject)
		}
		cancel()
	}
}

// SendBalanceLowEmails renders a low-balance notice for each business-selected recipient.
// An empty userName uses userEmail. Each recipient gets a background timeout and
// a source identity containing user ID plus the current UTC date; errors are
// logged rather than returned to the business caller.
//
// The bound notification service is preferred, followed by a sender implementing
// SendUserNotification. Otherwise the official English template uses caller branding
// and supplied variables directly. Those standalone sends bypass shared preference
// and marker handling; service-backed sends use their ordinary rendering and
// optional-event policy. Balance and threshold text use two decimal places.
func (s *AlertDelivery) SendBalanceLowEmails(recipients []string, userID int64, userName, userEmail string, balance, threshold float64, siteName, rechargeURL string) {
	displayName := userName
	if displayName == "" {
		displayName = userEmail
	}
	for _, to := range recipients {
		// Recipient attempts have independent budgets so a slow destination does
		// not consume another recipient's timeout. Release this timer after the current send outcome is known.
		ctx, cancel := context.WithTimeout(context.Background(), emailSendTimeout)
		input := SendRequest{
			Event: NotificationEmailEventBalanceLow, RecipientEmail: to, RecipientName: displayName,
			UserID: userID, SourceType: "balance_low", SourceID: strconv.FormatInt(userID, 10),
			ReminderKey: time.Now().UTC().Format("2006-01-02"),
			Variables:   map[string]string{"current_balance": fmt.Sprintf("%.2f", balance), "threshold": fmt.Sprintf("%.2f", threshold), "recharge_url": rechargeURL},
		}
		var err error
		// Prefer the shared renderer, then a mailer's event capability, before
		// selecting the standalone body. Those service paths supply recipient preferences and marker handling.
		if s.notificationEmailService != nil {
			err = s.notificationEmailService.Send(ctx, input)
		} else if mailer, ok := s.emailService.(interface {
			// SendUserNotification renders and sends an event through the sender's own service.
			// This adapter forwards its recipient/source fields and lets that mailer
			// capability select preferences, templates, and marker handling.
			SendUserNotification(context.Context, SendRequest) error
		}); ok {
			err = mailer.SendUserNotification(ctx, input)
		} else {
			// A bare sender has no shared template/preferences capability. Render the
			// English default with caller branding, then send only when that standalone rendering succeeds.
			official := notificationEmailOfficialTemplates[input.Event][notificationEmailDefaultLocale]
			input.Variables["site_name"] = siteName
			input.Variables["recipient_name"] = displayName
			input.Variables["recipient_email"] = to
			var rendered NotificationEmailPreview
			rendered, err = RenderNotificationEmail(input.Event, official.Subject, official.HTML, input.Variables, nil)
			if err == nil {
				err = s.emailService.SendEmail(ctx, to, rendered.Subject, rendered.HTML)
			}
		}
		cancel()
		if err != nil {
			slog.Error("Failed to send balance alert", "user_id", userID, "error", err)
		}
	}
}

// SendQuotaAlertEmails sends the supplied provider dimension and usage observations to administrator recipients.
// The dimension chooses a canonical label or its own name; threshold kind chooses
// currency or whole-percent text, and negative computed remainder is displayed as zero.
// Each template attempt has its own timeout and current UTC-date reminder identity.
//
// When a shared service is available, successful or unclassified failed attempts
// end that recipient's work here. Only template/configuration-classified errors
// enter the raw built-in recipient list. Without the service, all recipients use
// that body. The direct sender logs each outcome; this adapter makes no decision
// about whether the caller's business quota threshold has actually been crossed.
func (s *AlertDelivery) SendQuotaAlertEmails(adminEmails []string, providerID int64, providerName, platform string, dim contract.QuotaDimension, used float64, siteName string) {
	dimLabel := quotaDimLabels[dim.Name]
	// Custom dimensions have no canonical bilingual label. Keep their supplied
	// name in template variables and the raw body so callers can still identify that quota observation.
	if dimLabel == "" {
		dimLabel = dim.Name
	}

	// Format the caller-selected threshold as currency or whole-percent text.
	thresholdDisplay := fmt.Sprintf("$%.2f", dim.Threshold)
	if dim.ThresholdType == thresholdTypePercentage {
		thresholdDisplay = fmt.Sprintf("%.0f%%", dim.Threshold)
	}
	remaining := dim.Limit - used
	// Overused quota displays no negative remaining credit amount.
	if remaining < 0 {
		remaining = 0
	}

	if s.notificationEmailService != nil {
		fallbackRecipients := make([]string, 0, len(adminEmails))
		for _, to := range adminEmails {
			// Each quota recipient gets a separate context budget for its template
			// attempt. Release this timer before collecting any classified raw-body fallback recipients.
			ctx, cancel := context.WithTimeout(context.Background(), emailSendTimeout)
			err := s.notificationEmailService.Send(ctx, NotificationEmailSendInput{
				Event:          NotificationEmailEventProviderQuotaAlert,
				RecipientEmail: to,
				RecipientName:  EmailRecipientName(to),
				SourceType:     "provider_quota",
				SourceID:       fmt.Sprintf("%d-%s", providerID, dim.Name),
				ReminderKey:    time.Now().UTC().Format("2006-01-02"),
				Variables: map[string]string{
					"provider_id":     strconv.FormatInt(providerID, 10),
					"provider_name":   providerName,
					"platform":        platform,
					"quota_dimension": dimLabel,
					"quota_used":      fmt.Sprintf("%.2f", used),
					"quota_limit":     fmt.Sprintf("%.2f", dim.Limit),
					"quota_remaining": fmt.Sprintf("%.2f", remaining),
					"quota_threshold": thresholdDisplay,
				},
			})
			cancel()
			if err != nil {
				// Classified template/configuration failures admit a different body path.
				// Other outcomes keep this recipient out of raw fallback because transport acceptance may already be uncertain.
				if ShouldFallbackNotificationEmail(err) {
					slog.Warn("template provider quota alert failed; falling back to built-in body", "to", to, "provider_id", providerID, "dimension", dim.Name, "err", err.Error())
					fallbackRecipients = append(fallbackRecipients, to)
				} else {
					slog.Warn("template provider quota alert delivery failed; not sending fallback to avoid duplicates", "to", to, "provider_id", providerID, "dimension", dim.Name, "err", err.Error())
				}
			}
		}
		if len(fallbackRecipients) == 0 {
			return
		}
		adminEmails = fallbackRecipients
	}

	subject := fmt.Sprintf("[%s] 提供商限额告警 / Provider Quota Alert - %s", SanitizeEmailHeader(siteName), SanitizeEmailHeader(providerName))
	body := s.BuildQuotaAlertEmailBody(providerID, html.EscapeString(providerName), html.EscapeString(platform), html.EscapeString(dimLabel), used, dim.Limit, remaining, thresholdDisplay, html.EscapeString(siteName))
	s.sendEmails(adminEmails, subject, body, "provider", providerName, "dimension", dim.Name)
}

// BuildBalanceLowEmailBody formats supplied balance values and an optional recharge button into built-in HTML.
// Callers escape userName and siteName before this direct formatting helper.
// The recharge URL is HTML-escaped here but not scheme-validated, and numeric
// amounts are rendered at two decimal places. The shared notification settings
// supply the balance symbol when available; standalone rendering uses dollars.
func (s *AlertDelivery) BuildBalanceLowEmailBody(userName string, balance, threshold float64, siteName, rechargeURL string) string {
	rechargeBlock := ""
	// A supplied recharge destination adds the optional button after HTML
	// escaping. This legacy body helper leaves destination-scheme validation to its caller.
	if rechargeURL != "" {
		rechargeBlock = fmt.Sprintf(`<a href="%s" class="recharge-btn">立即充值 / Top Up Now</a>`, html.EscapeString(rechargeURL))
	}
	symbol := "$"
	if s.notificationEmailService != nil {
		variables := s.notificationEmailService.sampleVariables(context.Background(), NotificationEmailEventBalanceLow, notificationEmailDefaultLocale)
		if configured := strings.TrimSpace(variables["balance_unit_symbol"]); configured != "" {
			symbol = configured
		}
	}
	// Account amounts share the same two-decimal display precision.
	formattedBalance := fmt.Sprintf("%s%.2f", html.EscapeString(symbol), balance)
	formattedThreshold := fmt.Sprintf("%s%.2f", html.EscapeString(symbol), threshold)
	return fmt.Sprintf(balanceLowEmailTemplate, siteName, userName, userName, formattedBalance, formattedThreshold, formattedThreshold, rechargeBlock)
}

// BuildQuotaAlertEmailBody formats provider identity, quota values, and threshold display into built-in HTML.
// Callers escape the supplied string fragments. A nonpositive limit uses the
// unlimited label; remaining is displayed as supplied, and this helper does not
// apply the caller's remainder clamp or threshold-kind selection.
func (s *AlertDelivery) BuildQuotaAlertEmailBody(providerID int64, providerName, platform, dimLabel string, used, limit, remaining float64, thresholdDisplay, siteName string) string {
	limitStr := fmt.Sprintf("$%.2f", limit)
	// Nonpositive configured limits use the raw body's unlimited label rather
	// than a currency amount, while usage and remainder stay the observations supplied by the caller.
	if limit <= 0 {
		limitStr = "无限制 / Unlimited"
	}
	return fmt.Sprintf(quotaAlertEmailTemplate, siteName, providerID, providerName, platform, dimLabel, used, limitStr, remaining, thresholdDisplay)
}

package notification

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
)

// risk.go formats moderation observations into violation and account-disabled
// email requests.
// Moderation notices format an already recorded log or session warning into
// event variables and source identity. The shared mailer resolves language and
// sends the selected notice; these adapters report the provided risk observation
// and do not choose whether a request is blocked or an account is disabled.

// defaultContentModerationBanThreshold supplies the legacy disabled-account
// body's displayed threshold when a supplied policy value is nonpositive.
const defaultContentModerationBanThreshold = 10

// RiskDelivery maps moderation observations into localized user notification requests through the mailer.
type RiskDelivery struct {
	emailService *Mailer // assembled event mailer
}

// NewRiskDelivery binds the event mailer used for moderation and session-risk user notices.
// Notice wrappers require this mailer when invoked. Construction retains the
// supplied pointer and performs no settings reads, credential work, or transport operations.
func NewRiskDelivery(mail *Mailer) *RiskDelivery {
	return &RiskDelivery{emailService: mail}
}

// SendViolationEmail sends a content-violation notice identified by the supplied risk log.
// log is required because its address is read before helper normalization. A nil
// policy supplies zero threshold variables; the mailer selects language and transport,
// and the source ID permits notification deduplication when the log ID is positive.
func (s *RiskDelivery) SendViolationEmail(ctx context.Context, cfg *contract.RiskPolicy, log *contract.RiskLog) error {
	return s.emailService.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventContentModerationViolation, RecipientEmail: log.UserEmail,
		RecipientName: EmailRecipientName(log.UserEmail), UserID: contentModerationEmailUserID(log),
		SourceType: "content_moderation", SourceID: contentModerationEmailSourceID(log), Variables: contentModerationEmailVariables(log, cfg),
	})
}

// SendAccountDisabledEmail sends the account-disabled event using the provided content-moderation log.
// log is required for recipient lookup; policy can be nil for zero threshold
// variables. This operation reports the caller's disabled outcome and returns the
// mailer's error without applying or undoing any account-status change.
func (s *RiskDelivery) SendAccountDisabledEmail(ctx context.Context, cfg *contract.RiskPolicy, log *contract.RiskLog) error {
	return s.emailService.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventContentModerationDisabled, RecipientEmail: log.UserEmail,
		RecipientName: EmailRecipientName(log.UserEmail), UserID: contentModerationEmailUserID(log),
		SourceType: "content_moderation", SourceID: contentModerationEmailSourceID(log), Variables: contentModerationEmailVariables(log, cfg),
	})
}

// SendCyberAccountDisabledEmail sends an account-disabled event with session-warning source identity.
// warning is required for its recipient. The optional policy supplies CyberBanThreshold;
// shared variable preparation uses the cyber category and its own source type so
// session and content logs with equal IDs remain distinct notification sources.
func (s *RiskDelivery) SendCyberAccountDisabledEmail(ctx context.Context, cfg *contract.RiskPolicy, warning *contract.RiskWarning) error {
	return s.emailService.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventContentModerationDisabled, RecipientEmail: warning.UserEmail,
		RecipientName: EmailRecipientName(warning.UserEmail), UserID: contentModerationCyberEmailUserID(warning),
		SourceType: "content_moderation_cyber", SourceID: contentModerationCyberEmailSourceID(warning), Variables: contentModerationCyberEmailVariables(warning, cfg),
	})
}

// contentModerationEmailUserID returns the log's pointed account identifier or zero when absent.
// It does not validate positivity; the assembled recipient-language reader treats
// positive IDs as account lookup and nonpositive values as email-based lookup input.
func contentModerationEmailUserID(log *contract.RiskLog) int64 {
	if log == nil || log.UserID == nil {
		return 0
	}
	return *log.UserID
}

// contentModerationEmailSourceID returns decimal source text for a positive recorded log ID.
// Nil logs and nonpositive IDs return empty, causing source-backed marker handling
// to be skipped when the wrapper supplies that missing identity to notification delivery.
func contentModerationEmailSourceID(log *contract.RiskLog) string {
	if log == nil || log.ID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", log.ID)
}

// contentModerationEmailVariables returns a new notice map from optional log and policy values.
// Missing fields use current UTC time, dash labels, and zero-valued numeric text.
// Nonzero log time and nonblank group/category override those defaults; score is
// formatted to three decimals and policy threshold is displayed as supplied.
func contentModerationEmailVariables(log *contract.RiskLog, cfg *contract.RiskPolicy) map[string]string {
	variables := map[string]string{
		"triggered_at":        time.Now().UTC().Format(time.RFC3339),
		"group_name":          "-",
		"moderation_category": "-",
		"moderation_score":    "0.000",
		"violation_count":     "0",
		"ban_threshold":       "0",
	}
	if log != nil {
		if !log.CreatedAt.IsZero() {
			variables["triggered_at"] = log.CreatedAt.UTC().Format(time.RFC3339)
		}
		if strings.TrimSpace(log.GroupName) != "" {
			variables["group_name"] = strings.TrimSpace(log.GroupName)
		}
		if strings.TrimSpace(log.HighestCategory) != "" {
			variables["moderation_category"] = strings.TrimSpace(log.HighestCategory)
		}
		// The notice rounds recorded moderation scores to three decimals.
		variables["moderation_score"] = fmt.Sprintf("%.3f", log.HighestScore)
		variables["violation_count"] = fmt.Sprintf("%d", log.ViolationCount)
	}
	if cfg != nil {
		variables["ban_threshold"] = fmt.Sprintf("%d", cfg.BanThreshold)
	}
	return variables
}

// contentModerationCyberEmailUserID returns the warning's pointed account identifier or zero when absent.
// The helper retains nonpositive pointed values, leaving the assembled recipient
// reader to choose email lookup when no positive account ID was supplied.
func contentModerationCyberEmailUserID(warning *contract.RiskWarning) int64 {
	if warning == nil || warning.UserID == nil {
		return 0
	}
	return *warning.UserID
}

// contentModerationCyberEmailSourceID returns decimal source text for a positive session-warning identifier.
// Nil or nonpositive input yields an empty identity, so its assembled notification
// request does not select source-key coordination or delivery-marker lookup.
func contentModerationCyberEmailSourceID(warning *contract.RiskWarning) string {
	if warning == nil || warning.ID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", warning.ID)
}

// contentModerationCyberEmailVariables returns a fresh session-notice variable map with cyber category.
// Nonzero warning time and nonblank group replace current UTC/dash defaults, while
// score and violation count remain zero text. A supplied policy replaces the
// cyber ban threshold; warning ViolationCount and ProviderName are not rendered by this helper.
func contentModerationCyberEmailVariables(warning *contract.RiskWarning, cfg *contract.RiskPolicy) map[string]string {
	variables := map[string]string{
		"triggered_at":        time.Now().UTC().Format(time.RFC3339),
		"group_name":          "-",
		"moderation_category": "cyber",
		"moderation_score":    "0.000",
		"violation_count":     "0",
		"ban_threshold":       "0",
	}
	if warning != nil {
		if !warning.CreatedAt.IsZero() {
			variables["triggered_at"] = warning.CreatedAt.UTC().Format(time.RFC3339)
		}
		if strings.TrimSpace(warning.GroupName) != "" {
			variables["group_name"] = strings.TrimSpace(warning.GroupName)
		}
	}
	if cfg != nil {
		variables["ban_threshold"] = fmt.Sprintf("%d", cfg.CyberBanThreshold)
	}
	return variables
}

// BuildContentModerationAccountDisabledEmailBody formats the legacy disabled-account HTML from supplied observations.
// A nil log returns empty. With a log, cfg is required; a nonpositive BanThreshold
// uses the built-in display threshold, and empty email can use the pointed user ID.
//
// The body escapes string observations and branding, formats score to three decimal
// places, and labels its notice time using current server-local time rather than
// log.CreatedAt. Formatting reports the provided outcome and leaves account state
// handling to the caller that already chose this notice.
func BuildContentModerationAccountDisabledEmailBody(siteName string, log *contract.RiskLog, cfg *contract.RiskPolicy) string {
	if log == nil {
		return ""
	}
	userName := strings.TrimSpace(log.UserEmail)
	// This legacy input has no separate display-name field. When email is blank,
	// the pointed account identifier still gives the recipient an account label in the notice.
	if userName == "" && log.UserID != nil {
		userName = fmt.Sprintf("UID %d", *log.UserID)
	}
	threshold := cfg.BanThreshold
	// The legacy body's nonpositive policy threshold uses its built-in display
	// value so the notice still shows a positive limit; the policy decision was made by its caller.
	if threshold <= 0 {
		threshold = defaultContentModerationBanThreshold
	}
	return fmt.Sprintf(`<!doctype html>
<html>
<body style="margin:0;padding:0;background:#f5f6fb;color:#222;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Arial,sans-serif;">
  <div style="max-width:680px;margin:0 auto;padding:32px 20px;">
    <div style="height:8px;background:#ef4444;border-radius:14px 14px 0 0;"></div>
    <div style="background:#fff;border-radius:0 0 14px 14px;padding:40px 48px;box-shadow:0 8px 28px rgba(15,23,42,.08);">
      <div style="letter-spacing:4px;color:#999;font-size:14px;text-transform:uppercase;">Risk Control / 账户封禁</div>
      <h1 style="margin:20px 0 28px;font-size:30px;line-height:1.25;">账户已被自动禁用</h1>
      <p style="font-size:17px;line-height:1.9;margin:0 0 24px;">尊敬的用户 <strong>%s</strong>，您的账户在计数周期内多次触发平台风控策略，系统已自动禁用该账户。详情如下。</p>
      <div style="background:#fff1f2;border:1px solid #fecdd3;border-radius:12px;padding:22px 28px;margin:28px 0;">
        <h2 style="margin:0 0 18px;color:#b91c1c;font-size:18px;">封禁详情</h2>
        <table style="width:100%%;border-collapse:collapse;font-size:16px;">
          <tr><td style="padding:12px 0;color:#888;border-bottom:1px solid #fee2e2;">封禁时间</td><td style="padding:12px 0;border-bottom:1px solid #fee2e2;">%s</td></tr>
          <tr><td style="padding:12px 0;color:#888;border-bottom:1px solid #fee2e2;">触发来源</td><td style="padding:12px 0;border-bottom:1px solid #fee2e2;">内容审核</td></tr>
          <tr><td style="padding:12px 0;color:#888;border-bottom:1px solid #fee2e2;">所属分组</td><td style="padding:12px 0;border-bottom:1px solid #fee2e2;">%s</td></tr>
          <tr><td style="padding:12px 0;color:#888;border-bottom:1px solid #fee2e2;">命中类别</td><td style="padding:12px 0;border-bottom:1px solid #fee2e2;">%s / %.3f</td></tr>
          <tr><td style="padding:12px 0;color:#888;">累计触发次数</td><td style="padding:12px 0;color:#dc2626;font-weight:700;">%d 次（阈值 %d）</td></tr>
        </table>
      </div>
      <div style="margin-top:24px;padding:18px 20px;border-radius:10px;background:#ff3b30;color:#fff;font-size:18px;font-weight:700;text-align:center;line-height:1.6;">账户当前处于封禁状态，所有 API 请求将被拒绝</div>
      <p style="font-size:15px;line-height:1.8;color:#666;margin-top:24px;">如需申诉或恢复账号，请联系平台管理员处理。</p>
      <p style="font-size:14px;line-height:1.8;color:#777;margin-top:28px;">此邮件由 %s 自动发送，请勿回复。</p>
    </div>
  </div>
</body>
</html>`,
		html.EscapeString(userName),
		html.EscapeString(time.Now().Format("2006-01-02 15:04:05")),
		html.EscapeString(defaultContentModerationString(log.GroupName, "-")),
		html.EscapeString(defaultContentModerationString(log.HighestCategory, "-")),
		log.HighestScore,
		log.ViolationCount,
		threshold,
		html.EscapeString(siteName),
	)
}

// defaultContentModerationString returns trimmed nonblank text or the supplied display fallback.
// It trims both for its emptiness decision and returned value, leaving caller
// fallback spelling unchanged when the observation has no usable display text.
func defaultContentModerationString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

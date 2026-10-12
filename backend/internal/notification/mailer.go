package notification

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// Mail delivery resolves SMTP configuration when invoked, so language-dependent
// sender names and stored connection changes apply to the current call. Event
// wrappers use a shared notification service for template and recipient choices;
// a bare SendEmail call delegates the supplied subject and body directly to SMTP.

// SMTP setting keys select one batched connection and sender configuration,
// including the password retained in storage and the localized sender-name companion.
const (
	SettingKeySMTPFrom     = "smtp_from"
	SettingKeySMTPFromName = "smtp_from_name"
	SettingKeySMTPHost     = "smtp_host"
	SettingKeySMTPPassword = "smtp_password"
	SettingKeySMTPPort     = "smtp_port"
	SettingKeySMTPUseTLS   = "smtp_use_tls"
	SettingKeySMTPUsername = "smtp_username"
)

// ErrEmailNotConfigured identifies a missing transport configuration for readiness
// callers, sharing the same application error as the SMTP configuration definition.
var ErrEmailNotConfigured = contract.ErrEmailNotConfigured

// Mailer reads SMTP settings and delegates configured sends or event-template delivery.
type Mailer struct {
	settingRepo SettingRepository // shared notification settings
	transport   SMTPTransport     // SMTP connection implementation
	// notificationEmailService is installed during assembly or by lazy event
	// sending. Configure one shared instance before concurrent event calls to
	// make their delivery-key coordination use that same service instance.
	notificationEmailService *NotificationEmailService
}

type (
	// SMTPConfig is the connection, authentication, sender, and TLS input for transport calls.
	SMTPConfig = contract.SMTPConfig
	// SMTPTransport performs configured delivery and readiness checks with request cancellation.
	SMTPTransport interface {
		// Send delivers the supplied HTML body using cfg and the request context.
		// Production SMTP reports success after DATA acceptance; subsequent cleanup
		// or QUIT failures do not replace an already acknowledged delivery result.
		Send(context.Context, *SMTPConfig, string, string, string) error
		// Test verifies configured connection and authentication using the supplied context.
		// Production transport shares its TLS negotiation with Send and treats
		// successful authentication as readiness even if the server rejects QUIT.
		Test(context.Context, *SMTPConfig) error
	}
)

// NewMailer binds shared settings and transport dependencies for later mail operations.
// Construction performs no reads or connections. Event sends can install a
// notification service later; plain sends use the supplied transport directly.
func NewMailer(repo SettingRepository, transport SMTPTransport) *Mailer {
	return &Mailer{settingRepo: repo, transport: transport}
}

// SetNotificationEmailService binds the shared event renderer and delivery coordinator to the mailer.
// Call during assembly before concurrent event sends; the field assignment is
// unsynchronized. A nil value permits the next event send to construct a service.
func (s *Mailer) SetNotificationEmailService(n *NotificationEmailService) {
	s.notificationEmailService = n
}

// SendEmail loads current SMTP settings and sends the supplied recipient, subject, and HTML body.
// An already canceled context aborts before loading configuration. Read and
// transport failures reach the caller. SendUserNotification owns event selection
// and delivery-key deduplication for the mailer's event wrappers.
func (s *Mailer) SendEmail(ctx context.Context, to, subject, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := s.GetSMTPConfig(ctx)
	if err != nil {
		return err
	}
	return s.transport.Send(ctx, cfg, to, subject, body)
}

// SendEmailWithConfigContext delegates a send using the caller's complete SMTP configuration.
// It performs no settings lookup or local validation. The selected transport
// handles context cancellation, address validation, connection errors, and delivery confirmation.
func (s *Mailer) SendEmailWithConfigContext(ctx context.Context, cfg *SMTPConfig, to, subject, body string) error {
	return s.transport.Send(ctx, cfg, to, subject, body)
}

// TestSMTPConnectionContext delegates readiness checking with the caller's SMTP configuration and context.
// It performs no stored-settings lookup; the selected transport determines
// connection, TLS negotiation, authentication, and cleanup behavior for the test.
func (s *Mailer) TestSMTPConnectionContext(ctx context.Context, cfg *SMTPConfig) error {
	return s.transport.Test(ctx, cfg)
}

// EmailRecipientName returns a trimmed local-part greeting for a supplied email string.
// Empty input returns empty. When an at-sign has no preceding local part, or
// is absent, the entire trimmed input is used as the greeting.
func EmailRecipientName(email string) string {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return ""
	}
	if at := strings.Index(trimmed, "@"); at > 0 {
		return trimmed[:at]
	}
	return trimmed
}

// GetSMTPConfig reads SMTP settings and returns ErrEmailNotConfigured when the host is empty.
// It trims connection, authentication, and address strings, resolves the sender
// name for the context language, and selects port 587 for absent or invalid
// decimal text. Parsed port integers are returned without range validation.
func (s *Mailer) GetSMTPConfig(ctx context.Context) (*SMTPConfig, error) {
	keys := []string{
		SettingKeySMTPHost,
		SettingKeySMTPPort,
		SettingKeySMTPUsername,
		SettingKeySMTPPassword,
		SettingKeySMTPFrom,
		SettingKeySMTPFromName,
		locale.TextSettingKey(SettingKeySMTPFromName),
		SettingKeySMTPUseTLS,
	}

	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("get smtp settings: %w", err)
	}

	host := strings.TrimSpace(settings[SettingKeySMTPHost])
	if host == "" {
		return nil, ErrEmailNotConfigured
	}

	port := 587 // default submission port
	if portStr := settings[SettingKeySMTPPort]; portStr != "" {
		// A malformed stored port keeps the submission default so configuration
		// loading can still produce connection input; transport dialing reports any later endpoint failure.
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	useTLS := settings[SettingKeySMTPUseTLS] == "true"

	return &SMTPConfig{
		Host:     host,
		Port:     port,
		Username: strings.TrimSpace(settings[SettingKeySMTPUsername]),
		Password: strings.TrimSpace(settings[SettingKeySMTPPassword]),
		From:     strings.TrimSpace(settings[SettingKeySMTPFrom]),
		FromName: locale.ResolveSettingText(settings, SettingKeySMTPFromName, "", locale.FromContext(ctx)),
		UseTLS:   useTLS,
	}, nil
}

// SanitizeEmailHeader removes CR and LF to prevent additional header injection.
// It joins text around removed separators so MIME preparation sees one header
// value while every other character keeps its supplied spelling.
func SanitizeEmailHeader(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }

// CheckTransport checks stored SMTP configuration; connection and delivery happen in later calls.
// A nil service, missing sender, or sender without GetSMTPConfig returns
// ErrEmailNotConfigured. An available configuration reader determines readiness;
// this check does not establish that SMTP dialing or authentication will succeed.
func (s *NotificationEmailService) CheckTransport(ctx context.Context) error {
	if s == nil || s.emailService == nil {
		return ErrEmailNotConfigured
	}
	source, ok := s.emailService.(interface {
		// GetSMTPConfig returns configuration or an error for transport availability.
		// CheckTransport forwards this reader's error as its readiness result;
		// SMTP connection and authentication remain the sender's later operations.
		GetSMTPConfig(context.Context) (*SMTPConfig, error)
	})
	if !ok {
		return ErrEmailNotConfigured
	}
	_, err := source.GetSMTPConfig(ctx)
	return err
}

// SendUserNotification sends through the notification service's locale and template selection.
// A missing bound service constructs and binds one through NewNotificationEmailService.
// Assembly should provide the shared service before concurrent sends so the
// lazy path does not create separate delivery coordinators for initial calls.
func (s *Mailer) SendUserNotification(ctx context.Context, input SendRequest) error {
	sender := s.notificationEmailService
	if sender == nil {
		// Standalone mailers can render event templates using their existing settings
		// and direct SMTP sender. Construction binds this service back to the mailer for later event calls.
		sender = NewNotificationEmailService(s.settingRepo, s)
	}
	return sender.Send(ctx, input)
}

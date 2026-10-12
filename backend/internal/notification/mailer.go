package notification

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

const (
	SettingKeySMTPFrom     = "smtp_from"
	SettingKeySMTPFromName = "smtp_from_name"
	SettingKeySMTPHost     = "smtp_host"
	SettingKeySMTPPassword = "smtp_password"
	SettingKeySMTPPort     = "smtp_port"
	SettingKeySMTPUseTLS   = "smtp_use_tls"
	SettingKeySMTPUsername = "smtp_username"
)

var ErrEmailNotConfigured = contract.ErrEmailNotConfigured

type Mailer struct {
	settingRepo              SettingRepository
	transport                SMTPTransport
	notificationEmailService *NotificationEmailService
}

type (
	SMTPConfig    = contract.SMTPConfig
	SMTPTransport interface {
		Send(context.Context, *SMTPConfig, string, string, string) error
		Test(context.Context, *SMTPConfig) error
	}
)

func NewMailer(repo SettingRepository, transport SMTPTransport) *Mailer {
	return &Mailer{settingRepo: repo, transport: transport}
}

func (s *Mailer) SetNotificationEmailService(n *NotificationEmailService) {
	s.notificationEmailService = n
}

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

func (s *Mailer) SendEmailWithConfigContext(ctx context.Context, cfg *SMTPConfig, to, subject, body string) error {
	return s.transport.Send(ctx, cfg, to, subject, body)
}

func (s *Mailer) TestSMTPConnectionContext(ctx context.Context, cfg *SMTPConfig) error {
	return s.transport.Test(ctx, cfg)
}

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
func SanitizeEmailHeader(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }

// CheckTransport checks stored SMTP configuration; connection and delivery happen in later calls.
func (s *NotificationEmailService) CheckTransport(ctx context.Context) error {
	if s == nil || s.emailService == nil {
		return ErrEmailNotConfigured
	}
	source, ok := s.emailService.(interface {
		GetSMTPConfig(context.Context) (*SMTPConfig, error)
	})
	if !ok {
		return ErrEmailNotConfigured
	}
	_, err := source.GetSMTPConfig(ctx)
	return err
}

// SendUserNotification sends through the notification service's locale and template selection.
func (s *Mailer) SendUserNotification(ctx context.Context, input SendRequest) error {
	sender := s.notificationEmailService
	if sender == nil {
		sender = NewNotificationEmailService(s.settingRepo, s)
	}
	return sender.Send(ctx, input)
}

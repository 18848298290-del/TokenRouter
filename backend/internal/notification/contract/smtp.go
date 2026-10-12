package contract

import "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"

var ErrEmailNotConfigured = apperror.ServiceUnavailable("EMAIL_NOT_CONFIGURED", "email service not configured")

// SMTPConfig contains SMTP connection, authentication, and sender settings.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	UseTLS   bool
}

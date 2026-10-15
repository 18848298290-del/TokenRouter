package contract

import "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"

// smtp.go defines SMTP connection settings and the missing-configuration
// error.
// SMTP delivery receives host, authentication, sender, and TLS choices as one
// configuration value. The transport uses the same configuration for connection
// tests and actual delivery. Mailer's stored-settings reader reports a missing
// host before invoking the transport with that configuration.

// ErrEmailNotConfigured identifies an unavailable mailer configuration so callers
// can report service readiness failure before attempting an SMTP delivery.
var ErrEmailNotConfigured = apperror.ServiceUnavailable("EMAIL_NOT_CONFIGURED", "email service not configured")

// SMTPConfig contains SMTP connection, authentication, and sender settings.
type SMTPConfig struct {
	Host string // SMTP server host
	Port int    // TCP port number
	// Username and Password are passed to PLAIN authentication on every Send
	// and Test, including empty strings; net/smtp determines which transports
	// may carry these supplied credentials.
	Username string // SMTP authentication account
	Password string // SMTP authentication secret
	// From and FromName determine both sender representations. From is parsed
	// for its bare envelope address and optional display name; a nonblank
	// sanitized FromName replaces that name in the MIME From header.
	From     string // envelope sender address
	FromName string // displayed sender name
	// UseTLS selects implicit TLS with a required-STARTTLS compatibility path
	// in the transport; false selects a connection with opportunistic STARTTLS.
	UseTLS bool
}

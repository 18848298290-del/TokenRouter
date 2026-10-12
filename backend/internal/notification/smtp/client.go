package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
)

const (
	smtpDialTimeout = 10 * time.Second
	smtpIOTimeout   = 20 * time.Second
)

var smtpTestRootCAs *x509.CertPool

type (
	Client     struct{}
	SMTPConfig = contract.SMTPConfig
)

// contextConn closes the connection on cancellation and unregisters the callback on normal close.
type contextConn struct {
	net.Conn
	stop     func() bool
	deadline time.Time
}

func New() *Client { return &Client{} }

func sanitizeEmailHeader(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }

// Send sends an email using the supplied configuration.
func (s *Client) Send(ctx context.Context, config *SMTPConfig, to, subject, body string) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil && ctx.Err() != nil {
			resultErr = errors.Join(resultErr, ctx.Err())
		}
	}()
	message, err := buildSMTPMessage(config, to, subject, body)
	if err != nil {
		return err
	}

	client, err := s.connectSMTPContext(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	auth := smtp.PlainAuth("", config.Username, config.Password, config.Host)
	if err = client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err = client.Mail(message.envelopeFrom); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err = client.Rcpt(message.envelopeTo); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err = w.Write(message.data); err != nil {
		return fmt.Errorf("write msg: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("close writer: %w", err)
	}
	// A successful DATA acknowledgement confirms acceptance; a later QUIT failure must not trigger a resend.
	_ = client.Quit()
	return nil
}

func smtpTLSConfig(host string) *tls.Config {
	return &tls.Config{
		ServerName: host,
		// Require TLS 1.2 or newer to exclude weaker protocol versions.
		MinVersion: tls.VersionTLS12,
		RootCAs:    smtpTestRootCAs,
	}
}

// connectSMTPContext establishes the SMTP session shared by sending and connection tests.
// UseTLS first attempts implicit TLS. A plaintext record triggers a new connection with mandatory
// STARTTLS. With UseTLS disabled, STARTTLS is used when the server advertises it.
func (s *Client) connectSMTPContext(ctx context.Context, config *SMTPConfig) (*smtp.Client, error) {
	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)
	dialer := &net.Dialer{Timeout: smtpDialTimeout}
	tlsConfig := smtpTLSConfig(config.Host)

	if config.UseTLS {
		dialCtx, cancel := context.WithTimeout(ctx, smtpDialTimeout)
		raw, err := dialer.DialContext(dialCtx, "tcp", addr)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("tls dial: %w", err)
		}
		bound := bindCancellation(ctx, raw)
		conn := tls.Client(bound, tlsConfig)
		err = conn.HandshakeContext(dialCtx)
		cancel()
		if err != nil {
			_ = conn.Close()
		}
		if err == nil {
			return newSMTPClient(conn, config.Host)
		}
		if _, ok := errors.AsType[tls.RecordHeaderError](err); !ok {
			return nil, fmt.Errorf("tls dial: %w", err)
		}
		// A plaintext SMTP greeting causes the TLS handshake to return RecordHeaderError.
		return s.connectSMTPStartTLS(ctx, dialer, addr, config.Host, tlsConfig, true)
	}

	return s.connectSMTPStartTLS(ctx, dialer, addr, config.Host, tlsConfig, false)
}

// connectSMTPStartTLS upgrades a plaintext connection when supported; mandatory rejects servers without STARTTLS.
func (s *Client) connectSMTPStartTLS(ctx context.Context, dialer *net.Dialer, addr, host string, tlsConfig *tls.Config, mandatory bool) (*smtp.Client, error) {
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smtp dial: %w", err)
	}
	client, err := newSMTPClient(bindCancellation(ctx, conn), host)
	if err != nil {
		return nil, err
	}
	if ok, _ := client.Extension("STARTTLS"); !ok {
		if mandatory {
			_ = client.Close()
			return nil, errors.New("smtp server does not support STARTTLS")
		}
		return client, nil
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("starttls: %w", err)
	}
	return client, nil
}

func newSMTPClient(conn net.Conn, host string) (*smtp.Client, error) {
	_ = conn.SetDeadline(time.Now().Add(smtpIOTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("new smtp client: %w", err)
	}
	return client, nil
}

// Test checks connection and authentication using the same TLS negotiation as Send.
func (s *Client) Test(ctx context.Context, config *SMTPConfig) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil && ctx.Err() != nil {
			resultErr = errors.Join(resultErr, ctx.Err())
		}
	}()
	client, err := s.connectSMTPContext(ctx, config)
	if err != nil {
		return fmt.Errorf("smtp connection failed: %w", err)
	}
	defer func() { _ = client.Close() }()

	auth := smtp.PlainAuth("", config.Username, config.Password, config.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp authentication failed: %w", err)
	}

	// Connection and authentication succeeded; the test ignores nonstandard QUIT responses.
	_ = client.Quit()
	return nil
}

func (c *contextConn) Close() error {
	if c.stop != nil {
		c.stop()
	}
	return c.Conn.Close()
}

func bindCancellation(ctx context.Context, conn net.Conn) net.Conn {
	wrapped := &contextConn{Conn: conn}
	wrapped.deadline, _ = ctx.Deadline()
	wrapped.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return wrapped
}

// SetDeadline caps the SMTP I/O deadline at the request deadline.
func (c *contextConn) SetDeadline(deadline time.Time) error {
	if !c.deadline.IsZero() && (deadline.IsZero() || c.deadline.Before(deadline)) {
		deadline = c.deadline
	}
	return c.Conn.SetDeadline(deadline)
}

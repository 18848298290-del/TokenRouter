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

// client.go sends SMTP messages and tests connections with TLS negotiation and
// context-bound transport lifetimes.
// SMTP sending treats successful DATA acknowledgement as the acceptance point.
// Cancellation or rejected QUIT after that point leaves success intact, while
// failures before acceptance retain their transport error and any observed
// request cancellation. Sending and testing share connection negotiation.
//
// UseTLS first attempts implicit TLS and allows fresh required-STARTTLS setup
// after a record-header failure. Other handshake failures stop that path.
// Cancellation closes the wrapped socket, and I/O deadline selection retains
// the earlier request deadline and SMTP limit. Normal close unregisters future
// callback work while a callback already running may finish its socket close.

const (
	// Dial/implicit handshake and SMTP greeting/I/O budgets bound setup phases;
	// contextConn retains any earlier caller deadline when later I/O limits are installed.
	smtpDialTimeout = 10 * time.Second
	smtpIOTimeout   = 20 * time.Second
)

// smtpTestRootCAs supplies test trust roots to constructed TLS configurations.
// Nil uses system trust. Tests set and restore this shared pointer before their
// calls; changing it is not a synchronized runtime configuration operation.
var smtpTestRootCAs *x509.CertPool

type (
	// Client performs context-aware SMTP sending and testing through shared connection negotiation.
	Client struct{}
	// SMTPConfig is the connection, sender, authentication, and TLS input supplied by notification callers.
	SMTPConfig = contract.SMTPConfig
)

// contextConn carries cancellation closure and caller deadline rules across SMTP and TLS socket wrappers.
type contextConn struct {
	net.Conn // wrapped transport socket
	// stop unregisters future callback work during Close; a cancellation callback
	// that already began can still finish closing the underlying socket independently.
	stop func() bool
	// deadline retains the caller's request deadline so a later I/O limit cannot
	// extend socket work beyond that earlier fixed context deadline.
	deadline time.Time
}

// New returns the stateless SMTP transport used by assembled notification mailers.
func New() *Client { return &Client{} }

// sanitizeEmailHeader removes carriage returns and newlines from displayed message-header text.
// It joins the surrounding text after separator removal so MIME preparation
// receives one header value while every other character keeps its supplied spelling.
func sanitizeEmailHeader(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }

// Send builds MIME content and reports SMTP delivery outcome under the request context.
// An already canceled context returns before preparation. Keep configuration stable
// through this synchronous call; parsing and transport failures retain their error,
// with a context error joined when cancellation is observed on failure.
//
// DATA writer Close selects the acceptance result. After it succeeds, QUIT and
// cleanup errors are discarded and success remains even if cancellation arrives
// then. Before that acknowledgement, a send error can describe uncertain delivery;
// callers decide retries from that outcome rather than assuming no bytes were accepted.
func (s *Client) Send(ctx context.Context, config *SMTPConfig, to, subject, body string) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A canceled socket can report protocol failure first. Join observed context
	// cancellation on failure while keeping an already selected successful outcome nil.
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
	// This call owns session cleanup until return. A close error is a cleanup
	// outcome and does not replace the protocol result already selected by this operation.
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
	// DATA acknowledgement already selected success. Ignore final QUIT failure
	// so cancellation or a nonstandard reply does not invite retrying this accepted message.
	_ = client.Quit()
	return nil
}

// smtpTLSConfig returns certificate-verified TLS settings using host for server-name matching.
// TLS 1.2 is the minimum version. A nil test pool selects system roots, and
// constructed configs retain the current package-test pool pointer when provided.
func smtpTLSConfig(host string) *tls.Config {
	return &tls.Config{
		ServerName: host,
		// Require TLS 1.2 or newer to exclude weaker protocol versions.
		MinVersion: tls.VersionTLS12,
		RootCAs:    smtpTestRootCAs,
	}
}

// connectSMTPContext returns an SMTP session using the configured host, port, and TLS mode.
// UseTLS selects implicit TLS with a bounded dial/handshake context. A record-header
// failure allows a fresh connection using mandatory STARTTLS; other TLS failures
// return without selecting that compatibility path.
//
// The failed socket closes before replacement setup. Successful implicit TLS runs
// SMTP initialization on that socket. UseTLS false permits plaintext setup and
// upgrades when advertised; required STARTTLS rejects an absent extension. Both
// paths retain cancellation closure and request deadline limits through their socket wrappers.
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
		// The handshake shares the bounded dial context while socket cancellation
		// remains tied to ctx. Release the setup-budget timer when this handshake attempt finishes.
		err = conn.HandshakeContext(dialCtx)
		cancel()
		// Release failed implicit TLS before choosing replacement setup. The closed
		// socket cannot be reused as plaintext by the later required-STARTTLS compatibility attempt.
		if err != nil {
			_ = conn.Close()
		}
		if err == nil {
			return newSMTPClient(conn, config.Host)
		}
		if _, ok := errors.AsType[tls.RecordHeaderError](err); !ok {
			return nil, fmt.Errorf("tls dial: %w", err)
		}
		// Record-header failure is the compatibility trigger, including a plaintext
		// greeting. Reconnect using mandatory STARTTLS so this path still requires an encrypted session.
		return s.connectSMTPStartTLS(ctx, dialer, addr, config.Host, tlsConfig, true)
	}

	return s.connectSMTPStartTLS(ctx, dialer, addr, config.Host, tlsConfig, false)
}

// connectSMTPStartTLS returns context-bound SMTP, upgrading when the server advertises STARTTLS.
// Mandatory mode closes and rejects a session lacking that extension. Optional
// mode can return plaintext. The caller's PLAIN authentication rejects an
// unencrypted session unless its host is exactly localhost, 127.0.0.1, or ::1;
// advertised authentication support does not relax that net/smtp restriction.
func (s *Client) connectSMTPStartTLS(ctx context.Context, dialer *net.Dialer, addr, host string, tlsConfig *tls.Config, mandatory bool) (*smtp.Client, error) {
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smtp dial: %w", err)
	}
	client, err := newSMTPClient(bindCancellation(ctx, conn), host)
	if err != nil {
		return nil, err
	}
	// Extension probing reduces failed greeting/feature discovery to no support.
	// Mandatory mode rejects that result; optional mode leaves the initialized session for its later authentication.
	if ok, _ := client.Extension("STARTTLS"); !ok {
		if mandatory {
			// Required encryption cannot proceed with this server's missing extension.
			// Close the opened client before returning the unsupported-STARTTLS result to its caller.
			_ = client.Close()
			return nil, errors.New("smtp server does not support STARTTLS")
		}
		return client, nil
	}
	// A failed TLS upgrade leaves the requested setup incomplete. Close this
	// session before returning so its caller cannot authenticate through the failed upgraded connection.
	if err := client.StartTLS(tlsConfig); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("starttls: %w", err)
	}
	return client, nil
}

// newSMTPClient installs an I/O deadline and initializes SMTP on the supplied socket.
// Deadline-setting errors are ignored. Failed initialization closes the socket and
// returns a wrapped error; success transfers cleanup to the caller owning the returned client.
func newSMTPClient(conn net.Conn, host string) (*smtp.Client, error) {
	// contextConn caps this I/O limit at an earlier caller deadline. Ignore
	// deadline-installation errors here and let initialization report its resulting protocol or socket outcome.
	_ = conn.SetDeadline(time.Now().Add(smtpIOTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		// Failed greeting initialization leaves no client owner for this socket.
		// Close it here and retain the original protocol failure as the returned setup error.
		_ = conn.Close()
		return nil, fmt.Errorf("new smtp client: %w", err)
	}
	return client, nil
}

// Test verifies connection and authentication through the negotiation shared with sending.
// Supply a non-nil stable configuration. Canceled failed setup or authentication
// joins the observed context error; successful authentication keeps a nil result
// even when QUIT later fails or cancellation arrives during final protocol cleanup.
// PLAIN authentication is attempted with the supplied username and password,
// including empty strings; the server and net/smtp determine whether it succeeds.
func (s *Client) Test(ctx context.Context, config *SMTPConfig) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A canceled socket can report protocol failure first. Join observed context
	// cancellation on failure while keeping an already selected successful outcome nil.
	defer func() {
		if resultErr != nil && ctx.Err() != nil {
			resultErr = errors.Join(resultErr, ctx.Err())
		}
	}()
	client, err := s.connectSMTPContext(ctx, config)
	if err != nil {
		return fmt.Errorf("smtp connection failed: %w", err)
	}
	// This call owns session cleanup until return. A close error is a cleanup
	// outcome and does not replace the protocol result already selected by this operation.
	defer func() { _ = client.Close() }()

	auth := smtp.PlainAuth("", config.Username, config.Password, config.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp authentication failed: %w", err)
	}

	// Authentication selected readiness success. Ignore QUIT's outcome as final
	// cleanup, matching the send path after it has received its DATA acceptance acknowledgement.
	_ = client.Quit()
	return nil
}

// Close unregisters future cancellation callback work and closes the wrapped transport socket.
// A callback already running may close it too. The unregister result is discarded,
// and the underlying socket's close result is returned for the caller's cleanup handling.
func (c *contextConn) Close() error {
	if c.stop != nil {
		c.stop()
	}
	return c.Conn.Close()
}

// bindCancellation returns a socket wrapper retaining request cancellation and its fixed deadline.
// It registers closure of the original socket on cancellation and installs an
// initial request deadline when present. Later SetDeadline calls choose the earlier
// SMTP limit and the remembered request deadline.
//
// Normal Close unregisters future callback work before closing that same socket.
// Cancellation can overlap normal cleanup, so its callback ignores close errors.
// The wrapper supplies I/O interruption; Send and Test own the operation error
// result and can attach cancellation when their protocol operation fails.
func bindCancellation(ctx context.Context, conn net.Conn) net.Conn {
	wrapped := &contextConn{Conn: conn}
	wrapped.deadline, _ = ctx.Deadline()
	// Close the original socket to interrupt blocked protocol I/O on cancellation.
	// Ignore its cleanup result because the waiting SMTP operation owns the reported context/network failure.
	wrapped.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	if deadline, ok := ctx.Deadline(); ok {
		// Install the caller deadline before protocol setup when possible. A failed
		// installation still leaves cancellation-driven socket closure registered for this request.
		_ = conn.SetDeadline(deadline)
	}
	return wrapped
}

// SetDeadline installs the earlier request deadline or requested I/O deadline on the wrapped socket.
// A zero requested deadline retains an existing request deadline. With no remembered
// caller deadline, the input passes through, including zero to clear the I/O limit.
//
// The context deadline was captured before SMTP setup and stays fixed even when
// later code replaces an I/O deadline. Capping those replacements keeps the
// caller's request limit effective; the underlying deadline-setting result reaches
// the caller for its operation's error handling.
func (c *contextConn) SetDeadline(deadline time.Time) error {
	// An I/O reset may otherwise extend or clear the socket's request limit.
	// Cap it at the captured context deadline before passing it to the underlying connection.
	if !c.deadline.IsZero() && (deadline.IsZero() || c.deadline.Before(deadline)) {
		deadline = c.deadline
	}
	return c.Conn.SetDeadline(deadline)
}

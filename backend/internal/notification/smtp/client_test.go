package smtp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// fakeSMTPServer supports implicit TLS, STARTTLS, and plaintext SMTP client tests.
type fakeSMTPServer struct {
	listener          net.Listener
	tlsConfig         *tls.Config
	advertiseStartTLS bool

	mu       sync.Mutex
	commands []string
	conns    atomic.Int64
	wg       sync.WaitGroup
}

// TestSMTPAcknowledgementCancellationBoundary verifies success when cancellation occurs during QUIT after a
// DATA acknowledgement.
// Without a DATA acknowledgement, delivery remains uncertain.
func TestSMTPAcknowledgementCancellationBoundary(t *testing.T) {
	for _, ack := range []bool{true, false} {
		name := "unknown-without-ack"
		if ack {
			name = "cancel-after-ack"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer func() { _ = listener.Close() }()
			address, ok := listener.Addr().(*net.TCPAddr)
			require.True(t, ok)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var messages atomic.Int64
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
				write := func(value string) bool {
					if _, err := rw.WriteString(value + "\r\n"); err != nil {
						return false
					}
					return rw.Flush() == nil
				}
				if !write("220 localhost ESMTP") {
					return
				}
				for {
					line, err := rw.ReadString('\n')
					if err != nil {
						return
					}
					command := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(command, "EHLO"):
						if !write("250-localhost\r\n250 AUTH PLAIN") {
							return
						}
					case strings.HasPrefix(command, "AUTH"):
						if !write("235 authenticated") {
							return
						}
					case strings.HasPrefix(command, "MAIL"), strings.HasPrefix(command, "RCPT"):
						if !write("250 OK") {
							return
						}
					case command == "DATA":
						if !write("354 send data") {
							return
						}
						for {
							line, err = rw.ReadString('\n')
							if err != nil {
								return
							}
							if strings.TrimSpace(line) == "." {
								break
							}
						}
						messages.Add(1)
						if !ack {
							return
						}
						if !write("250 accepted") {
							return
						}
					case command == "QUIT":
						cancel()
						_ = write("500 nonstandard quit")
						return
					}
				}
			}()
			err = New().Send(ctx, &SMTPConfig{Host: "127.0.0.1", Port: address.Port, Username: "fixture", Password: "fixture", From: "sender@example.com"}, "recipient@example.com", "fixture", "fixture")
			<-finished
			require.Equal(t, int64(1), messages.Load())
			if ack {
				require.NoError(t, err)
				require.ErrorIs(t, ctx.Err(), context.Canceled)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestSMTPConnectionImplicitTLS checks implicit TLS with UseTLS enabled.
func TestSMTPConnectionImplicitTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, true, false)
	svc := &Client{}

	if err := svc.Test(context.Background(), smtpTestConfig(port, true)); err != nil {
		t.Fatalf("expected implicit TLS connection to succeed, got: %v", err)
	}
	if !srv.sawCommand("EHLO") {
		t.Fatal("expected server to receive EHLO")
	}
}

// TestSMTPConnectionStartTLSFallbackWhenTLSEnabled checks that a plaintext greeting with UseTLS enabled
// causes a new connection that requires STARTTLS.
func TestSMTPConnectionStartTLSFallbackWhenTLSEnabled(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &Client{}

	if err := svc.Test(context.Background(), smtpTestConfig(port, true)); err != nil {
		t.Fatalf("expected STARTTLS fallback to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected server to receive STARTTLS command")
	}
	if got := srv.conns.Load(); got < 2 {
		t.Fatalf("expected implicit TLS attempt before STARTTLS fallback (>=2 connections), got %d", got)
	}
}

// TestSMTPConnectionMandatoryStartTLSRefusesPlaintext checks that UseTLS rejects a server without STARTTLS
// before credentials are sent.
func TestSMTPConnectionMandatoryStartTLSRefusesPlaintext(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	svc := &Client{}

	err := svc.Test(context.Background(), smtpTestConfig(port, true))
	if err == nil {
		t.Fatal("expected error when server does not support STARTTLS")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS-related error, got: %v", err)
	}
	if srv.sawCommand("AUTH") {
		t.Fatal("credentials must not be sent over plaintext when TLS is required")
	}
}

// TestSMTPConnectionOpportunisticStartTLSWhenTLSDisabled checks that advertised STARTTLS is used before
// authentication.
func TestSMTPConnectionOpportunisticStartTLSWhenTLSDisabled(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &Client{}

	if err := svc.Test(context.Background(), smtpTestConfig(port, false)); err != nil {
		t.Fatalf("expected opportunistic STARTTLS test connection to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected test connection to upgrade via STARTTLS like the send path")
	}
}

// TestSMTPConnectionPlainWhenNoStartTLS checks plaintext operation when UseTLS is disabled and STARTTLS is
// unavailable.
func TestSMTPConnectionPlainWhenNoStartTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	svc := &Client{}

	if err := svc.Test(context.Background(), smtpTestConfig(port, false)); err != nil {
		t.Fatalf("expected plain connection to succeed, got: %v", err)
	}
	if srv.sawCommand("STARTTLS") {
		t.Fatal("did not expect STARTTLS command when server does not advertise it")
	}
}

// TestSendEmailWithConfigStartTLSFallback checks MAIL, RCPT, and DATA over STARTTLS with UseTLS enabled.
func TestSendEmailWithConfigStartTLSFallback(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &Client{}

	err := svc.Send(context.Background(), smtpTestConfig(port, true), "rcpt@example.com", "subject", "<p>body</p>")
	if err != nil {
		t.Fatalf("expected send via STARTTLS fallback to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected send path to upgrade via STARTTLS")
	}
	if !srv.sawCommand("DATA") {
		t.Fatal("expected send path to reach DATA")
	}
}

// TestSendEmailWithConfigImplicitTLS checks delivery over implicit TLS with UseTLS enabled.
func TestSendEmailWithConfigImplicitTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, true, false)
	svc := &Client{}

	err := svc.Send(context.Background(), smtpTestConfig(port, true), "rcpt@example.com", "subject", "<p>body</p>")
	if err != nil {
		t.Fatalf("expected send via implicit TLS to succeed, got: %v", err)
	}
	if !srv.sawCommand("DATA") {
		t.Fatal("expected send path to reach DATA")
	}
}

// newSMTPTestCert creates a self-signed certificate for 127.0.0.1/localhost and its trust pool.
func newSMTPTestCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, pool
}

func startFakeSMTPServer(t *testing.T, implicitTLS, advertiseStartTLS bool) (*fakeSMTPServer, int) {
	t.Helper()
	cert, pool := newSMTPTestCert(t)
	prevPool := smtpTestRootCAs
	smtpTestRootCAs = pool
	t.Cleanup(func() { smtpTestRootCAs = prevPool })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &fakeSMTPServer{
		listener:          listener,
		tlsConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		advertiseStartTLS: advertiseStartTLS,
	}
	if implicitTLS {
		srv.listener = tls.NewListener(listener, srv.tlsConfig)
	}
	t.Cleanup(func() {
		_ = srv.listener.Close()
		srv.wg.Wait()
	})

	srv.wg.Go(func() {
		for {
			conn, err := srv.listener.Accept()
			if err != nil {
				return
			}
			srv.conns.Add(1)
			srv.wg.Go(func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				srv.serve(conn, srv.advertiseStartTLS)
			})
		}
	})

	port := testassert.MustType[*net.TCPAddr](listener.Addr()).Port
	return srv, port
}

func (srv *fakeSMTPServer) record(cmd string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.commands = append(srv.commands, cmd)
}

func (srv *fakeSMTPServer) sawCommand(prefix string) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, cmd := range srv.commands {
		if strings.HasPrefix(strings.ToUpper(cmd), prefix) {
			return true
		}
	}
	return false
}

func (srv *fakeSMTPServer) serve(conn net.Conn, allowStartTLS bool) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	writeLine := func(line string) bool {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	if !writeLine("220 fake.test ESMTP ready") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		srv.record(cmd)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			ok := writeLine("250-fake.test")
			if allowStartTLS {
				ok = ok && writeLine("250-STARTTLS")
			}
			if !ok || !writeLine("250-AUTH PLAIN LOGIN") || !writeLine("250 8BITMIME") {
				return
			}
		case upper == "STARTTLS" && allowStartTLS:
			if !writeLine("220 2.0.0 ready to start TLS") {
				return
			}
			tlsConn := tls.Server(conn, srv.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			srv.serveUpgraded(tlsConn)
			return
		case strings.HasPrefix(upper, "AUTH"):
			if !writeLine("235 2.7.0 authentication successful") {
				return
			}
		case strings.HasPrefix(upper, "MAIL"), strings.HasPrefix(upper, "RCPT"):
			if !writeLine("250 ok") {
				return
			}
		case upper == "DATA":
			if !writeLine("354 go ahead") {
				return
			}
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			if !writeLine("250 message accepted") {
				return
			}
		case upper == "QUIT":
			_ = writeLine("221 bye")
			return
		default:
			if !writeLine("250 ok") {
				return
			}
		}
	}
}

// serveUpgraded handles commands after a STARTTLS upgrade.
func (srv *fakeSMTPServer) serveUpgraded(conn net.Conn) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	// net/smtp sends EHLO again after StartTLS succeeds, so no greeting is needed here.
	srv.serveCommands(reader, writer)
}

func (srv *fakeSMTPServer) serveCommands(reader *bufio.Reader, writer *bufio.Writer) {
	writeLine := func(line string) bool {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		srv.record(cmd)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			if !writeLine("250-fake.test") || !writeLine("250-AUTH PLAIN LOGIN") || !writeLine("250 8BITMIME") {
				return
			}
		case strings.HasPrefix(upper, "AUTH"):
			if !writeLine("235 2.7.0 authentication successful") {
				return
			}
		case strings.HasPrefix(upper, "MAIL"), strings.HasPrefix(upper, "RCPT"):
			if !writeLine("250 ok") {
				return
			}
		case upper == "DATA":
			if !writeLine("354 go ahead") {
				return
			}
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			if !writeLine("250 message accepted") {
				return
			}
		case upper == "QUIT":
			_ = writeLine("221 bye")
			return
		default:
			if !writeLine("250 ok") {
				return
			}
		}
	}
}

func smtpTestConfig(port int, useTLS bool) *SMTPConfig {
	return &SMTPConfig{
		Host:     "127.0.0.1",
		Port:     port,
		Username: "user",
		Password: "pass",
		From:     "noreply@example.com",
		FromName: "Test",
		UseTLS:   useTLS,
	}
}

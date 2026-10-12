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

// fakeSMTPServer records commands and serves controlled TLS/plaintext sessions for transport assertions.
type fakeSMTPServer struct {
	listener          net.Listener // fixture connection listener
	tlsConfig         *tls.Config  // fixture TLS certificate
	advertiseStartTLS bool         // advertised upgrade capability

	// mu protects commands recorded by independently accepted connections, while
	// conns counts accepted sockets and wg retains the accept loop and its session
	// goroutines until listener cleanup has waited for their completion.
	mu       sync.Mutex     // command capture lock
	commands []string       // recorded protocol commands
	conns    atomic.Int64   // accepted socket count
	wg       sync.WaitGroup // fixture goroutine lifetime
}

// TestSMTPAcknowledgementCancellationBoundary runs DATA sessions with and without an acceptance reply.
// It verifies both sessions received message bytes, success after acknowledgement
// survives cancellation during QUIT, and the unacknowledged session returns an error.
func TestSMTPAcknowledgementCancellationBoundary(t *testing.T) {
	for _, ack := range []bool{true, false} {
		name := "unknown-without-ack"
		if ack {
			name = "cancel-after-ack"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			// The fixture listener owns the acknowledgement scenario's admission socket.
			// Close it on exit so a failed assertion cannot leave its accept endpoint available.
			defer func() { _ = listener.Close() }()
			address, ok := listener.Addr().(*net.TCPAddr)
			require.True(t, ok)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var messages atomic.Int64
			finished := make(chan struct{})
			// Run the controlled SMTP peer independently of Send so it can cancel ctx
			// exactly when QUIT arrives or withhold the DATA acknowledgement after receiving the message.
			go func() {
				defer close(finished)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				// The peer goroutine owns this accepted socket. Close it before completion
				// publication so the client receives EOF when the unacknowledged DATA branch returns.
				defer func() { _ = conn.Close() }()
				rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
				// Reduce fixture write/flush failures to an unavailable reply. Protocol
				// branches then return, letting deferred socket cleanup and completion publication end this controlled peer.
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
						// Receiving DATA is distinct from acknowledging acceptance. End this
						// peer without a success reply to expose the client's uncertain-delivery error path.
						if !ack {
							return
						}
						if !write("250 accepted") {
							return
						}
					// Cancel after the preceding successful DATA reply, then send an
					// unusual QUIT outcome to test the transport's already-selected success result.
					case command == "QUIT":
						cancel()
						_ = write("500 nonstandard quit")
						return
					}
				}
			}()
			err = New().Send(ctx, &SMTPConfig{Host: "127.0.0.1", Port: address.Port, Username: "fixture", Password: "fixture", From: "sender@example.com"}, "recipient@example.com", "fixture", "fixture")
			// Observe peer completion before inspecting receipt count and result. Its
			// close distinguishes server receipt from the separate acknowledgement that determines client success.
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

// TestSMTPConnectionImplicitTLS uses an implicit-TLS listener with transport TLS enabled.
// It verifies connection/authentication testing succeeds and the fixture records
// EHLO after the encrypted connection is established.
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

// TestSMTPConnectionStartTLSFallbackWhenTLSEnabled offers plaintext greeting plus STARTTLS with TLS mode enabled.
// It verifies readiness succeeds, STARTTLS is issued, and at least two connections
// were accepted for the implicit attempt and upgraded replacement.
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

// TestSMTPConnectionMandatoryStartTLSRefusesPlaintext offers a plaintext server without the STARTTLS extension.
// It verifies TLS-enabled readiness returns a STARTTLS error and the fixture
// records no AUTH command before that required-upgrade rejection.
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

// TestSMTPConnectionOpportunisticStartTLSWhenTLSDisabled offers STARTTLS while the configured TLS flag is false.
// It verifies readiness succeeds and the fixture receives the upgrade command,
// protecting the transport's advertised-extension path for optional TLS mode.
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

// TestSMTPConnectionPlainWhenNoStartTLS offers a loopback plaintext server with no upgrade extension.
// It verifies optional-TLS readiness succeeds and no STARTTLS command is recorded
// for that local fixture's advertised capability set.
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

// TestSendEmailWithConfigStartTLSFallback sends through a plaintext greeting server offering STARTTLS.
// It verifies the TLS-enabled send succeeds and the fixture records STARTTLS
// and DATA after the transport's compatibility connection path.
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

// TestSendEmailWithConfigImplicitTLS sends HTML through an implicit-TLS loopback fixture.
// It verifies the send returns success and the fixture records DATA for that
// configured encrypted connection, exercising message delivery after TLS setup.
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

// newSMTPTestCert creates a self-signed loopback certificate and trust pool for SMTP fixtures.
// It accepts both 127.0.0.1 and localhost identities and returns a certificate
// valid across a two-hour window around generation. Fixture failures fail the owning test.
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

// startFakeSMTPServer starts a fixture supporting requested implicit TLS and advertised upgrade choices.
// It installs the generated trust pool and restores the previous package pool
// through test cleanup. Returned port identifies the bound loopback listener.
//
// The listener loop and each accepted session register with one WaitGroup.
// Cleanup closes admission and waits for those goroutines; session deadlines bound
// held fixture I/O, while each session closes its socket on return. Because the
// trust-pool assignment is global, callers keep these fixture setups sequential.
func startFakeSMTPServer(t *testing.T, implicitTLS, advertiseStartTLS bool) (*fakeSMTPServer, int) {
	t.Helper()
	cert, pool := newSMTPTestCert(t)
	// TLS validation needs this fixture certificate. Publish its pool for the
	// sequential client test and restore the previous global value after the fixture is cleaned up.
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
	// Closing the listener ends the accept loop; waiting then retains the fixture
	// until every registered session has returned after its close or installed I/O deadline.
	t.Cleanup(func() {
		_ = srv.listener.Close()
		srv.wg.Wait()
	})

	// Register the accept loop before exposing the fixture. Each accepted socket
	// registers a child session so cleanup can wait for connection handling as well as admission.
	srv.wg.Go(func() {
		for {
			conn, err := srv.listener.Accept()
			if err != nil {
				return
			}
			srv.conns.Add(1)
			srv.wg.Go(func() {
				defer func() { _ = conn.Close() }()
				// A stalled fake session needs a bounded cleanup wait. Ignore deadline
				// installation errors here and let protocol I/O or listener/session cleanup expose fixture failure.
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				srv.serve(conn, srv.advertiseStartTLS)
			})
		}
	})

	port := testassert.MustType[*net.TCPAddr](listener.Addr()).Port
	return srv, port
}

// record records one protocol command under the fixture capture mutex for later assertions.
func (srv *fakeSMTPServer) record(cmd string) {
	// Accepted sessions record commands concurrently. Hold the capture lock
	// through appending so each command becomes available to the locked assertion reader as one complete entry.
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.commands = append(srv.commands, cmd)
}

// sawCommand reports whether a recorded command begins with the supplied uppercase prefix.
// It reads the capture list under its mutex and uppercases each recorded command;
// callers supply the prefix in the case used by protocol assertions.
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

// serve handles a new SMTP connection, optionally advertising and performing STARTTLS.
// Command recording precedes response handling. Successful upgrade hands the TLS
// socket to serveUpgraded without another greeting; read, write, and handshake failures
// end the fixture session for its owning goroutine to close.
//
// The fixture consumes DATA through the dot terminator before acknowledging it.
// AUTH and envelope commands receive deterministic success replies for transport
// assertions, while the selected upgrade advertisement decides which setup path
// can proceed. This fixture does not validate credentials or persist mail.
func (srv *fakeSMTPServer) serve(conn net.Conn, allowStartTLS bool) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	// Replies expose success as a boolean to the fixture loop. A socket write
	// or flush failure ends that protocol branch so a closed/canceled peer can release its session.
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
			// Switch this existing socket to TLS after the upgrade reply. The client
			// sends a new EHLO after handshake, so upgraded handling must omit the original SMTP greeting.
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
			// QUIT already ends this fixture session. Ignore final reply failure
			// and let the owning goroutine close the socket instead of waiting for another command.
			_ = writeLine("221 bye")
			return
		default:
			if !writeLine("250 ok") {
				return
			}
		}
	}
}

// serveUpgraded continues protocol handling after TLS without issuing a second greeting.
// net/smtp sends EHLO again after StartTLS succeeds, so the upgraded reader and
// writer pass directly to serveCommands on the already accepted socket.
func (srv *fakeSMTPServer) serveUpgraded(conn net.Conn) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	// The plaintext session already emitted its 220 greeting before STARTTLS.
	// Resume command handling so the client's post-upgrade EHLO receives extension replies instead of another initial greeting.
	srv.serveCommands(reader, writer)
}

// serveCommands responds to commands on the upgraded session and records each received command.
// It acknowledges fixture authentication, envelope commands, and completed DATA.
// Read/write failures end the session, and QUIT attempts its final reply before
// returning to the owning session goroutine for socket cleanup.
func (srv *fakeSMTPServer) serveCommands(reader *bufio.Reader, writer *bufio.Writer) {
	// Replies expose success as a boolean to the fixture loop. A socket write
	// or flush failure ends that protocol branch so a closed/canceled peer can release its session.
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
			// QUIT already ends this fixture session. Ignore final reply failure
			// and let the owning goroutine close the socket instead of waiting for another command.
			_ = writeLine("221 bye")
			return
		default:
			if !writeLine("250 ok") {
				return
			}
		}
	}
}

// smtpTestConfig returns fresh loopback SMTP settings with the requested port and TLS flag.
// The fixture uses fixed sender, account, and password strings for command-path
// assertions; callers can modify the returned configuration without changing another fixture value.
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

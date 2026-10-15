package provider

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
)

// tls_collector.go runs the TLS capture listener and records offered
// fingerprints with HTTP request metadata.
// The TLS collector reads an offered ClientHello before completing a server
// handshake, allowing an administrator to save the client's ordered fingerprint
// beside HTTP request metadata. Buffered peeking keeps that first TLS record
// available to the handshake, which negotiates HTTP/1.1 for the application
// request parser.
//
// A service mutex coordinates listener startup, stop, sessions, and status.
// Ordinary stop detaches the current server and clears sessions before network
// shutdown, while terminal shutdown seals later startup. Each connection
// completes its handshake once and retains captured lists for records that
// remain shared through session inspection. A generated short-lived authority
// can be returned to the administrator for controlled client trust; configured
// certificates instead use their existing trust arrangement.

// Collector defaults govern temporary session retention, capture record count,
// and the buffer needed to inspect a complete TLS record before handshake
// consumption.
const (
	tlsFingerprintCollectorDefaultTTL        = 30 * time.Minute
	tlsFingerprintCollectorDefaultMaxRecords = 20
	// TLS record payload length uses sixteen bits. Include its five-byte
	// header so Peek can retain the maximum first record for later handshake reads.
	tlsFingerprintCollectorClientHelloBufferSize = 64*1024 + 5
)

type (
	// TLSFingerprintCollectorStatus exposes the egress listener-status value
	// through this network adapter package.
	TLSFingerprintCollectorStatus = egress.TLSFingerprintCollectorStatus
	// TLSFingerprintCollectorSession exposes the egress temporary capture-session
	// value through this adapter package.
	TLSFingerprintCollectorSession = egress.TLSFingerprintCollectorSession
	// TLSFingerprintCaptureRecord exposes the egress captured handshake/request
	// value retained by session storage.
	TLSFingerprintCaptureRecord = egress.TLSFingerprintCaptureRecord
)

// CollectorOptions supplies listener identity, session retention, certificate
// inputs, and timing for collector construction.
//
// CertFile and KeyFile are configured together or both omitted for generated
// certificates. Host, Port, and PublicBaseURL determine listening and
// advertised addresses; session duration and record limits use defaults when
// nonpositive.
type CollectorOptions struct {
	Host                 string // configured listener bind host
	Port                 int    // configured listener TCP port
	PublicBaseURL        string // advertised client capture URL
	CertFile             string // optional server certificate path
	KeyFile              string // optional server private-key path
	SessionTTLSeconds    int    // seconds
	MaxRecordsPerSession int    // configured retained record cap
	// Now controls service start timestamps and capture session expiry, with
	// time.Now chosen when absent. Capture record IDs/times and generated
	// certificate lifetimes use direct wall-clock calls. The callback runs while
	// service or session locks are held and returns promptly without calling back
	// into methods that need those same locks.
	Now func() time.Time // session/start clock callback
	// Diagnostics runs synchronously from the Serve goroutine when serving fails.
	// Its callback returns before that goroutine can signal completion; synchronously
	// calling Stop or Shutdown on this service can therefore wait on that callback.
	Diagnostics egress.Diagnostics // listener failure diagnostic sink
}

// TLSFingerprintCollectorService owns the TLS collector listener and capture
// sessions.
type TLSFingerprintCollectorService struct {
	// mu coordinates closed, serveDone, server, listener, running, startedAt,
	// lastError, caPEM, cert, generatedCert, and sessions. Stop detaches current
	// listener/session state before network waiting, permitting an ordinary later
	// start to install a distinct lifetime.
	closed    bool          // terminal startup seal flag
	serveDone chan struct{} // current Serve completion signal
	// options is retained at construction and stays unchanged while service
	// helpers read it under mu. Diagnostics and Now callbacks remain supplied by
	// the application.
	options CollectorOptions // retained application collector options
	now     func() time.Time // session/start clock callback

	mu       sync.Mutex   // listener and session state lock
	server   *http.Server // current owned HTTP server
	listener net.Listener // current owned capture listener
	running  bool         // published listener availability flag
	// startedAt is retained by status snapshots while the listener is current.
	// Status callers treat that shared pointer as immutable even after a stop
	// resets service state.
	startedAt *time.Time // retained listener start timestamp
	lastError string     // latest retained start/serve error
	caPEM     string     // generated authority PEM certificate
	// cert is copied into the listener's certificate value before serving. That
	// listener retains the associated key and certificate material until its
	// connection lifetime completes.
	cert          tls.Certificate         // retained server certificate value
	generatedCert bool                    // generated certificate source flag
	sessions      *egress.CaptureSessions // current capture session registry
}

// tlsFingerprintCaptureContextKey distinguishes connection capture metadata in
// HTTP request contexts.
type tlsFingerprintCaptureContextKey struct{}

// tlsFingerprintCaptureContext pairs shared parsed hello lists with the
// connection's negotiated application protocol.
type tlsFingerprintCaptureContext struct {
	// clientHello refers to the connection's retained capture value after parsing.
	// Record construction shares its lists with Profile and RawTLSFingerprint, so
	// subsequent consumers leave them immutable.
	clientHello    *tlsfingerprint.CapturedClientHello // shared parsed offered ClientHello
	negotiatedALPN string                              // completed handshake protocol selection
}

// tlsFingerprintCaptureListener wraps accepted sockets for pre-handshake
// ClientHello capture and HTTPS request serving.
type tlsFingerprintCaptureListener struct {
	net.Listener // owned underlying TCP listener
	// cert is retained by every accepted capture connection. The service installs
	// a listener-specific value before serving and leaves its certificate material
	// stable for those connections.
	cert *tls.Certificate // retained immutable listener certificate
}

// tlsFingerprintCaptureConn buffers offered TLS bytes and performs one captured
// server handshake before application I/O.
//
// handshakeOnce coordinates Read and Write through capture and server
// handshake. handshakeErr retains failure, while tlsConn and negotiatedALPN
// are assigned after success; captured can already exist when the handshake
// later fails.
type tlsFingerprintCaptureConn struct {
	net.Conn // owned underlying accepted socket
	// cert remains shared with the listener for this connection's handshake. Its
	// key and certificate material stays stable while accepted connections are
	// using it.
	cert *tls.Certificate // retained immutable listener certificate
	// reader peeks the first TLS record without consuming it. readerConn then
	// feeds those buffered bytes to tls.Server so capture and handshake inspect
	// the same offered data.
	reader         *bufio.Reader                       // buffered first-record replay reader
	captured       *tlsfingerprint.CapturedClientHello // retained parsed offered ClientHello
	negotiatedALPN string                              // successful handshake protocol selection
	handshakeOnce  sync.Once                           // one-time handshake execution guard
	handshakeErr   error                               // retained first handshake failure
	tlsConn        *tls.Conn                           // successful server TLS connection
}

// readerConn replays peeked socket bytes to the TLS server through a retained
// buffered reader.
type readerConn struct {
	// Conn supplies socket methods while reader supplies Read. Both belong to the
	// same accepted connection so buffered capture bytes are consumed before later
	// socket bytes.
	net.Conn               // shared accepted connection methods
	reader   *bufio.Reader // retained buffered handshake reader
}

// NewTLSFingerprintCollectorService constructs an idle collector with retained
// options and an empty session registry. A nil Now selects time.Now for
// start/session timing; the application separately invokes Start and terminal
// Shutdown.
func NewTLSFingerprintCollectorService(options CollectorOptions) *TLSFingerprintCollectorService {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &TLSFingerprintCollectorService{options: options, now: now, sessions: egress.NewCaptureSessions(now)}
}

// collectorConfigLocked returns the collector options retained during
// construction. The caller holds mu when using this configuration alongside
// listener or session state, and the options remain unchanged for the service
// lifetime.
func (s *TLSFingerprintCollectorService) collectorConfigLocked() CollectorOptions { return s.options }

// Status returns effective collector settings and current listener state under
// the service lock. StartedAt remains a shared pointer and callers leave it
// immutable; status can show retained LastError even while the listener is
// stopped.
func (s *TLSFingerprintCollectorService) Status() TLSFingerprintCollectorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

// Start opens a dedicated capture listener or returns the current running
// status. Terminal shutdown rejects new startup, and certificate or listen
// failure is retained in LastError and returned. The supplied context does not
// own the serving lifetime.
//
// The service lock covers certificate setup, session replacement, listener
// registration, and Serve goroutine launch. Each Serve lifetime captures its
// own server and completion signal; an error from an older detached server
// cannot overwrite a replacement because identity is checked before updating
// state. The caller later uses Stop for restartable shutdown or Shutdown for
// terminal application shutdown.
func (s *TLSFingerprintCollectorService) Start(ctx context.Context) (TLSFingerprintCollectorStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.statusLocked(), errors.New("TLS fingerprint collector is shutting down")
	}
	if s.running {
		return s.statusLocked(), nil
	}

	cert, caPEM, generated, err := s.loadOrGenerateCertificateLocked()
	if err != nil {
		s.lastError = err.Error()
		return s.statusLocked(), err
	}

	listenAddr := s.listenAddressLocked()
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		s.lastError = err.Error()
		return s.statusLocked(), err
	}

	now := s.now()
	s.cert = cert
	s.caPEM = caPEM
	s.generatedCert = generated
	s.startedAt = &now
	s.lastError = ""
	s.sessions = egress.NewCaptureSessions(s.now)
	certificate := s.cert
	s.listener = newTLSFingerprintCaptureListener(ln, &certificate)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleCaptureRequest)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       60 * time.Second,
		// Store the connection wrapper before HTTP reads trigger its lazy
		// handshake. The handler later observes populated capture data through
		// that same wrapper instead of an early empty metadata copy.
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if captureConn, ok := conn.(*tlsFingerprintCaptureConn); ok {
				return context.WithValue(ctx, tlsFingerprintCaptureContextKey{}, captureConn)
			}
			return ctx
		},
		TLSConfig: &tls.Config{
			// The lazy connection handshake also advertises HTTP/1.1. Captured
			// offers may include h2, while application requests stay on the HTTP/1 parser.
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		},
	}
	s.running = true

	server := s.server
	listener := s.listener
	done := make(chan struct{})
	s.serveDone = done
	// Serve captures this server identity and completion channel before launch.
	// A detached older server can finish while a later listener lifetime is active.
	go func() {
		defer close(done)
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.options.Diagnostics.Log("service.tls_fp_collector", "[TLSFPCollector] serve failed: %v", serveErr)
			s.mu.Lock()
			if s.server != server {
				s.mu.Unlock()
				return
			}
			s.running = false
			s.lastError = serveErr.Error()
			s.server = nil
			s.listener = nil
			s.mu.Unlock()
		}
	}()
	return s.statusLocked(), nil
}

// Stop detaches listener status and replaces sessions before shutting down the
// captured server. It permits a later ordinary Start. Provide a nonnil ctx when
// a current server needs shutdown. Shutdown receives a five-second child budget
// of ctx; failure forces Close and returns that shutdown error.
//
// Network waiting occurs after releasing the service lock, so another start can
// publish a distinct server while this old lifetime drains. Successful graceful
// shutdown then waits for the captured Serve completion signal, bounded by the
// original ctx. The forced-close path returns without joining Serve, including
// when Close itself fails. With no current server it returns immediately, and
// concurrent stops can observe that detached state before the first network wait
// finishes.
func (s *TLSFingerprintCollectorService) Stop(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	done := s.serveDone
	s.serveDone = nil
	s.running = false
	s.server = nil
	s.listener = nil
	s.startedAt = nil
	s.caPEM = ""
	s.cert = tls.Certificate{}
	s.generatedCert = false
	s.sessions = egress.NewCaptureSessions(s.now)
	// Detach visible state before network waiting so shutdown does not hold
	// mu across active handlers that append captures through the same service lock.
	s.mu.Unlock()

	if server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// A timed-out graceful shutdown can leave active connections. Force their
	// closure and report the original shutdown error rather than return success.
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}

	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Shutdown seals later listener startup and invokes Stop with the supplied
// context. Repeated shutdown stays terminal; Stop determines whether a current
// server needs graceful or forced network closure and how waiting is bounded.
func (s *TLSFingerprintCollectorService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.Stop(ctx)
}

// CreateSession allocates a temporary capture session when a listener is
// running. It supplies effective advertised URL, certificate authority, and
// duration under the service lock; a stopped collector returns an error.
func (s *TLSFingerprintCollectorService) CreateSession() (*TLSFingerprintCollectorSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return nil, errors.New("TLS fingerprint collector is not running")
	}
	return s.sessions.Create(s.publicBaseURLLocked(), s.caPEM, s.sessionTTLLocked())
}

// ListCaptures returns a copied pointer slice for a live token while holding
// the service/session locks. Records and nested lists remain shared, so callers
// treat returned captures as immutable; expired or missing sessions return an
// error.
func (s *TLSFingerprintCollectorService) ListCaptures(token string) ([]*TLSFingerprintCaptureRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions.List(token)
}

// DeleteSession removes one token's captures from the current session registry
// under the service lock. Missing tokens do nothing, and record pointers
// already held by callers retain their objects.
func (s *TLSFingerprintCollectorService) DeleteSession(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions.Delete(token)
}

// handleCaptureRequest authenticates a capture token and requires parsed
// handshake metadata before building and retaining a record. Missing tokens
// return unauthorized, unavailable connection capture returns bad request, and
// session lookup failure also returns unauthorized.
//
// The record combines connection fingerprint lists with selected request
// metadata, then appendCapture uses the current session registry and record
// cap. It captures HTTP request identity after the shared handshake succeeded;
// duplicate requests can therefore create records for the same connection
// fingerprint. A concurrent stop can replace sessions before append, causing
// that token lookup to fail.
func (s *TLSFingerprintCollectorService) handleCaptureRequest(w http.ResponseWriter, r *http.Request) {
	token := captureTokenFromRequest(r)
	if token == "" {
		writeCollectorJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing token"})
		return
	}
	captureConn, _ := r.Context().Value(tlsFingerprintCaptureContextKey{}).(*tlsFingerprintCaptureConn)
	if captureConn == nil {
		writeCollectorJSON(w, http.StatusBadRequest, map[string]any{"error": "missing tls connection"})
		return
	}
	captureCtx := captureConn.captureContext()
	if captureCtx == nil || captureCtx.clientHello == nil {
		writeCollectorJSON(w, http.StatusBadRequest, map[string]any{"error": "missing tls fingerprint"})
		return
	}

	record := s.buildCaptureRecord(r, captureCtx)
	if err := s.appendCapture(token, record); err != nil {
		writeCollectorJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	writeCollectorJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"captured_id": record.ID,
		"message":     "TLS fingerprint captured",
	})
}

// appendCapture appends the supplied shared record through the current session
// registry with the effective cap. The service lock prevents concurrent
// registry replacement during insertion, while record ownership remains with
// immutable capture consumers.
func (s *TLSFingerprintCollectorService) appendCapture(token string, record *TLSFingerprintCaptureRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions.Append(token, record, s.maxRecordsLocked())
}

// buildCaptureRecord creates an inspection record using direct wall-clock time
// and the successful connection's captured hello. Profile and RawTLSFingerprint
// share the parsed lists, while header summaries are separately allocated maps.
//
// The helper records the request path, method, client hint, negotiated ALPN,
// JA3, and a formatted profile preview. IDs use the nanosecond timestamp text
// rather than a random identifier, and the configured session clock does not
// determine these times. Consumers leave the retained record and captured lists
// immutable after session insertion.
func (s *TLSFingerprintCollectorService) buildCaptureRecord(r *http.Request, captureCtx *tlsFingerprintCaptureContext) *TLSFingerprintCaptureRecord {
	now := time.Now()
	ch := captureCtx.clientHello
	clientKind := detectTLSFingerprintClientKind(r)
	profile := &egress.TLSFingerprintProfile{
		Name:                defaultTLSFingerprintProfileName(clientKind, now),
		Description:         tlsFingerprintCollectorStringPtr("由内置 TLS 指纹收集器采集"),
		EnableGREASE:        ch.EnableGREASE,
		CipherSuites:        ch.CipherSuites,
		Curves:              ch.Curves,
		PointFormats:        ch.PointFormats,
		SignatureAlgorithms: ch.SignatureAlgorithms,
		ALPNProtocols:       ch.ALPNProtocols,
		SupportedVersions:   ch.SupportedVersions,
		KeyShareGroups:      ch.KeyShareGroups,
		PSKModes:            ch.PSKModes,
		Extensions:          ch.Extensions,
	}
	record := &TLSFingerprintCaptureRecord{
		ID:                fmt.Sprintf("%d", now.UnixNano()),
		CapturedAt:        now,
		ClientKind:        clientKind,
		RequestPath:       r.URL.Path,
		Method:            r.Method,
		UserAgent:         r.UserAgent(),
		JA3Raw:            ch.JA3Raw,
		JA3Hash:           ch.JA3Hash,
		NegotiatedALPN:    captureCtx.negotiatedALPN,
		HTTPProto:         r.Proto,
		Profile:           profile,
		YAML:              tlsFingerprintProfileToYAML(profile),
		HeadersSummary:    summarizeTLSFingerprintHeaders(r.Header),
		StainlessSummary:  summarizeStainlessHeaders(r.Header),
		RawTLSFingerprint: (*egress.CapturedClientHello)(ch),
	}
	return record
}

// statusLocked combines effective configuration with the listener state for
// admin responses. The caller holds mu; StartedAt retains the service pointer,
// while other returned fields copy their current scalar values.
func (s *TLSFingerprintCollectorService) statusLocked() TLSFingerprintCollectorStatus {
	return TLSFingerprintCollectorStatus{
		Running:              s.running,
		ListenAddress:        s.listenAddressLocked(),
		PublicBaseURL:        s.publicBaseURLLocked(),
		UsingGeneratedCert:   s.generatedCert,
		CAPEM:                s.caPEM,
		SessionTTLSeconds:    int(s.sessionTTLLocked().Seconds()),
		MaxRecordsPerSession: s.maxRecordsLocked(),
		StartedAt:            s.startedAt,
		LastError:            s.lastError,
	}
}

// loadOrGenerateCertificateLocked loads the configured certificate/key pair or
// creates a temporary authority and server certificate. The caller holds mu;
// one configured path without its partner fails, and externally loaded
// certificates return no generated CA PEM.
func (s *TLSFingerprintCollectorService) loadOrGenerateCertificateLocked() (tls.Certificate, string, bool, error) {
	collectorCfg := s.collectorConfigLocked()
	certFile := strings.TrimSpace(collectorCfg.CertFile)
	keyFile := strings.TrimSpace(collectorCfg.KeyFile)
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return tls.Certificate{}, "", false, errors.New("cert_file and key_file must be configured together")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return tls.Certificate{}, "", false, err
		}
		return cert, "", false, nil
	}
	cert, caPEM, err := generateTLSFingerprintCollectorCertificate(s.publicHostLocked())
	return cert, caPEM, true, err
}

// listenAddressLocked formats the configured bind address while the caller
// holds mu. Blank Host becomes 0.0.0.0 and a nonpositive Port becomes 8443 for
// the dedicated capture listener.
func (s *TLSFingerprintCollectorService) listenAddressLocked() string {
	collectorCfg := s.collectorConfigLocked()
	host := strings.TrimSpace(collectorCfg.Host)
	if host == "" {
		host = "0.0.0.0"
	}
	port := collectorCfg.Port
	if port <= 0 {
		port = 8443
	}
	return net.JoinHostPort(host, strconvItoa(port))
}

// publicBaseURLLocked returns a trimmed configured URL without trailing
// slashes, or formats HTTPS from the selected public host and effective port.
// The caller holds mu, and nonpositive ports select 8443.
func (s *TLSFingerprintCollectorService) publicBaseURLLocked() string {
	collectorCfg := s.collectorConfigLocked()
	if raw := strings.TrimRight(strings.TrimSpace(collectorCfg.PublicBaseURL), "/"); raw != "" {
		return raw
	}
	host := s.publicHostLocked()
	port := collectorCfg.Port
	if port <= 0 {
		port = 8443
	}
	return fmt.Sprintf("https://%s", net.JoinHostPort(host, strconvItoa(port)))
}

// publicHostLocked chooses the configured advertised URL hostname before the
// bind host. Blank or wildcard bind hosts become localhost for client-facing
// capture addresses; the caller holds mu during configuration access.
func (s *TLSFingerprintCollectorService) publicHostLocked() string {
	collectorCfg := s.collectorConfigLocked()
	if raw := strings.TrimSpace(collectorCfg.PublicBaseURL); raw != "" {
		if host := hostFromURL(raw); host != "" {
			return host
		}
	}
	host := strings.TrimSpace(collectorCfg.Host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "localhost"
	}
	return host
}

// sessionTTLLocked returns the configured session seconds as a duration or the
// thirty-minute default when nonpositive. The caller holds mu while deriving
// this effective setting for session creation or status.
func (s *TLSFingerprintCollectorService) sessionTTLLocked() time.Duration {
	seconds := s.collectorConfigLocked().SessionTTLSeconds
	if seconds <= 0 {
		return tlsFingerprintCollectorDefaultTTL
	}
	return time.Duration(seconds) * time.Second
}

// maxRecordsLocked returns the configured positive session record cap or the
// twenty-record default. The caller holds mu when deriving this limit for
// append operations and effective administrator status.
func (s *TLSFingerprintCollectorService) maxRecordsLocked() int {
	maxRecords := s.collectorConfigLocked().MaxRecordsPerSession
	if maxRecords <= 0 {
		return tlsFingerprintCollectorDefaultMaxRecords
	}
	return maxRecords
}

// newTLSFingerprintCaptureListener wraps the supplied listener with a retained
// certificate pointer. Accepted connections borrow that stable certificate
// material, while server shutdown owns closure of the underlying listener.
func newTLSFingerprintCaptureListener(inner net.Listener, cert *tls.Certificate) net.Listener {
	return &tlsFingerprintCaptureListener{Listener: inner, cert: cert}
}

// Accept wraps one accepted TCP socket for lazy capture and server handshake.
// Underlying accept errors return directly; the returned connection retains the
// listener certificate until its later I/O and closure.
func (l *tlsFingerprintCaptureListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newTLSFingerprintCaptureConn(conn, l.cert), nil
}

// newTLSFingerprintCaptureConn retains an accepted socket and certificate with
// a buffer sized for the first complete TLS record. The constructor leaves
// handshake work until Read or Write and the consumer owns closing the
// connection.
func newTLSFingerprintCaptureConn(conn net.Conn, cert *tls.Certificate) *tlsFingerprintCaptureConn {
	return &tlsFingerprintCaptureConn{
		Conn:   conn,
		cert:   cert,
		reader: bufio.NewReaderSize(conn, tlsFingerprintCollectorClientHelloBufferSize),
	}
}

// Read completes the retained capture handshake before reading application
// bytes through TLS. The first handshake failure is returned to later calls,
// and the consumer closes the connection after I/O completes.
func (c *tlsFingerprintCaptureConn) Read(p []byte) (int, error) {
	if err := c.ensureHandshake(); err != nil {
		return 0, err
	}
	return c.tlsConn.Read(p)
}

// Write completes the retained capture handshake before sending application
// bytes through TLS. The first handshake failure is returned to later calls,
// and the consumer closes the connection after I/O completes.
func (c *tlsFingerprintCaptureConn) Write(p []byte) (int, error) {
	if err := c.ensureHandshake(); err != nil {
		return 0, err
	}
	return c.tlsConn.Write(p)
}

// Close closes the successful TLS connection when present, otherwise the
// underlying accepted socket. Its consumer owns connection cleanup, and this
// method does not coordinate closure with an in-progress handshake assignment.
func (c *tlsFingerprintCaptureConn) Close() error {
	if c.tlsConn != nil {
		return c.tlsConn.Close()
	}
	return c.Conn.Close()
}

// ensureHandshake captures offered bytes and performs the server TLS handshake
// once for subsequent Read/Write calls. Failed capture or handshake retains the
// error; success installs tlsConn and its negotiated ALPN.
//
// Peeking leaves bytes in reader so tls.Server receives the same first record
// through readerConn. The server uses the retained certificate, requires TLS
// 1.2, and advertises HTTP/1.1 to keep application parsing on that protocol.
// handshakeOnce coordinates competing reads and writes, while connection
// closing and any deadlines remain with the accepted socket's owner.
func (c *tlsFingerprintCaptureConn) ensureHandshake() error {
	c.handshakeOnce.Do(func() {
		if err := c.captureClientHello(); err != nil {
			c.handshakeErr = err
			return
		}
		// Feed peeked bytes back through readerConn before later socket bytes.
		// This keeps the captured hello and the server handshake on one offered record.
		tlsConn := tls.Server(&readerConn{Conn: c.Conn, reader: c.reader}, &tls.Config{
			Certificates: []tls.Certificate{*c.cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"http/1.1"},
		})
		if err := tlsConn.Handshake(); err != nil {
			c.handshakeErr = err
			return
		}
		c.tlsConn = tlsConn
		c.negotiatedALPN = tlsConn.ConnectionState().NegotiatedProtocol
	})
	return c.handshakeErr
}

// captureClientHello peeks and parses one complete first TLS record while
// retaining its bytes for the later server handshake. Header, read, and parser
// failures return directly. Multi-record ClientHello reassembly is outside this
// capture path.
//
// The first five bytes supply the record length, then a second Peek retains that
// entire record in reader. Parsing populates captured without advancing the
// reader, so readerConn subsequently gives tls.Server the same offered bytes
// before later socket data. Callers configure socket deadlines and close the
// connection when capture or handshake fails.
func (c *tlsFingerprintCaptureConn) captureClientHello() error {
	header, err := c.reader.Peek(5)
	if err != nil {
		return err
	}
	recordLen := int(header[3])<<8 | int(header[4])
	if recordLen <= 0 {
		return errors.New("invalid TLS record length")
	}
	// Leave the first record buffered after parsing so the TLS server can
	// consume those same bytes when the lazy handshake starts.
	record, err := c.reader.Peek(5 + recordLen)
	if err != nil {
		return err
	}
	captured, err := tlsfingerprint.ParseCapturedClientHello(record)
	if err != nil {
		return err
	}
	c.captured = captured
	return nil
}

// captureContext returns a new wrapper for the retained parsed hello and
// negotiated ALPN. An absent parsed hello returns nil. Callers inspect this data
// after handshake completion; this helper supplies no synchronization against
// concurrent handshake assignments, and the captured lists remain shared and
// immutable.
func (c *tlsFingerprintCaptureConn) captureContext() *tlsFingerprintCaptureContext {
	if c.captured == nil {
		return nil
	}
	return &tlsFingerprintCaptureContext{
		clientHello:    c.captured,
		negotiatedALPN: c.negotiatedALPN,
	}
}

// Read consumes the retained buffered reader before later underlying socket
// bytes.
func (c *readerConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

// writeCollectorJSON sets JSON content type, writes status, and encodes payload
// to the response. Encoding failure is discarded after status publication, so
// callers cannot use this helper to recover a partially written response.
func writeCollectorJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Status is already written before encoding can fail. Discard that error
	// here because a replacement status would not recover the published response.
	_ = json.NewEncoder(w).Encode(payload)
}

// generateTLSFingerprintCollectorCertificate creates a P-256 temporary
// authority and server certificate for host plus localhost. Both lifetimes
// extend twenty-four hours from direct wall-clock time and start one minute
// earlier for tolerance. The returned PEM lets an administrator trust this
// generated authority.
//
// The authority signs the server certificate, while the returned
// tls.Certificate contains its server key and leaf material. Host names or
// literal addresses become SAN entries through addCertificateHost. Values are
// created in memory for this listener setup; persistence, trust installation,
// and later resource lifetimes remain with the caller.
func generateTLSFingerprintCollectorCertificate(host string) (tls.Certificate, string, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	// Backdate both certificate lifetimes by one minute for capture clients
	// with nearby clocks, while the generated authority expires after one day.
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "TokenRouter TLS Fingerprint Collector CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano() + 1),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	addCertificateHost(serverTemplate, host)
	addCertificateHost(serverTemplate, "localhost")
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	return cert, caPEM, nil
}

// addCertificateHost appends a trimmed SAN identity to the supplied
// certificate. Empty text does nothing, literal IPs populate IPAddresses, and
// successfully split host/port text is reduced to its host before DNS-name
// handling.
func addCertificateHost(cert *x509.Certificate, host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	if ip := net.ParseIP(host); ip != nil {
		cert.IPAddresses = append(cert.IPAddresses, ip)
		return
	}
	if strings.Contains(host, ":") {
		if splitHost, _, err := net.SplitHostPort(host); err == nil {
			addCertificateHost(cert, splitHost)
			return
		}
	}
	cert.DNSNames = append(cert.DNSNames, host)
}

// detectTLSFingerprintClientKind classifies request User-Agent and path hints
// into Claude Code, Codex, or unknown. Claude/messages hints take precedence
// when both families match; these labels describe heuristic capture grouping
// rather than authenticated client identity.
func detectTLSFingerprintClientKind(r *http.Request) string {
	ua := strings.ToLower(r.UserAgent())
	path := strings.ToLower(r.URL.Path)
	switch {
	case strings.Contains(ua, "claude") || strings.Contains(path, "messages"):
		return "claude_code"
	case strings.Contains(ua, "codex") || strings.Contains(path, "codex") || strings.Contains(path, "responses"):
		return "codex"
	default:
		return "unknown"
	}
}

// captureTokenFromRequest checks query and dedicated-header tokens before the
// capture path, then Authorization and API-key headers. Nil requests return
// empty; other requests need an initialized URL. A recognized capture path
// returns its trimmed second component immediately, including an empty
// component, so that path can stop selection before authentication-header
// fallback.
//
// Clients can append protocol endpoint suffixes after the path token.
// Authorization accepts a two-word Bearer value or one raw token through its
// helper. This precedence selects a token for the current capture registry;
// selected authentication text is excluded from the named header summaries.
func captureTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token
	}
	if token := strings.TrimSpace(r.Header.Get("X-TLS-Fingerprint-Token")); token != "" {
		return token
	}
	// A client can append /v1/messages or /v1/responses to its capture base.
	// The second path component supplies the token before that endpoint suffix; an
	// empty component also returns here and prevents later authorization fallback.
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "capture" {
		return strings.TrimSpace(parts[1])
	}
	// Controlled clients can send a capture token through existing auth
	// configuration. Consult Authorization after explicit URL/header/path token
	// choices.
	if token := bearerTokenFromAuthorization(r.Header.Get("Authorization")); token != "" {
		return token
	}
	if token := strings.TrimSpace(r.Header.Get("X-Api-Key")); token != "" {
		return token
	}
	return ""
}

// bearerTokenFromAuthorization extracts a case-insensitive two-word Bearer
// token or a single raw token. Whitespace-only, extra-word, and other two-word
// schemes return empty, allowing capture token selection to continue with the
// API-key header.
func bearerTokenFromAuthorization(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return strings.TrimSpace(fields[1])
	}
	if len(fields) == 1 {
		return strings.TrimSpace(fields[0])
	}
	return ""
}

// defaultTLSFingerprintProfileName formats a captured profile name from the
// client hint and supplied timestamp. Claude Code and Codex use their display
// labels; other hints use Captured TLS with the same local timestamp format.
func defaultTLSFingerprintProfileName(clientKind string, ts time.Time) string {
	switch clientKind {
	case "claude_code":
		return "Claude Code " + ts.Format("2006-01-02 15:04:05")
	case "codex":
		return "Codex CLI " + ts.Format("2006-01-02 15:04:05")
	default:
		return "Captured TLS " + ts.Format("2006-01-02 15:04:05")
	}
}

// tlsFingerprintProfileToYAML formats editable profile values into a
// captured_profile text block. Nil input returns empty; helpers quote strings
// and format ordered arrays, but their limited quoting is not a general YAML
// serializer for arbitrary string content.
func tlsFingerprintProfileToYAML(profile *egress.TLSFingerprintProfile) string {
	if profile == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprint(&b, "captured_profile:\n")
	writeYAMLString(&b, "name", profile.Name)
	if profile.Description != nil {
		writeYAMLString(&b, "description", *profile.Description)
	}
	fmt.Fprintf(&b, "  enable_grease: %t\n", profile.EnableGREASE)
	writeYAMLNumberArray(&b, "cipher_suites", profile.CipherSuites)
	writeYAMLNumberArray(&b, "curves", profile.Curves)
	writeYAMLNumberArray(&b, "point_formats", profile.PointFormats)
	writeYAMLNumberArray(&b, "signature_algorithms", profile.SignatureAlgorithms)
	writeYAMLStringArray(&b, "alpn_protocols", profile.ALPNProtocols)
	writeYAMLNumberArray(&b, "supported_versions", profile.SupportedVersions)
	writeYAMLNumberArray(&b, "key_share_groups", profile.KeyShareGroups)
	writeYAMLNumberArray(&b, "psk_modes", profile.PSKModes)
	writeYAMLNumberArray(&b, "extensions", profile.Extensions)
	return b.String()
}

// writeYAMLString appends one quoted field with embedded quote characters
// escaped. Other characters, including backslashes and newlines, retain their
// input spelling, so callers restrict content when they require a valid YAML
// scalar.
func writeYAMLString(b *strings.Builder, key, value string) {
	escaped := strings.ReplaceAll(value, `"`, `\"`)
	fmt.Fprintf(b, "  %s: \"%s\"\n", key, escaped)
}

// writeYAMLNumberArray appends an ordered decimal array for one fingerprint
// field.
func writeYAMLNumberArray[T ~uint16](b *strings.Builder, key string, values []T) {
	fmt.Fprintf(b, "  %s: [", key)
	for i, value := range values {
		if i > 0 {
			fmt.Fprint(b, ", ")
		}
		fmt.Fprintf(b, "%d", value)
	}
	fmt.Fprint(b, "]\n")
}

// writeYAMLStringArray appends ordered quoted strings with embedded quotes
// escaped. Other string characters retain input spelling, so callers restrict
// values when they require general YAML escaping for backslashes or newlines.
func writeYAMLStringArray(b *strings.Builder, key string, values []string) {
	fmt.Fprintf(b, "  %s: [", key)
	for i, value := range values {
		if i > 0 {
			fmt.Fprint(b, ", ")
		}
		escaped := strings.ReplaceAll(value, `"`, `\"`)
		fmt.Fprintf(b, "\"%s\"", escaped)
	}
	fmt.Fprint(b, "]\n")
}

// summarizeTLSFingerprintHeaders copies selected request identity and SDK
// metadata into a new map. It uses Header.Get for first values and omits blank
// selections; Authorization and Cookie are outside this named summary list.
func summarizeTLSFingerprintHeaders(headers http.Header) map[string]string {
	allowed := []string{
		"user-agent",
		"x-stainless-os",
		"x-stainless-arch",
		"x-stainless-runtime",
		"x-stainless-runtime-version",
		"x-stainless-lang",
		"x-stainless-package-version",
		"anthropic-version",
		"openai-organization",
	}
	out := make(map[string]string)
	for _, key := range allowed {
		if value := headers.Get(key); value != "" {
			out[key] = value
		}
	}
	return out
}

// summarizeStainlessHeaders returns a new map of first values for x-stainless-
// names with nonempty value slices. Names are lowercased, empty first values
// remain present, and case-variant collisions follow map iteration order.
func summarizeStainlessHeaders(headers http.Header) map[string]string {
	out := make(map[string]string)
	for key, values := range headers {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "x-stainless-") || len(values) == 0 {
			continue
		}
		out[lower] = values[0]
	}
	return out
}

// hostFromURL extracts an advertised hostname, assuming HTTPS when input has no
// scheme separator. Blank or invalid request URLs return empty; a missing
// Hostname falls back to the parsed Host for the caller's public-address
// selection.
func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil || req.URL == nil {
		return ""
	}
	host := req.URL.Hostname()
	if host == "" {
		return req.URL.Host
	}
	return host
}

// tlsFingerprintCollectorStringPtr allocates one editable string pointer for
// captured profile description text.
func tlsFingerprintCollectorStringPtr(v string) *string {
	return &v
}

// strconvItoa formats a listener port as a decimal string for address
// construction.
func strconvItoa(v int) string {
	return fmt.Sprintf("%d", v)
}

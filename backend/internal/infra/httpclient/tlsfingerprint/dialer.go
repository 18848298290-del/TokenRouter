package tlsfingerprint

import (
	"bufio"
	"context"
	stdtls "crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls" //nolint:revive // The package is named tls; the alias distinguishes it from crypto/tls.
	"golang.org/x/net/proxy"
)

// dialer.go establishes direct and proxied TLS connections using configured
// ClientHello profiles.
// TLS fingerprint dialing creates an ordered ClientHello independently of
// net/http's ordinary TLS connector. Direct and SOCKS paths obtain a network
// connection before the target uTLS handshake; HTTP proxies establish CONNECT
// first, and HTTPS proxies add a standard verified TLS connection around that
// CONNECT exchange. Default network dialing and the target handshake have
// separate ten-second budgets; a supplied direct dialer owns its dial budget.
//
// Profiles select raw ordered IDs and protocol lists, with empty lists
// choosing built-in defaults. Unknown extension IDs carry empty payloads,
// while ECH uses uTLS's generated GREASE payload rather than an empty
// extension. Dialers retain profile and proxy pointers; selected spec slices
// can remain shared, including SupportedVersions whose GREASE entries uTLS
// rewrites during ApplyPreset. CONNECT I/O has no installed deadline, so its
// waiting is distinct from bounded dialing and target handshaking.

const (
	// Fingerprint connectors bypass ordinary transport TLS dialing, so these
	// phase limits apply to their network setup and separate target handshake.
	defaultTLSFingerprintDialTimeout      = 10 * time.Second
	defaultTLSFingerprintDialKeepAlive    = 30 * time.Second
	defaultTLSFingerprintHandshakeTimeout = 10 * time.Second
)

var (
	// defaultCipherSuites supplies ordered TLS 1.3 and legacy cipher IDs when a
	// profile omits its cipher list. Their order contributes to the ClientHello
	// fingerprint compared by the optional network fixtures.
	defaultCipherSuites = []uint16{
		// TLS 1.3 cipher suites
		0x1301, // TLS_AES_128_GCM_SHA256
		0x1302, // TLS_AES_256_GCM_SHA384
		0x1303, // TLS_CHACHA20_POLY1305_SHA256

		// ECDHE + AES-GCM
		0xc02b, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
		0xc02f, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
		0xc02c, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
		0xc030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384

		// ECDHE + ChaCha20-Poly1305
		0xcca9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
		0xcca8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256

		// ECDHE + AES-CBC-SHA (legacy fallback)
		0xc009, // TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA
		0xc013, // TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA
		0xc00a, // TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA
		0xc014, // TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA

		// RSA + AES-GCM (non-PFS)
		0x009c, // TLS_RSA_WITH_AES_128_GCM_SHA256
		0x009d, // TLS_RSA_WITH_AES_256_GCM_SHA384

		// RSA + AES-CBC-SHA (non-PFS, legacy)
		0x002f, // TLS_RSA_WITH_AES_128_CBC_SHA
		0x0035, // TLS_RSA_WITH_AES_256_CBC_SHA
	}

	// defaultCurves advertises X25519, P-256, and P-384 in that order when a
	// profile leaves its supported-group list empty.
	defaultCurves = []utls.CurveID{
		utls.X25519,    // 0x001d
		utls.CurveP256, // 0x0017 (secp256r1)
		utls.CurveP384, // 0x0018 (secp384r1)
	}

	// defaultPointFormats selects the uncompressed point-format byte when a
	// profile omits the format list consumed by the corresponding TLS extension.
	defaultPointFormats = []uint16{
		0, // uncompressed
	}

	// defaultSignatureAlgorithms supplies the ordered signature-scheme IDs used
	// by signature extensions when the profile leaves its algorithm list empty.
	defaultSignatureAlgorithms = []utls.SignatureScheme{
		0x0403, // ecdsa_secp256r1_sha256
		0x0804, // rsa_pss_rsae_sha256
		0x0401, // rsa_pkcs1_sha256
		0x0503, // ecdsa_secp384r1_sha384
		0x0805, // rsa_pss_rsae_sha384
		0x0501, // rsa_pkcs1_sha384
		0x0806, // rsa_pss_rsae_sha512
		0x0601, // rsa_pkcs1_sha512
		0x0201, // rsa_pkcs1_sha1
	}

	// defaultExtensionOrder selects the built-in sequence when Profile.Extensions
	// is empty; a supplied order replaces it and controls GREASE positioning.
	defaultExtensionOrder = []uint16{
		0,     // server_name
		65037, // encrypted_client_hello
		23,    // extended_master_secret
		65281, // renegotiation_info
		10,    // supported_groups
		11,    // ec_point_formats
		35,    // session_ticket
		16,    // alpn
		5,     // status_request
		13,    // signature_algorithms
		18,    // signed_certificate_timestamp
		51,    // key_share
		45,    // psk_key_exchange_modes
		43,    // supported_versions
	}
)

// Profile contains TLS fingerprint configuration.
// All slice fields use built-in defaults when empty.
type Profile struct {
	Name string // cache identity profile name
	// CipherSuites, Curves, PointFormats, SignatureAlgorithms, ALPNProtocols,
	// SupportedVersions, KeyShareGroups, PSKModes, and Extensions use effective
	// defaults when empty. A nonempty Extensions list decides which resolved
	// values are advertised by including their extension IDs in that order.
	CipherSuites []uint16 // ordered cipher suite IDs
	Curves       []uint16 // ordered supported group IDs
	PointFormats []uint16 // ordered point format IDs
	// EnableGREASE adds GREASE bookends when Extensions selects the built-in
	// order. Explicit GREASE IDs in a custom order remain effective regardless
	// of this flag.
	EnableGREASE        bool     // add GREASE for default order
	SignatureAlgorithms []uint16 // ordered signature scheme IDs
	ALPNProtocols       []string // ordered application protocol names
	// SupportedVersions can remain shared with its uTLS extension. ApplyPreset
	// rewrites GREASE values in place, so callers copy this list or serialize
	// use when those values appear in a shared profile.
	SupportedVersions []uint16 // ordered TLS version IDs
	KeyShareGroups    []uint16 // ordered key share group IDs
	PSKModes          []uint16 // ordered PSK exchange mode IDs
	Extensions        []uint16 // ordered TLS extension type IDs
}

// Dialer creates target TLS connections with configured ClientHello fingerprints.
type Dialer struct {
	// profile is retained rather than copied. Keep its values stable during
	// dialing, and serialize or copy SupportedVersions storage when GREASE
	// permits uTLS mutation.
	profile *Profile // retained TLS profile pointer
	// baseDialer is selected at construction and opens the target connection. A
	// custom callback owns its network timeout and response to context
	// cancellation.
	baseDialer func(ctx context.Context, network, addr string) (net.Conn, error) // network connection setup callback
}

// HTTPProxyDialer creates TLS connections through HTTP/HTTPS proxies with custom fingerprints.
// It handles the CONNECT tunnel establishment before performing TLS handshake.
type HTTPProxyDialer struct {
	// profile is retained rather than copied. Keep its values stable during
	// dialing, and serialize or copy SupportedVersions storage when GREASE
	// permits uTLS mutation.
	profile *Profile // retained TLS profile pointer
	// proxyURL is retained as supplied for dialing and proxy credentials.
	// Callers keep its fields and userinfo stable for the dialer's lifetime.
	proxyURL *url.URL // retained proxy URL pointer
	// proxyTLSConfig is an optional fixture override cloned before proxy TLS.
	// Missing ServerName and MinVersion are filled; other supplied settings
	// remain effective.
	proxyTLSConfig *stdtls.Config // optional proxy TLS test override
}

// SOCKS5ProxyDialer creates TLS connections through SOCKS5 proxies with custom fingerprints.
// It uses golang.org/x/net/proxy to establish the SOCKS5 tunnel.
type SOCKS5ProxyDialer struct {
	// profile is retained rather than copied. Keep its values stable during
	// dialing, and serialize or copy SupportedVersions storage when GREASE
	// permits uTLS mutation.
	profile *Profile // retained TLS profile pointer
	// proxyURL is retained as supplied for dialing and proxy credentials.
	// Callers keep its fields and userinfo stable for the dialer's lifetime.
	proxyURL *url.URL // retained proxy URL pointer
}

// newTLSFingerprintNetworkDialer supplies ten-second connection setup and
// thirty-second TCP keepalive settings. It is used below direct and proxy
// connections where custom TLS dialing bypasses net/http's original network
// connector.
func newTLSFingerprintNetworkDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   defaultTLSFingerprintDialTimeout,
		KeepAlive: defaultTLSFingerprintDialKeepAlive,
	}
}

// NewDialer retains profile and creates a target-TLS dialer using baseDialer
// for network setup. Nil baseDialer selects the bounded direct connector; a
// supplied callback owns its network budgets and cancellation behavior, and
// callers retain stable profile data during use.
func NewDialer(profile *Profile, baseDialer func(ctx context.Context, network, addr string) (net.Conn, error)) *Dialer {
	if baseDialer == nil {
		baseDialer = newTLSFingerprintNetworkDialer().DialContext
	}
	return &Dialer{profile: profile, baseDialer: baseDialer}
}

// NewHTTPProxyDialer retains profile and proxyURL for CONNECT followed by
// target uTLS handshaking. Callers provide a proxy URL before dialing and
// keep referenced data stable; HTTPS proxy TLS configuration uses verified
// defaults unless its private test override is supplied.
func NewHTTPProxyDialer(profile *Profile, proxyURL *url.URL) *HTTPProxyDialer {
	return &HTTPProxyDialer{profile: profile, proxyURL: proxyURL}
}

// NewSOCKS5ProxyDialer retains profile and proxyURL for SOCKS tunneling
// before target uTLS handshaking. Callers provide a non-nil proxy URL and
// keep referenced data stable while the returned dialer is in use.
func NewSOCKS5ProxyDialer(profile *Profile, proxyURL *url.URL) *SOCKS5ProxyDialer {
	return &SOCKS5ProxyDialer{profile: profile, proxyURL: proxyURL}
}

// DialTLSContext opens the supplied target through the retained SOCKS proxy
// and returns a fingerprinted TLS connection. Pass a non-nil context and
// target host:port; the forward connector bounds proxy TCP setup, and an
// absent proxy port selects 1080.
//
// The context-aware SOCKS interface is preferred; its non-context
// compatibility branch cannot forward cancellation. Target handshaking owns
// closing the tunnel on preset or handshake failure, while a successful
// caller closes the returned uTLS connection.
func (d *SOCKS5ProxyDialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	slog.Debug("tls_fingerprint_socks5_connecting", "proxy", d.proxyURL.Host, "target", addr)

	// Step 1: Create SOCKS5 dialer
	var auth *proxy.Auth
	if d.proxyURL.User != nil {
		username := d.proxyURL.User.Username()
		password, _ := d.proxyURL.User.Password()
		auth = &proxy.Auth{
			User:     username,
			Password: password,
		}
	}

	// Determine proxy address
	proxyAddr := d.proxyURL.Host
	if d.proxyURL.Port() == "" {
		proxyAddr = net.JoinHostPort(d.proxyURL.Hostname(), "1080") // Default SOCKS5 port
	}

	socksDialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, newTLSFingerprintNetworkDialer())
	if err != nil {
		slog.Debug("tls_fingerprint_socks5_dialer_failed", "error", err)
		return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
	}

	// Prefer the context-aware SOCKS tunnel so the caller's budget reaches its
	// negotiation; the compatibility Dial branch has no context argument.
	slog.Debug("tls_fingerprint_socks5_establishing_tunnel", "target", addr)
	var conn net.Conn
	if contextDialer, ok := socksDialer.(proxy.ContextDialer); ok {
		conn, err = contextDialer.DialContext(ctx, network, addr)
	} else {
		conn, err = socksDialer.Dial(network, addr)
	}
	if err != nil {
		slog.Debug("tls_fingerprint_socks5_connect_failed", "error", err)
		return nil, fmt.Errorf("SOCKS5 connect: %w", err)
	}
	slog.Debug("tls_fingerprint_socks5_tunnel_established")

	// Step 3: Perform TLS handshake on the tunnel with utls fingerprint
	return performTLSHandshake(ctx, conn, d.profile, addr)
}

// DialTLSContext establishes proxy TCP, optional verified HTTPS-proxy TLS,
// CONNECT, then target uTLS. A missing dialer/proxy URL returns an error.
// Pass non-nil ctx and target host:port; successful callers own closing the
// returned connection, and explicit failure paths close the proxy connection.
//
// Proxy TLS uses the supplied context; target TLS adds a ten-second handshake
// budget. CONNECT write/read installs no deadline or cancellation watcher, so
// context cancellation does not itself bound those I/O calls. The successful
// tunnel is handed directly to target handshaking rather than drained as an
// HTTP response.
func (d *HTTPProxyDialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d == nil || d.proxyURL == nil {
		return nil, errors.New("HTTP proxy dialer missing proxy URL")
	}
	slog.Debug("tls_fingerprint_http_proxy_connecting", "proxy", d.proxyURL.Host, "target", addr)

	// Step 1: TCP connect to proxy server
	var proxyAddr string
	if d.proxyURL.Port() != "" {
		proxyAddr = d.proxyURL.Host
	} else {
		// Default ports
		if d.proxyURL.Scheme == "https" {
			proxyAddr = net.JoinHostPort(d.proxyURL.Hostname(), "443")
		} else {
			proxyAddr = net.JoinHostPort(d.proxyURL.Hostname(), "80")
		}
	}

	dialer := newTLSFingerprintNetworkDialer()
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		slog.Debug("tls_fingerprint_http_proxy_connect_failed", "error", err)
		return nil, fmt.Errorf("connect to proxy: %w", err)
	}
	slog.Debug("tls_fingerprint_http_proxy_connected", "proxy_addr", proxyAddr)
	if strings.EqualFold(d.proxyURL.Scheme, "https") {
		// Authenticate the HTTPS proxy before sending CONNECT or proxy credentials
		// through it. This handshake uses ctx directly, before the target TLS budget.
		proxyTLSConfig := &stdtls.Config{ServerName: d.proxyURL.Hostname(), MinVersion: stdtls.VersionTLS12}
		if d.proxyTLSConfig != nil {
			proxyTLSConfig = d.proxyTLSConfig.Clone()
			if proxyTLSConfig.ServerName == "" {
				proxyTLSConfig.ServerName = d.proxyURL.Hostname()
			}
			if proxyTLSConfig.MinVersion == 0 {
				proxyTLSConfig.MinVersion = stdtls.VersionTLS12
			}
		}
		tlsConn := stdtls.Client(conn, proxyTLSConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			slog.Debug("tls_fingerprint_https_proxy_handshake_failed", "error", err)
			return nil, fmt.Errorf("handshake with HTTPS proxy: %w", err)
		}
		conn = tlsConn
		slog.Debug("tls_fingerprint_https_proxy_handshake_success", "proxy_addr", proxyAddr)
	}

	// CONNECT addresses the target authority directly. Put host:port in Opaque
	// and Host so request writing names the tunnel destination before target TLS begins.
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}

	// HTTP Basic proxy authentication encodes the retained userinfo as
	// username:password; send it to the proxy during CONNECT before the target
	// handshake begins.
	if d.proxyURL.User != nil {
		username := d.proxyURL.User.Username()
		password, _ := d.proxyURL.User.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+auth)
	}

	slog.Debug("tls_fingerprint_http_proxy_sending_connect", "target", addr)
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		slog.Debug("tls_fingerprint_http_proxy_write_failed", "error", err)
		return nil, fmt.Errorf("write CONNECT request: %w", err)
	}

	// Step 3: Read CONNECT response
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		slog.Debug("tls_fingerprint_http_proxy_read_response_failed", "error", err)
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	// CONNECT success transfers the connection to target TLS. Leave the HTTP
	// response body undrained so response handling cannot consume bytes from that tunnel.

	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		slog.Debug("tls_fingerprint_http_proxy_connect_failed_status", "status_code", resp.StatusCode, "status", resp.Status)
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}
	slog.Debug("tls_fingerprint_http_proxy_tunnel_established")

	// Step 4: Perform TLS handshake on the tunnel with utls fingerprint
	return performTLSHandshake(ctx, conn, d.profile, addr)
}

// DialTLSContext opens network/addr through the retained base connector, then
// applies and completes target uTLS handshaking. Pass a non-nil context and
// target host:port; custom base connectors own network cancellation and
// timeouts, and successful callers close the returned connection.
func (d *Dialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// Establish TCP connection using base dialer (supports proxy)
	slog.Debug("tls_fingerprint_dialing_tcp", "addr", addr)
	conn, err := d.baseDialer(ctx, network, addr)
	if err != nil {
		slog.Debug("tls_fingerprint_tcp_dial_failed", "error", err)
		return nil, err
	}
	slog.Debug("tls_fingerprint_tcp_connected", "addr", addr)

	// Perform TLS handshake with utls fingerprint
	return performTLSHandshake(ctx, conn, d.profile, addr)
}

// performTLSHandshake applies the profile to an established connection and
// returns the authenticated uTLS connection after success. addr normally
// contains the target host and port; failed splitting uses addr as the server
// name. Preset and handshake failures close conn, and callers own the
// returned connection.
//
// The target handshake gets a separate ten-second context budget bounded by
// the supplied non-nil parent context. Spec construction can borrow profile
// slices; pinned uTLS rewrites GREASE SupportedVersions in place, so such
// profiles need separate storage or serialized use.
func performTLSHandshake(ctx context.Context, conn net.Conn, profile *Profile, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	// If a custom target cannot be split, retain its supplied spelling as the
	// server-name input; uTLS still applies certificate verification using that value.
	if err != nil {
		host = addr
	}

	spec := buildClientHelloSpecFromProfile(profile)
	tlsConn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloCustom)

	if err := tlsConn.ApplyPreset(spec); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("apply TLS preset: %w", err)
	}

	// Custom TLS dialing bypasses http.Transport's handshake timer. Bound the
	// target handshake separately after a direct connection or proxy tunnel is ready.
	handshakeCtx, cancel := context.WithTimeout(ctx, defaultTLSFingerprintHandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}

	state := tlsConn.ConnectionState()
	slog.Debug("tls_fingerprint_handshake_success",
		"host", host,
		"version", state.Version,
		"cipher_suite", state.CipherSuite,
		"alpn", state.NegotiatedProtocol)

	return tlsConn, nil
}

// toUTLSCurves copies ordered group IDs into fresh uTLS CurveID storage.
func toUTLSCurves(curves []uint16) []utls.CurveID {
	result := make([]utls.CurveID, len(curves))
	for i, c := range curves {
		result[i] = utls.CurveID(c)
	}
	return result
}

// isGREASEValue reports whether both bytes match the reserved TLS GREASE
// pattern.
func isGREASEValue(v uint16) bool {
	return v&0x0f0f == 0x0a0a && v>>8 == v&0xff
}

// buildClientHelloSpecFromProfile selects configured nonempty lists or
// built-in defaults and constructs ordered uTLS extensions. It keeps raw
// extension order, uses generated ECH payloads, and sends unknown IDs with
// empty payloads; callers choose IDs whose payloads their peer accepts.
//
// EnableGREASE adds bookend extensions only with the built-in order. Cipher,
// ALPN, and version lists can remain borrowed, while converted curves, point
// formats, signature schemes, key shares, and PSK modes receive newly
// allocated representations. ApplyPreset rewrites GREASE version entries in
// the borrowed SupportedVersions slice, so the returned spec is not an
// independent profile copy.
func buildClientHelloSpecFromProfile(profile *Profile) *utls.ClientHelloSpec {
	// Resolve effective values (profile overrides or built-in defaults)
	cipherSuites := defaultCipherSuites
	if profile != nil && len(profile.CipherSuites) > 0 {
		cipherSuites = profile.CipherSuites
	}

	curves := defaultCurves
	if profile != nil && len(profile.Curves) > 0 {
		curves = toUTLSCurves(profile.Curves)
	}

	pointFormats := defaultPointFormats
	if profile != nil && len(profile.PointFormats) > 0 {
		pointFormats = profile.PointFormats
	}

	signatureAlgorithms := defaultSignatureAlgorithms
	if profile != nil && len(profile.SignatureAlgorithms) > 0 {
		signatureAlgorithms = make([]utls.SignatureScheme, len(profile.SignatureAlgorithms))
		for i, s := range profile.SignatureAlgorithms {
			signatureAlgorithms[i] = utls.SignatureScheme(s)
		}
	}

	alpnProtocols := []string{"http/1.1"}
	if profile != nil && len(profile.ALPNProtocols) > 0 {
		alpnProtocols = profile.ALPNProtocols
	}

	supportedVersions := []uint16{utls.VersionTLS13, utls.VersionTLS12}
	if profile != nil && len(profile.SupportedVersions) > 0 {
		supportedVersions = profile.SupportedVersions
	}

	keyShareGroups := []utls.CurveID{utls.X25519}
	if profile != nil && len(profile.KeyShareGroups) > 0 {
		keyShareGroups = toUTLSCurves(profile.KeyShareGroups)
	}

	pskModes := []uint16{uint16(utls.PskModeDHE)}
	if profile != nil && len(profile.PSKModes) > 0 {
		pskModes = profile.PSKModes
	}

	enableGREASE := profile != nil && profile.EnableGREASE

	// Build key shares
	keyShares := make([]utls.KeyShare, len(keyShareGroups))
	for i, g := range keyShareGroups {
		keyShares[i] = utls.KeyShare{Group: g}
	}

	// Determine extension order
	extOrder := defaultExtensionOrder
	if profile != nil && len(profile.Extensions) > 0 {
		extOrder = profile.Extensions
	}

	// Build extensions list from the ordered IDs.
	// Parametric extensions (curves, sigalgs, etc.) are populated with resolved profile values.
	// Unknown IDs use GenericExtension (sends type ID with empty data).
	extensions := make([]utls.TLSExtension, 0, len(extOrder)+2)
	for _, id := range extOrder {
		if isGREASEValue(id) {
			extensions = append(extensions, &utls.UtlsGREASEExtension{})
			continue
		}
		switch id {
		case 0: // server_name
			extensions = append(extensions, &utls.SNIExtension{})
		case 5: // status_request (OCSP)
			extensions = append(extensions, &utls.StatusRequestExtension{})
		case 10: // supported_groups
			extensions = append(extensions, &utls.SupportedCurvesExtension{Curves: curves})
		case 11: // ec_point_formats
			extensions = append(extensions, &utls.SupportedPointsExtension{SupportedPoints: toUint8s(pointFormats)})
		case 13: // signature_algorithms
			extensions = append(extensions, &utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: signatureAlgorithms})
		case 16: // alpn
			extensions = append(extensions, &utls.ALPNExtension{AlpnProtocols: alpnProtocols})
		case 18: // signed_certificate_timestamp
			extensions = append(extensions, &utls.SCTExtension{})
		case 23: // extended_master_secret
			extensions = append(extensions, &utls.ExtendedMasterSecretExtension{})
		case 35: // session_ticket
			extensions = append(extensions, &utls.SessionTicketExtension{})
		case 43: // supported_versions
			extensions = append(extensions, &utls.SupportedVersionsExtension{Versions: supportedVersions})
		case 45: // psk_key_exchange_modes
			extensions = append(extensions, &utls.PSKKeyExchangeModesExtension{Modes: toUint8s(pskModes)})
		case 50: // signature_algorithms_cert
			extensions = append(extensions, &utls.SignatureAlgorithmsCertExtension{SupportedSignatureAlgorithms: signatureAlgorithms})
		case 51: // key_share
			extensions = append(extensions, &utls.KeyShareExtension{KeyShares: keyShares})
		case 0xfe0d: // encrypted_client_hello (ECH, 65037)
			// ECH has structured payload bytes. Let uTLS generate its GREASE encoding
			// so a peer validating extension framing receives a formed ECH payload.
			extensions = append(extensions, &utls.GREASEEncryptedClientHelloExtension{})
		case 0xff01: // renegotiation_info
			extensions = append(extensions, &utls.RenegotiationInfoExtension{})
		default:
			// An unknown extension ID has no configured payload implementation here.
			// Send empty bytes, requiring the profile owner to select peer-accepted IDs.
			extensions = append(extensions, &utls.GenericExtension{Id: id})
		}
	}

	// A custom order already determines GREASE positions. Add bookends only to
	// the built-in order so configured extension ordering remains the profile's choice.
	if enableGREASE && (profile == nil || len(profile.Extensions) == 0) {
		extensions = append([]utls.TLSExtension{&utls.UtlsGREASEExtension{}}, extensions...)
		extensions = append(extensions, &utls.UtlsGREASEExtension{})
	}

	return &utls.ClientHelloSpec{
		CipherSuites:       cipherSuites,
		CompressionMethods: []uint8{0}, // null compression only (standard)
		Extensions:         extensions,
		TLSVersMax:         utls.VersionTLS13,
		TLSVersMin:         utls.VersionTLS10,
	}
}

// toUint8s copies values into byte storage, retaining each value's low eight
// bits. Inputs above 255 are truncated by conversion; profile consumers
// supply byte-sized point formats and PSK modes.
func toUint8s(vals []uint16) []uint8 {
	out := make([]uint8, len(vals))
	// TLS point-format and PSK-mode vectors carry one byte per entry, so
	// conversion retains the low eight bits of each configured value.
	for i, v := range vals {
		out[i] = uint8(v)
	}
	return out
}

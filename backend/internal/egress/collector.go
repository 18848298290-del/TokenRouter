package egress

import (
	"context"
	"time"
)

// Capture sessions expose a temporary URL and certificate authority so an
// administrator can direct a client handshake to the collector. Stored records
// pair the offered ClientHello lists with negotiated protocol and selected
// request headers, while session storage retains record pointers for later
// inspection.

// TLSFingerprintCollectorStatus describes listener state and the effective
// capture session configuration for administrators.
type TLSFingerprintCollectorStatus struct {
	Running              bool   `json:"running"`                 // current listener running flag
	ListenAddress        string `json:"listen_address"`          // effective TCP listen address
	PublicBaseURL        string `json:"public_base_url"`         // effective capture base URL
	UsingGeneratedCert   bool   `json:"using_generated_cert"`    // temporary certificate generation flag
	CAPEM                string `json:"ca_pem,omitempty"`        // generated authority PEM certificate
	SessionTTLSeconds    int    `json:"session_ttl_seconds"`     // seconds
	MaxRecordsPerSession int    `json:"max_records_per_session"` // retained session record limit
	// StartedAt refers to the service's retained start time when available. Status
	// callers treat this pointed-to value as immutable because the snapshot
	// retains it.
	StartedAt *time.Time `json:"started_at,omitempty"` // current listener start time
	LastError string     `json:"last_error,omitempty"` // latest retained startup/serve error
}

// TLSFingerprintCollectorSession supplies one temporary capture token,
// expiration, URL, and available certificate authority.
type TLSFingerprintCollectorSession struct {
	// Token identifies the session used by CaptureURL. CaptureSessions accepts the
	// session at ExpiresAt and removes it when the supplied clock advances beyond
	// that timestamp.
	Token      string    `json:"token"`            // temporary capture session token
	ExpiresAt  time.Time `json:"expires_at"`       // strict expiration comparison time
	CaptureURL string    `json:"capture_url"`      // administrator client capture URL
	CAPEM      string    `json:"ca_pem,omitempty"` // generated authority PEM certificate
}

// TLSFingerprintCaptureRecord contains one captured request and TLS fingerprint.
//
// Profile and RawTLSFingerprint refer to captured ClientHello lists that can
// remain shared. CaptureSessions retains each record pointer, and callers
// leave returned records and their collections unchanged after insertion.
type TLSFingerprintCaptureRecord struct {
	ID             string                 `json:"id"`              // capture nanosecond timestamp text
	CapturedAt     time.Time              `json:"captured_at"`     // wall-clock request capture time
	ClientKind     string                 `json:"client_kind"`     // heuristic client kind label
	RequestPath    string                 `json:"request_path"`    // captured request URL path
	Method         string                 `json:"method"`          // captured request HTTP method
	UserAgent      string                 `json:"user_agent"`      // captured User-Agent header value
	JA3Raw         string                 `json:"ja3_raw"`         // captured normalized JA3 text
	JA3Hash        string                 `json:"ja3_hash"`        // MD5
	NegotiatedALPN string                 `json:"negotiated_alpn"` // completed handshake ALPN selection
	HTTPProto      string                 `json:"http_proto"`      // captured HTTP protocol text
	Profile        *TLSFingerprintProfile `json:"profile"`         // captured editable profile values
	YAML           string                 `json:"yaml"`            // formatted captured profile text
	// HeadersSummary contains the selected metadata headers used for profile
	// inspection. The collector omits Authorization and Cookie from this summary
	// rather than storing the full request headers.
	HeadersSummary map[string]string `json:"headers_summary"` // selected request metadata values
	// StainlessSummary includes the first value of each x-stainless- header with a
	// nonempty value slice, including empty first values. Names are lowercased,
	// and the map is separate from the selected HeadersSummary values.
	StainlessSummary map[string]string `json:"stainless_summary"` // first Stainless header values
	// captured ordered hello values
	RawTLSFingerprint *CapturedClientHello `json:"raw_tls_fingerprint,omitempty"`
}

// CapturedClientHello records ordered TLS handshake parameters and JA3 values
// parsed for capture inspection.
type CapturedClientHello struct {
	// CipherSuites, Curves, SupportedVersions, KeyShareGroups, and Extensions
	// retain offered ordering with GREASE values normalized by the capture parser.
	// JA3Raw and JA3Hash instead omit GREASE entries from fingerprint calculation.

	CipherSuites        []uint16 // ordered offered cipher IDs
	Curves              []uint16 // ordered supported group IDs
	PointFormats        []uint16 // ordered point format IDs
	SignatureAlgorithms []uint16 // ordered signature scheme IDs
	ALPNProtocols       []string // ordered offered ALPN strings
	SupportedVersions   []uint16 // ordered offered version IDs
	KeyShareGroups      []uint16 // ordered key-share group IDs
	PSKModes            []uint16 // ordered key-exchange mode IDs
	Extensions          []uint16 // ordered offered extension IDs
	EnableGREASE        bool     // observed GREASE presence flag
	JA3Raw              string   // normalized JA3 fingerprint text
	JA3Hash             string   // MD5
}

// Collector exposes on-demand capture to the admin API; its adapter owns the listener.
type Collector interface {
	// Status returns listener state and effective session limits for admin
	// display. A retained StartedAt pointer can remain shared with the service, so
	// consumers treat that time as immutable.
	Status() TLSFingerprintCollectorStatus
	// Start opens the collector listener and returns its effective status.
	// Implementations retain listener ownership until Stop or Shutdown; repeated
	// calls while running return the existing status to administrators.
	Start(context.Context) (TLSFingerprintCollectorStatus, error)
	// Stop shuts down the current listener and clears capture sessions. The
	// context bounds waiting, and an ordinary stop permits the adapter to start a
	// fresh listener later.
	Stop(context.Context) error
	// Shutdown seals future listener startup and stops current capture work. The
	// context bounds shutdown waiting, and callers invoke it during application
	// termination before dependent network resources are closed.
	Shutdown(context.Context) error
	// CreateSession returns a temporary capture token, URL, and available
	// certificate authority. The collector needs a running listener, and the
	// returned expiration determines when later capture lookup rejects that
	// session.
	CreateSession() (*TLSFingerprintCollectorSession, error)
	// ListCaptures returns a newly allocated slice of the session's capture record
	// pointers. Records remain shared and callers treat them as immutable; missing
	// or expired sessions return an error.
	ListCaptures(string) ([]*TLSFingerprintCaptureRecord, error)
	// DeleteSession removes the identified temporary session and its retained
	// captures.
	DeleteSession(string)
}

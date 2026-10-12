package egress

import (
	"context"
	"time"
)

// TLSFingerprintCollectorStatus describes the collector's current state.
type TLSFingerprintCollectorStatus struct {
	Running              bool       `json:"running"`
	ListenAddress        string     `json:"listen_address"`
	PublicBaseURL        string     `json:"public_base_url"`
	UsingGeneratedCert   bool       `json:"using_generated_cert"`
	CAPEM                string     `json:"ca_pem,omitempty"`
	SessionTTLSeconds    int        `json:"session_ttl_seconds"`
	MaxRecordsPerSession int        `json:"max_records_per_session"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	LastError            string     `json:"last_error,omitempty"`
}

// TLSFingerprintCollectorSession identifies a short-lived capture session.
type TLSFingerprintCollectorSession struct {
	Token      string    `json:"token"`
	ExpiresAt  time.Time `json:"expires_at"`
	CaptureURL string    `json:"capture_url"`
	CAPEM      string    `json:"ca_pem,omitempty"`
}

// TLSFingerprintCaptureRecord contains one captured request and TLS fingerprint.
type TLSFingerprintCaptureRecord struct {
	ID                string                 `json:"id"`
	CapturedAt        time.Time              `json:"captured_at"`
	ClientKind        string                 `json:"client_kind"`
	RequestPath       string                 `json:"request_path"`
	Method            string                 `json:"method"`
	UserAgent         string                 `json:"user_agent"`
	JA3Raw            string                 `json:"ja3_raw"`
	JA3Hash           string                 `json:"ja3_hash"`
	NegotiatedALPN    string                 `json:"negotiated_alpn"`
	HTTPProto         string                 `json:"http_proto"`
	Profile           *TLSFingerprintProfile `json:"profile"`
	YAML              string                 `json:"yaml"`
	HeadersSummary    map[string]string      `json:"headers_summary"`
	StainlessSummary  map[string]string      `json:"stainless_summary"`
	RawTLSFingerprint *CapturedClientHello   `json:"raw_tls_fingerprint,omitempty"`
}
type CapturedClientHello struct {
	CipherSuites        []uint16
	Curves              []uint16
	PointFormats        []uint16
	SignatureAlgorithms []uint16
	ALPNProtocols       []string
	SupportedVersions   []uint16
	KeyShareGroups      []uint16
	PSKModes            []uint16
	Extensions          []uint16
	EnableGREASE        bool
	JA3Raw              string
	JA3Hash             string
}

// Collector exposes on-demand capture to the admin API; its adapter owns the listener.
type Collector interface {
	Status() TLSFingerprintCollectorStatus
	Start(context.Context) (TLSFingerprintCollectorStatus, error)
	Stop(context.Context) error
	Shutdown(context.Context) error
	CreateSession() (*TLSFingerprintCollectorSession, error)
	ListCaptures(string) ([]*TLSFingerprintCaptureRecord, error)
	DeleteSession(string)
}

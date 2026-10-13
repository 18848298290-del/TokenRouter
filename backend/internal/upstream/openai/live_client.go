package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	coderws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Live call creation posts SDP and session JSON with provider authentication
// and an attestation header, then returns the upstream SDP and call identifier.
// Redirects are disabled for this authenticated request, and bounded body
// reading checks one extra byte to reject oversized responses.
//
// The sideband connection uses the returned call identifier on a WebSocket URL
// and requires raw frame access for relay. Connections that cannot expose that
// interface are closed before an error is returned, preventing an unusable dial
// result from remaining acquired. Live identity preparation keeps session and
// thread identifiers when supplied, fills absent values, and removes the
// Responses experimental beta header before this separate protocol is sent.

// Live constants bound response capture and name the attestation header supplied
// during authenticated call creation before sideband frame relay begins.
const (
	liveUpstreamBodyLimit = 2 << 20
	LiveAttestationHeader = "x-oai-attestation"
)

// LiveCreated contains upstream SDP and identifiers for a created Live call.
type LiveCreated struct {
	SDP      []byte // session description protocol content
	CallID   string // upstream Live call identifier
	Location string // upstream call Location header
}

// LiveCreateOptions supplies credentials, attestation, and transport callbacks
// for call creation.
type LiveCreateOptions struct {
	URL             string                                             // validated call creation endpoint
	Attestation     string                                             `json:"-"` // generated Live attestation value
	Token           func(context.Context) (string, error)              // required access token resolver
	Authentication  func(context.Context, string) (http.Header, error) // required authentication header builder
	ProviderHeaders func(context.Context, http.Header) error           // required account header writer
	Routing         func(context.Context, http.Header)                 // required transport identity writer
	Do              func(*http.Request) (*http.Response, error)        // required HTTP exchange callback
	StageFailure    func(string, error)                                // required setup failure observer
	HTTPFailure     func(int, http.Header, []byte) error               // required upstream status translator
}

// LiveFrameConn exposes contextual raw frames used by Live sideband relay.
type LiveFrameConn interface {
	// ReadFrame receives one raw frame under ctx and returns its message type,
	// payload, or read error. Caller cancellation controls an outstanding frame read
	// until the concrete transport returns.
	ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error)
	// WriteFrame sends the supplied raw payload with its message type under ctx.
	// Callers serialize writes as required by the underlying connection implementation,
	// and ctx controls cancellation of the pending write operation.
	WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error
	// Close releases the sideband connection when relay ends or setup fails.
	// The concrete transport determines close-handshake timing and errors for callers
	// after they finish reading or sending frames.
	Close() error
}

// String returns a fixed label that omits Live attestation credentials.
func (LiveCreateOptions) String() string { return "openai live create options" }

// CreateLiveCall posts request SDP and session JSON with the configured
// attestation and authentication. Request and option callbacks are required,
// ctx controls the exchange, and the response body is closed after bounded
// reading and status classification.
func CreateLiveCall(ctx context.Context, request *wire.LiveCallRequest, options LiveCreateOptions) (*LiveCreated, error) {
	token, err := options.Token(ctx)
	if err != nil {
		options.StageFailure("access_token", err)
		return nil, err
	}
	body, err := json.Marshal(struct {
		SDP     string          `json:"sdp"`     // session description protocol content
		Session json.RawMessage `json:"session"` // raw Live session JSON
	}{
		SDP:     request.SDP,
		Session: request.Session,
	})
	if err != nil {
		return nil, err
	}
	reqCtx := upstream.WithHTTPUpstreamRedirectsDisabled(upstream.WithHTTPUpstreamProfile(ctx, upstream.HTTPUpstreamProfileOpenAI))
	upstreamReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, options.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	authHeaders, err := options.Authentication(ctx, token)
	if err != nil {
		options.StageFailure("authentication_headers", err)
		return nil, err
	}
	for key, values := range authHeaders {
		for _, value := range values {
			upstreamReq.Header.Add(key, value)
		}
	}
	upstreamReq.Host = "chatgpt.com"
	if err := options.ProviderHeaders(ctx, upstreamReq.Header); err != nil {
		options.StageFailure("account_headers", err)
		return nil, err
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "application/sdp")
	upstreamReq.Header.Set(LiveAttestationHeader, options.Attestation)

	options.Routing(ctx, upstreamReq.Header)
	resp, err := options.Do(upstreamReq)
	if err != nil {
		options.StageFailure("upstream_transport", err)
		return nil, err
	}
	// Release the acquired HTTP response on read, size, status, and Location
	// failures so unsuccessful Live creation cannot retain its transport connection.
	defer func() { _ = resp.Body.Close() }()
	// Read one extra byte beyond the Live body budget so oversized responses
	// are rejected instead of being returned as silently truncated SDP or error JSON.
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, liveUpstreamBodyLimit+1))
	if readErr != nil {
		return nil, readErr
	}
	if len(responseBody) > liveUpstreamBodyLimit {
		return nil, errors.New("live upstream response is too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, options.HTTPFailure(resp.StatusCode, resp.Header, responseBody)
	}
	callID, err := LiveCallIDFromLocation(resp.Header.Get("Location"))
	if err != nil {
		return nil, err
	}
	return &LiveCreated{
		SDP:      responseBody,
		CallID:   callID,
		Location: resp.Header.Get("Location"),
	}, nil
}

// LiveCallIDFromLocation returns the last path segment of a trimmed Location
// URL. Missing, unparseable, empty-path, dot, and codex-only locations return
// errors; the helper extracts an identifier rather than validating the Location
// host.
func LiveCallIDFromLocation(location string) (string, error) {
	location = strings.TrimSpace(location)
	if location == "" {
		return "", errors.New("live upstream response has no Location")
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("parse live Location: %w", err)
	}
	callID := strings.TrimSpace(path.Base(strings.TrimSuffix(parsed.Path, "/")))
	if callID == "" || callID == "." || callID == "codex" {
		return "", errors.New("live upstream Location has no call id")
	}
	return callID, nil
}

// ApplyLiveUpstreamIdentityHeaders prepares Live alpha and paired Codex
// identity headers. Headers must be writable; blank session/thread values
// receive new IDs and the Responses experimental beta header is removed after
// identity preparation.
func ApplyLiveUpstreamIdentityHeaders(headers http.Header) {
	headers.Set("OpenAI-Alpha", "quicksilver=v2")
	EnsureCodexIdentityHeaders(headers)
	EnforceCodexIdentityHeaders(headers)
	if strings.TrimSpace(headers.Get("session-id")) == "" {
		headers.Set("session-id", uuid.NewString())
	}
	if strings.TrimSpace(headers.Get("thread-id")) == "" {
		headers.Set("thread-id", uuid.NewString())
	}
	// Identity preparation sets the Responses beta header; remove it after pairing
	// because Live uses its own alpha protocol negotiation for this request.
	headers.Del("OpenAI-Beta")
}

// DialLiveSideband opens an escaped call-ID path with the supplied headers,
// proxy, and TLS profile. A successful connection must implement LiveFrameConn;
// unsupported connections are closed here, and callers own the returned raw
// connection.
func DialLiveSideband(ctx context.Context, dialer WSClientDialer, baseURL, callID string, headers http.Header, proxyURL string, profile *tlsfingerprint.Profile) (LiveFrameConn, error) {
	target := strings.TrimRight(baseURL, "/") + "/" + url.PathEscape(callID)
	conn, status, _, err := dialer.Dial(ctx, target, headers, proxyURL, profile)
	if err != nil {
		return nil, fmt.Errorf("dial live sideband (status %d): %w", status, err)
	}
	raw, ok := conn.(LiveFrameConn)
	if !ok {
		// Dial succeeded but this transport cannot relay raw Live frames. Close
		// the unusable connection before returning so callers own no hidden socket.
		_ = conn.Close()
		return nil, errors.New("live sideband transport does not support raw frames")
	}
	return raw, nil
}

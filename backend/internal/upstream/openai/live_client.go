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

const (
	liveUpstreamBodyLimit = 2 << 20
	LiveAttestationHeader = "x-oai-attestation"
)

type LiveCreated struct {
	SDP      []byte
	CallID   string
	Location string
}
type LiveCreateOptions struct {
	URL             string
	Attestation     string `json:"-"`
	Token           func(context.Context) (string, error)
	Authentication  func(context.Context, string) (http.Header, error)
	ProviderHeaders func(context.Context, http.Header) error
	Routing         func(context.Context, http.Header)
	Do              func(*http.Request) (*http.Response, error)
	StageFailure    func(string, error)
	HTTPFailure     func(int, http.Header, []byte) error
}

type LiveFrameConn interface {
	ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error)
	WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error
	Close() error
}

func (LiveCreateOptions) String() string { return "openai live create options" }

func CreateLiveCall(ctx context.Context, request *wire.LiveCallRequest, options LiveCreateOptions) (*LiveCreated, error) {
	token, err := options.Token(ctx)
	if err != nil {
		options.StageFailure("access_token", err)
		return nil, err
	}
	body, err := json.Marshal(struct {
		SDP     string          `json:"sdp"`
		Session json.RawMessage `json:"session"`
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
	defer func() { _ = resp.Body.Close() }()
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
	// Realtime and Live omit the Responses experimental header.
	headers.Del("OpenAI-Beta")
}

// DialLiveSideband dials with validated transport options and closes connections that cannot expose raw frames.
func DialLiveSideband(ctx context.Context, dialer WSClientDialer, baseURL, callID string, headers http.Header, proxyURL string, profile *tlsfingerprint.Profile) (LiveFrameConn, error) {
	target := strings.TrimRight(baseURL, "/") + "/" + url.PathEscape(callID)
	conn, status, _, err := dialer.Dial(ctx, target, headers, proxyURL, profile)
	if err != nil {
		return nil, fmt.Errorf("dial live sideband (status %d): %w", status, err)
	}
	raw, ok := conn.(LiveFrameConn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("live sideband transport does not support raw frames")
	}
	return raw, nil
}

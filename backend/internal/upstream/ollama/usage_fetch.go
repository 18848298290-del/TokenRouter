package ollama

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

const (
	SettingsURL    = "https://ollama.com/settings"
	RequestTimeout = 15 * time.Second
	MaxBodyBytes   = 512 * 1024
)

type FetchInput struct {
	ObservedAt time.Time
	Cookie     string `json:"-"`
}

type FetchOptions struct {
	Do          func(*http.Request) (*http.Response, error)
	Context     func(context.Context) context.Context
	Unavailable error
}

func (i FetchInput) String() string   { return "ollama usage input" }
func (i FetchInput) GoString() string { return i.String() }

// FetchUsage reads the Ollama settings page with the supplied session and parses usage.
func FetchUsage(ctx context.Context, input FetchInput, options FetchOptions) (*usageview.OllamaUsageObservation, error) {
	if options.Do == nil {
		return nil, options.Unavailable
	}
	now, cookie := input.ObservedAt, input.Cookie
	requestCtx, cancel := context.WithTimeout(options.Context(ctx), RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, SettingsURL, nil)
	if err != nil || !IsExactSettingsURL(req.URL) {
		return nil, options.Unavailable
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "tokenrouter-ollama-usage/1")
	resp, err := options.Do(req)
	if err != nil {
		return &usageview.OllamaUsageObservation{HTTPStatus: 0, Failure: "request_failed", RetryAfter: 0, Unauthorized: false}, nil
	}
	if resp == nil || resp.Body == nil {
		return &usageview.OllamaUsageObservation{HTTPStatus: 0, Failure: "empty_response", RetryAfter: 0, Unauthorized: false}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Request != nil && !IsExactSettingsURL(resp.Request.URL) {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "response_host_mismatch", RetryAfter: 0, Unauthorized: false}, nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "redirect_blocked", RetryAfter: RetryAfter(resp.Header, now), Unauthorized: false}, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "unauthorized", RetryAfter: RetryAfter(resp.Header, now), Unauthorized: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "http_error", RetryAfter: RetryAfter(resp.Header, now), Unauthorized: false}, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if readErr != nil {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "response_read_failed", RetryAfter: 0, Unauthorized: false}, nil
	}
	if len(body) > MaxBodyBytes {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "response_too_large", RetryAfter: 0, Unauthorized: false}, nil
	}
	data, parseErr := ParseOllamaCloudUsageHTML(body)
	if errors.Is(parseErr, ErrUnauthorizedHTML) {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "unauthorized", RetryAfter: 0, Unauthorized: true}, nil
	}
	if parseErr != nil {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "invalid_html", RetryAfter: 0, Unauthorized: false}, nil
	}

	return &usageview.OllamaUsageObservation{Data: data, HTTPStatus: resp.StatusCode}, nil
}

func IsExactSettingsURL(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == "https" && parsed.Host == "ollama.com" && parsed.Path == "/settings" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.RawPath == ""
}

// RetryAfter returns the positive delay requested by Retry-After, or zero when unavailable.
func RetryAfter(header http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if delay := at.Sub(now); delay > 0 {
			return delay
		}
	}
	return 0
}

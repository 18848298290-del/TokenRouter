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

// Ollama Cloud usage is read from a fixed HTTPS settings page using a
// browser-session Cookie. Fetching checks both the outgoing URL and any final
// response URL, rejects redirect statuses, and reads a bounded HTML body before
// passing it to the usage parser. The caller supplies the transport policy that
// prevents following redirects during the exchange.
//
// Network, HTTP, size, and parsing failures become categorized observations
// with a nil error, letting the provider service persist failure status and
// retry timing. Missing transport configuration instead returns the supplied
// unavailable error. An acquired response body stays owned until parsing
// finishes and is closed on every later return, while sign-in HTML can mark a
// successful HTTP response as unauthorized.

// These fetch limits fix the cookie-bearing target, bound one request's context
// lifetime, and cap HTML bytes before the parser examines page data.
const (
	// SettingsURL: fixed settings page URL.
	SettingsURL    = "https://ollama.com/settings"
	RequestTimeout = 15 * time.Second // derived request context budget
	MaxBodyBytes   = 512 * 1024       // maximum accepted HTML bytes
)

// FetchInput carries the observation clock and browser-session Cookie for one
// settings-page query.
type FetchInput struct {
	ObservedAt time.Time // clock for retry-date interpretation
	// Cookie is sent as the browser-session header. The provider caller
	// validates its contents before fetching, and diagnostic input
	// formatting omits the value.
	Cookie string `json:"-"`
}

// FetchOptions supplies the sender, context policy, and unavailable error used
// by the settings-page fetch.
type FetchOptions struct {
	// Do sends the fixed request using the caller's redirect policy.
	// FetchUsage returns Unavailable when this callback is nil and closes
	// each usable body supplied with a nil sending error.
	Do func(*http.Request) (*http.Response, error)
	// Context derives the transport context before the fifteen-second
	// budget is applied. Supply this callback whenever Do is configured for
	// fetching.
	Context func(context.Context) context.Context
	// Unavailable is returned when the sender or fixed request is unusable.
	// Callers provide an error that distinguishes this setup failure from a
	// persisted fetch observation.
	Unavailable error
}

// String returns a fixed input label that omits the supplied Cookie.
func (i FetchInput) String() string { return "ollama usage input" }

// GoString uses String's fixed label for Go-syntax diagnostic formatting.
func (i FetchInput) GoString() string { return i.String() }

// FetchUsage reads the fixed settings page and returns parsed data or a
// categorized failure observation. Configure Context whenever Do is usable and
// supply Unavailable to signal missing transport; callers normalize the Cookie
// and provide redirect-blocking transport policy before invoking it.
//
// A request timeout bounds the context derived by Context. Initial and supplied
// final response URLs are checked by IsExactSettingsURL, while redirect,
// unauthorized, and other unsuccessful statuses carry separate observations.
// ObservedAt supplies the clock for HTTP-date Retry-After interpretation.
//
// Bodies reached after a successful Do are closed on every later exit,
// including bounded-reading and parsing failures. Transport and content
// failures return a nonnil observation with nil error so provider refresh
// handling can persist the failure. Sign-in HTML sets Unauthorized even after
// HTTP success.
func FetchUsage(ctx context.Context, input FetchInput, options FetchOptions) (*usageview.OllamaUsageObservation, error) {
	if options.Do == nil {
		return nil, options.Unavailable
	}
	now, cookie := input.ObservedAt, input.Cookie
	requestCtx, cancel := context.WithTimeout(options.Context(ctx), RequestTimeout)
	// Release the derived timeout's resources on every exit; provider
	// refresh handling decides whether the returned error or failure
	// observation is persisted.
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, SettingsURL, nil)
	// An unusable fixed request belongs to setup failure, so return the
	// supplied unavailable error before attaching and sending the
	// browser-session Cookie.
	if err != nil || !IsExactSettingsURL(req.URL) {
		return nil, options.Unavailable
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "tokenrouter-ollama-usage/1")
	resp, err := options.Do(req)
	// Convert a sender failure into observation data so the provider
	// service can record the failed attempt and retain earlier successful
	// display data.
	if err != nil {
		return &usageview.OllamaUsageObservation{HTTPStatus: 0, Failure: "request_failed", RetryAfter: 0, Unauthorized: false}, nil
	}
	// A missing response or body has no parseable settings page. Report an
	// empty-response observation so provider refresh records this attempted
	// fetch.
	if resp == nil || resp.Body == nil {
		return &usageview.OllamaUsageObservation{HTTPStatus: 0, Failure: "empty_response", RetryAfter: 0, Unauthorized: false}, nil
	}
	// Close the received body after every later outcome so the shared
	// sender releases its response ownership, including rejected status and
	// HTML paths.
	defer func() { _ = resp.Body.Close() }()
	// A final URL outside the fixed target cannot establish settings usage
	// for the supplied session. Reject its response before interpreting any
	// HTML fields.
	if resp.Request != nil && !IsExactSettingsURL(resp.Request.URL) {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "response_host_mismatch", RetryAfter: 0, Unauthorized: false}, nil
	}
	// A redirect response does not contain the requested settings
	// observation. Keep its retry timing as failure evidence for the
	// provider's refresh schedule.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "redirect_blocked", RetryAfter: RetryAfter(resp.Header, now), Unauthorized: false}, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "unauthorized", RetryAfter: RetryAfter(resp.Header, now), Unauthorized: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "http_error", RetryAfter: RetryAfter(resp.Header, now), Unauthorized: false}, nil
	}
	// Reserve one extra byte to distinguish a page at the size limit from a
	// larger page before parsing can publish data from a truncated prefix.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if readErr != nil {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "response_read_failed", RetryAfter: 0, Unauthorized: false}, nil
	}
	if len(body) > MaxBodyBytes {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "response_too_large", RetryAfter: 0, Unauthorized: false}, nil
	}
	data, parseErr := ParseOllamaCloudUsageHTML(body)
	// A login page can arrive with a successful HTTP status. Retain the
	// parser's authentication evidence so provider refresh classifies the
	// session as unauthorized.
	if errors.Is(parseErr, ErrUnauthorizedHTML) {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "unauthorized", RetryAfter: 0, Unauthorized: true}, nil
	}
	// Unreadable or unrecognized HTML becomes failure evidence while the
	// provider service retains earlier successful usage data when
	// persisting this attempt.
	if parseErr != nil {
		return &usageview.OllamaUsageObservation{HTTPStatus: resp.StatusCode, Failure: "invalid_html", RetryAfter: 0, Unauthorized: false}, nil
	}

	return &usageview.OllamaUsageObservation{Data: data, HTTPStatus: resp.StatusCode}, nil
}

// IsExactSettingsURL reports whether parsed has the HTTPS scheme, ollama.com
// host, and settings path with no userinfo, query text, fragment, or RawPath
// override. Nil returns false; an empty query marker is accepted because
// ForceQuery is not checked.
func IsExactSettingsURL(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == "https" && parsed.Host == "ollama.com" && parsed.Path == "/settings" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.RawPath == ""
}

// RetryAfter interprets positive integer seconds or a future HTTP date relative
// to now. Missing, invalid, and past values return zero; the integer seconds
// conversion uses time.Duration arithmetic, so callers apply their retry timing
// policy to the result.
func RetryAfter(header http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		// Convert integer seconds using duration arithmetic; large
		// values can overflow.
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if delay := at.Sub(now); delay > 0 {
			return delay
		}
	}
	return 0
}

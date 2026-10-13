package egress

import (
	"strings"
	"sync"
	"time"
)

// OpenAI requests normally select HTTP/2 when enabled, while HTTP and HTTPS
// proxies can temporarily select HTTP/1 after repeated compatibility failures.
// Grok and other upstream profiles retain their separate transport mode
// choices, and an incompatible TLS ALPN profile can force OpenAI HTTP/1 before
// client acquisition.
//
// Fallback observations are keyed by the normalized proxy identity and
// coordinated within each entry's mutex. Eligible HTTP/2 failures accumulate
// within a window, reaching the threshold opens a timed fallback, and later
// HTTP/2 success resets the error window. Success leaves an active fallback
// deadline intact; deadline expiry is observed during subsequent selection.
// Stored proxy entries remain in this policy instance after their deadlines
// expire.

// HTTP/2 fallback defaults govern failure-window admission and temporary HTTP/1
// selection; transport mode values distinguish the constructed clients in pool
// identity.
const (
	defaultOpenAIHTTP2FallbackErrorThreshold = 2
	defaultOpenAIHTTP2FallbackWindow         = 60 * time.Second
	defaultOpenAIHTTP2FallbackTTL            = 10 * time.Minute
	TransportDefault                         = "default"            // ordinary default transport mode
	TransportOpenAIH1                        = "openai_h1"          // OpenAI HTTP/1 selected mode
	TransportOpenAIH2                        = "openai_h2"          // OpenAI HTTP/2 selected mode
	TransportOpenAIH1Fallback                = "openai_h1_fallback" // temporary proxy HTTP/1 mode
	TransportGrok                            = "grok"               // Grok-specific selected transport mode
)

// HTTP2Options configures upstream HTTP/2 and proxy fallback thresholds and durations.
//
// Enabled and AllowProxyFallbackToHTTP1 gate proxy fallback recording.
// FallbackErrorThreshold, FallbackWindow, and FallbackTTL use built-in
// defaults when nonpositive values reach the failure recorder.
type HTTP2Options struct {
	Enabled                   bool          // OpenAI HTTP/2 selection flag
	AllowProxyFallbackToHTTP1 bool          // HTTP-proxy fallback permission flag
	FallbackErrorThreshold    int           // compatibility failure observation threshold
	FallbackWindow            time.Duration // failure observation accumulation duration
	FallbackTTL               time.Duration // HTTP/1 fallback validity duration
}

// TransportPolicy shares proxy-keyed HTTP/2 fallback observations across
// requests using the same instance.
type TransportPolicy struct {
	// fallbacks retains one state pointer per admitted proxy key for the instance
	// lifetime. Expiry clears the stored deadline but leaves its map entry, and
	// each state serializes its observations independently.
	fallbacks sync.Map // proxy-keyed fallback state
}

// openAIHTTP2FallbackState coordinates one proxy's error window and temporary
// HTTP/1 fallback deadline.
type openAIHTTP2FallbackState struct {
	// mu protects windowStart, errorCount, and fallbackUntil. Threshold activation
	// resets the error window; a successful HTTP/2 request also resets that window
	// but leaves the fallback deadline intact.
	mu            sync.Mutex // failure observation state lock
	windowStart   time.Time  // current error window start
	errorCount    int        // eligible window failure count
	fallbackUntil time.Time  // exclusive fallback deadline timestamp
}

// TransportRequest contains transport settings for one outbound request.
//
// HasTLSProfile and TLSSupportsHTTP2 are supplied alongside TLSProfile by the
// caller. Plan uses these observations to downgrade selected OpenAI HTTP/2
// when that fingerprint cannot negotiate h2.
type TransportRequest struct {
	TLSProfile *TLSFingerprintProfile // selected egress fingerprint profile
	Headers    map[string]string      // selected provider header overrides
	// ValidateResolvedIP requests DNS-address validation; PublicHostsOnly requires
	// public addresses; DisableRedirects rejects redirect following.
	ValidateResolvedIP, PublicHostsOnly, DisableRedirects bool
	Profile                                               string // upstream transport family name
	ProxyURL                                              string // selected credential-bearing proxy URL
	ProxyKey                                              string // normalized proxy cache identity
	ProxyScheme                                           string // selected parsed proxy scheme
	// OpenAI protocol fallback configuration
	HTTP2            HTTP2Options
	HasTLSProfile    bool      // resolved fingerprint presence flag
	TLSSupportsHTTP2 bool      // resolved fingerprint ALPN eligibility
	Now              time.Time // fallback deadline observation time
}

// Resolve selects a transport mode from the upstream family, HTTP/2 options,
// proxy scheme, and current fallback state. Grok selects its dedicated mode,
// other families select the default, and disabled OpenAI HTTP/2 selects HTTP/1.
//
// Enabled OpenAI requests use HTTP/2 for non-HTTP proxy schemes. HTTP and HTTPS
// proxies additionally consult a permitted active fallback for proxyKey at now;
// an expired deadline is cleared during that lookup. The result names a
// transport mode for later construction and pool isolation.
func (p *TransportPolicy) Resolve(profile, proxyKey, proxyScheme string, options HTTP2Options, now time.Time) string {
	if profile == "grok" {
		return TransportGrok
	}
	if profile != "openai" {
		return TransportDefault
	}
	if !options.Enabled {
		return TransportOpenAIH1
	}
	scheme := strings.ToLower(proxyScheme)
	if scheme != "http" && scheme != "https" {
		return TransportOpenAIH2
	}
	// Restrict negotiated-protocol fallback to HTTP proxy paths whose options
	// permit it; direct and SOCKS selections keep their enabled HTTP/2 mode.
	if options.AllowProxyFallbackToHTTP1 && p.Active(proxyKey, now) {
		return TransportOpenAIH1Fallback
	}
	return TransportOpenAIH2
}

// Active reports whether key has a stored, usable fallback deadline later than
// now. Missing or unexpected entry values return false, and the state helper
// clears an expired deadline under its own mutex.
func (p *TransportPolicy) Active(key string, now time.Time) bool {
	raw, ok := p.fallbacks.Load(key)
	if !ok {
		return false
	}
	state, ok := raw.(*openAIHTTP2FallbackState)
	return ok && state != nil && state.isFallbackActive(now)
}

// ObserveFailure records compatibility failure for permitted OpenAI HTTP/2
// requests on HTTP proxy keys. Other observations return false with zero time;
// an eligible observation reports whether it activated fallback and any
// retained active deadline.
func (p *TransportPolicy) ObserveFailure(profile, mode, key string, compatibilityFailure bool, options HTTP2Options, now time.Time) (bool, time.Time) {
	if profile != "openai" || mode != TransportOpenAIH2 || !options.Enabled || !options.AllowProxyFallbackToHTTP1 || !httpProxyKey(key) || !compatibilityFailure {
		return false, time.Time{}
	}
	// Publish one state object per proxy key before recording, so concurrent
	// request failures contribute to the same mutex-protected threshold window.
	value, _ := p.fallbacks.LoadOrStore(key, &openAIHTTP2FallbackState{})
	state, ok := value.(*openAIHTTP2FallbackState)
	if !ok || state == nil {
		return false, time.Time{}
	}
	return state.recordFailure(now, options.FallbackErrorThreshold, options.FallbackWindow, options.FallbackTTL)
}

// ObserveSuccess clears the failure window for an OpenAI HTTP/2 request on a
// stored HTTP proxy key. It leaves any active fallback deadline intact, so
// success does not immediately reverse a timed HTTP/1 selection.
func (p *TransportPolicy) ObserveSuccess(profile, mode, key string) {
	if profile != "openai" || mode != TransportOpenAIH2 || !httpProxyKey(key) {
		return
	}
	value, ok := p.fallbacks.Load(key)
	if ok {
		if state, valid := value.(*openAIHTTP2FallbackState); valid && state != nil {
			state.resetErrorWindow()
		}
	}
}

// httpProxyKey reports whether the normalized cache key begins with an HTTP or
// HTTPS proxy scheme. Matching is case sensitive, so callers supply the
// canonical proxy identity from transport normalization.
func httpProxyKey(key string) bool {
	return strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://")
}

// isFallbackActive checks the exclusive fallback deadline while serializing
// access to the proxy state. An absent deadline returns false, a future
// deadline returns true, and a reached deadline is cleared before returning
// false.
func (s *openAIHTTP2FallbackState) isFallbackActive(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fallbackUntil.IsZero() {
		return false
	}
	if now.Before(s.fallbackUntil) {
		return true
	}
	// Remove a reached deadline before reporting inactivity, so later selection
	// cannot retain this expired timed fallback in the stored proxy state.
	s.fallbackUntil = time.Time{}
	return false
}

// resetErrorWindow clears the error count and starting timestamp under the
// proxy state lock. Existing fallbackUntil remains intact, allowing success
// observations to discard failure history while a selected timed fallback
// continues.
func (s *openAIHTTP2FallbackState) resetErrorWindow() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windowStart = time.Time{}
	s.errorCount = 0
}

// recordFailure accumulates one failure in a proxy's observation window and
// returns a newly activated or existing fallback deadline. Nonpositive
// threshold, window, and ttl select the defined defaults. All window and
// deadline decisions occur under the same state lock.
//
// An active deadline returns unchanged with activation=false. A reached
// deadline is cleared, and a window restarts when absent or older than its
// allowed duration. Reaching the failure threshold publishes now plus ttl and
// resets the accumulated window, so the next selection can use the temporary
// HTTP/1 mode.
func (s *openAIHTTP2FallbackState) recordFailure(now time.Time, threshold int, window, ttl time.Duration) (bool, time.Time) {
	if threshold <= 0 {
		threshold = defaultOpenAIHTTP2FallbackErrorThreshold
	}
	if window <= 0 {
		window = defaultOpenAIHTTP2FallbackWindow
	}
	if ttl <= 0 {
		ttl = defaultOpenAIHTTP2FallbackTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Keep an existing active deadline unchanged so repeated failed requests
	// cannot extend fallback or accumulate a second threshold during that period.
	if !s.fallbackUntil.IsZero() && now.Before(s.fallbackUntil) {
		return false, s.fallbackUntil
	}
	// A reached deadline permits new compatibility observations. Clear it before
	// checking the failure window so accumulation resumes after the prior timed fallback.
	if !s.fallbackUntil.IsZero() && !now.Before(s.fallbackUntil) {
		s.fallbackUntil = time.Time{}
	}

	// Failures at the window endpoint still accumulate. Restart older windows
	// before incrementing so separated failures cannot activate one shared threshold.
	if s.windowStart.IsZero() || now.Sub(s.windowStart) > window {
		s.windowStart = now
		s.errorCount = 0
	}
	s.errorCount++
	if s.errorCount < threshold {
		return false, time.Time{}
	}

	s.fallbackUntil = now.Add(ttl)
	s.windowStart = time.Time{}
	s.errorCount = 0
	return true, s.fallbackUntil
}

// Plan combines selected transport mode with copied request policy for client
// construction. It resolves the upstream family and proxy fallback first, then
// changes selected OpenAI HTTP/2 to HTTP/1 when the supplied TLS presence and
// ALPN observations report incompatibility.
//
// RequestPolicy copies the fingerprint and filters headers while retaining
// destination and redirect flags. The caller supplies these observations from
// the same resolved profile, then uses TransportMode in protocol options and
// cache identity so incompatible negotiations receive distinct clients.
func (p *TransportPolicy) Plan(request TransportRequest) EgressPolicy {
	mode := p.Resolve(request.Profile, request.ProxyKey, request.ProxyScheme, request.HTTP2, request.Now)
	if request.HasTLSProfile && mode == TransportOpenAIH2 && !request.TLSSupportsHTTP2 {
		mode = TransportOpenAIH1
	}
	policy := RequestPolicy(RequestPolicyInput{ProxyURL: request.ProxyURL, TLSProfile: request.TLSProfile, Headers: request.Headers, ValidateResolvedIP: request.ValidateResolvedIP, PublicHostsOnly: request.PublicHostsOnly, DisableRedirects: request.DisableRedirects})
	policy.TransportMode = mode
	return policy
}

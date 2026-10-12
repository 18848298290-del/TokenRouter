package egress

import (
	"strings"
	"sync"
	"time"
)

const (
	defaultOpenAIHTTP2FallbackErrorThreshold = 2
	defaultOpenAIHTTP2FallbackWindow         = 60 * time.Second
	defaultOpenAIHTTP2FallbackTTL            = 10 * time.Minute
	TransportDefault                         = "default"
	TransportOpenAIH1                        = "openai_h1"
	TransportOpenAIH2                        = "openai_h2"
	TransportOpenAIH1Fallback                = "openai_h1_fallback"
	TransportGrok                            = "grok"
)

// HTTP2Options configures upstream HTTP/2 and proxy fallback thresholds and durations.
type HTTP2Options struct {
	Enabled                   bool
	AllowProxyFallbackToHTTP1 bool
	FallbackErrorThreshold    int
	FallbackWindow            time.Duration
	FallbackTTL               time.Duration
}

// TransportPolicy shares HTTP/2 fallback state among callers using the same instance.
type TransportPolicy struct{ fallbacks sync.Map }

type openAIHTTP2FallbackState struct {
	mu            sync.Mutex
	windowStart   time.Time
	errorCount    int
	fallbackUntil time.Time
}

// TransportRequest contains transport settings for one outbound request.
type TransportRequest struct {
	TLSProfile                                            *TLSFingerprintProfile
	Headers                                               map[string]string
	ValidateResolvedIP, PublicHostsOnly, DisableRedirects bool
	Profile                                               string
	ProxyURL                                              string
	ProxyKey                                              string
	ProxyScheme                                           string
	HTTP2                                                 HTTP2Options
	HasTLSProfile                                         bool
	TLSSupportsHTTP2                                      bool
	Now                                                   time.Time
}

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
	if options.AllowProxyFallbackToHTTP1 && p.Active(proxyKey, now) {
		return TransportOpenAIH1Fallback
	}
	return TransportOpenAIH2
}

func (p *TransportPolicy) Active(key string, now time.Time) bool {
	raw, ok := p.fallbacks.Load(key)
	if !ok {
		return false
	}
	state, ok := raw.(*openAIHTTP2FallbackState)
	return ok && state != nil && state.isFallbackActive(now)
}

func (p *TransportPolicy) ObserveFailure(profile, mode, key string, compatibilityFailure bool, options HTTP2Options, now time.Time) (bool, time.Time) {
	if profile != "openai" || mode != TransportOpenAIH2 || !options.Enabled || !options.AllowProxyFallbackToHTTP1 || !httpProxyKey(key) || !compatibilityFailure {
		return false, time.Time{}
	}
	value, _ := p.fallbacks.LoadOrStore(key, &openAIHTTP2FallbackState{})
	state, ok := value.(*openAIHTTP2FallbackState)
	if !ok || state == nil {
		return false, time.Time{}
	}
	return state.recordFailure(now, options.FallbackErrorThreshold, options.FallbackWindow, options.FallbackTTL)
}

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

func httpProxyKey(key string) bool {
	return strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://")
}

func (s *openAIHTTP2FallbackState) isFallbackActive(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fallbackUntil.IsZero() {
		return false
	}
	if now.Before(s.fallbackUntil) {
		return true
	}
	s.fallbackUntil = time.Time{}
	return false
}

func (s *openAIHTTP2FallbackState) resetErrorWindow() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windowStart = time.Time{}
	s.errorCount = 0
}

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

	if !s.fallbackUntil.IsZero() && now.Before(s.fallbackUntil) {
		return false, s.fallbackUntil
	}
	if !s.fallbackUntil.IsZero() && !now.Before(s.fallbackUntil) {
		s.fallbackUntil = time.Time{}
	}

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

// Plan builds pool policy, selecting HTTP/1 when the TLS profile cannot support HTTP/2.
func (p *TransportPolicy) Plan(request TransportRequest) EgressPolicy {
	mode := p.Resolve(request.Profile, request.ProxyKey, request.ProxyScheme, request.HTTP2, request.Now)
	if request.HasTLSProfile && mode == TransportOpenAIH2 && !request.TLSSupportsHTTP2 {
		mode = TransportOpenAIH1
	}
	policy := RequestPolicy(RequestPolicyInput{ProxyURL: request.ProxyURL, TLSProfile: request.TLSProfile, Headers: request.Headers, ValidateResolvedIP: request.ValidateResolvedIP, PublicHostsOnly: request.PublicHostsOnly, DisableRedirects: request.DisableRedirects})
	policy.TransportMode = mode
	return policy
}

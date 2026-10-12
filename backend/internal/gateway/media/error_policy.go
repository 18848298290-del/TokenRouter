package media

import (
	"fmt"
	"time"
)

// ErrorResponse describes an error for the HTTP adapter to encode and write.
type ErrorResponse struct {
	Status        int
	Type, Message string
}
type GrokRetry struct {
	Retryable, PolicyRetryable bool
	Delay                      time.Duration
	Deadline                   time.Time
	Maximum                    int
}
type GrokFailurePorts struct {
	ContentRejection func() (bool, string)
	Generic          func() bool
	Failover         func() bool
	Retry            func() GrokRetry
	Observe          func(string, string)
	Rewrite          func() (ErrorResponse, bool)
	Write            func(ErrorResponse)
	NewFailover      func(GrokRetry, bool) error
}

// EmbeddingFailurePorts supplies classification, provider-policy, and response operations in execution order.
type EmbeddingFailurePorts struct {
	InvalidRequest func() bool
	ApplyPolicy    func()
	Generic        func() bool
	Failover       func() bool
	RecordFailover func()
	NewFailover    func() error
	Forward        func()
	Write          func(ErrorResponse)
}

// AlphaFailurePorts supplies search endpoint failover and provider-health operations.
type AlphaFailurePorts struct {
	Prepare             func()
	Failover            func() bool
	EndpointUnsupported func() bool
	ApplySideEffects    func() bool
	NewFailover         func(bool) error
}

// ImageFailurePorts supplies authorization recovery, observation, provider policy, and error conversion.
type ImageFailurePorts struct {
	Recover     func() (bool, error)
	Failover    func() bool
	Observe     func()
	ApplyPolicy func() bool
	NewFailover func() error
	Handle      func() error
}

// ImageResponseFailurePorts supplies error output rules applied after the failover decision.
type ImageResponseFailurePorts struct {
	CyberMessage    func() (string, bool)
	Observe         func(string, string)
	Write           func(ErrorResponse) error
	WrapCyber       func(error) error
	ApplyPolicy     func()
	Generic         func() bool
	Failover        func() bool
	NewFailover     func() error
	Rewrite         func() (ErrorResponse, bool)
	DefaultResponse func() error
}

// ResolveGrokFailure checks content refusal, provider policy, and failover before rewriting the final error.
// Rewrite applies display rules after provider switching ends; the caller handles health updates.
func ResolveGrokFailure(status int, message string, p GrokFailurePorts) error {
	if rejected, client := p.ContentRejection(); rejected {
		p.Observe("http_error", client)
		p.Write(ErrorResponse{Status: 403, Type: "invalid_request_error", Message: client})
		return fmt.Errorf("grok content policy rejection: %s", client)
	}
	if p.Generic() {
		p.Observe("http_error", message)
		p.Write(ErrorResponse{Status: 500, Type: "upstream_error", Message: "Upstream gateway error"})
		return fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", status, message)
	}
	if p.Failover() {
		p.Observe("failover", message)
		retry := p.Retry()
		return p.NewFailover(retry, retry.Retryable && status == 429)
	}
	p.Observe("http_error", message)
	if response, matched := p.Rewrite(); matched {
		p.Write(response)
		return fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", status, message)
	}
	typ := "upstream_error"
	switch status {
	case 400:
		typ = "invalid_request_error"
	case 404:
		typ = "not_found_error"
	case 429:
		typ = "rate_limit_error"
	}
	p.Write(ErrorResponse{Status: status, Type: typ, Message: message})
	return fmt.Errorf("upstream error: %d %s", status, message)
}

func ResolveEmbeddingFailure(status int, p EmbeddingFailurePorts) error {
	if p.InvalidRequest() {
		p.Forward()
		return fmt.Errorf("upstream invalid request: %d", status)
	}
	p.ApplyPolicy()
	if p.Generic() {
		p.Write(ErrorResponse{Status: 500, Type: "upstream_error", Message: "Upstream gateway error"})
		return fmt.Errorf("upstream error: %d (not in custom error codes)", status)
	}
	if p.Failover() {
		p.RecordFailover()
		return p.NewFailover()
	}
	p.Forward()
	return fmt.Errorf("upstream returned status %d", status)
}

func ResolveAlphaFailure(status int, p AlphaFailurePorts) error {
	if !p.Failover() && !p.EndpointUnsupported() {
		return nil
	}
	if p.Prepare != nil {
		p.Prepare()
	}
	disabled := false
	if AlphaProviderErrorSideEffects(status) {
		disabled = p.ApplySideEffects()
	}
	return p.NewFailover(disabled)
}

func ResolveImageFailure(p ImageFailurePorts) (bool, error) {
	if p.Recover != nil {
		matched, err := p.Recover()
		if matched {
			if err != nil {
				return false, fmt.Errorf("agent identity task recovery failed: %w", err)
			}
			return true, nil
		}
	}
	if p.Failover() {
		p.Observe()
		if p.ApplyPolicy() {
			return false, p.Handle()
		}
		return false, p.NewFailover()
	}
	return false, p.Handle()
}

func ResolveImageResponseFailure(status int, message string, p ImageResponseFailurePorts) error {
	if client, matched := p.CyberMessage(); matched {
		p.Observe("http_error", client)
		_ = p.Write(ErrorResponse{Status: status, Type: "invalid_request_error", Message: client})
		return p.WrapCyber(fmt.Errorf("upstream error: %d message=%s", status, client))
	}
	p.ApplyPolicy()
	if p.Generic() {
		p.Observe("http_error", message)
		return p.Write(ErrorResponse{Status: 500, Type: "upstream_error", Message: "Upstream gateway error"})
	}
	if p.Failover() {
		p.Observe("failover", message)
		return p.NewFailover()
	}
	p.Observe("http_error", message)
	if response, matched := p.Rewrite(); matched {
		return p.Write(response)
	}
	return p.DefaultResponse()
}

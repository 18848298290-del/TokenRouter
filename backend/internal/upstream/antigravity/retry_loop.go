package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	googlewire "github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

const (
	SmartRetryActionContinue      SmartRetryAction = iota // use ordinary retry handling
	SmartRetryActionBreakWithResp                         // return the response or error
	SmartRetryActionContinueURL                           // try the next endpoint
)

type RetryInput struct {
	Ctx                                     context.Context
	Prefix                                  string
	ProviderID                              int64
	ProviderName                            string
	Native, OveragesEnabled, SingleProvider bool
	AccessToken, Action                     string
	Body                                    []byte
	RequestedModel                          string
	IsStickySession                         bool
	CreditsExhausted                        func() bool
	ModelLimited                            func(context.Context, string) bool
	ModelRemaining                          func(context.Context, string) time.Duration
}

type RetryObservation struct {
	ProviderID                                            int64
	ProviderName                                          string
	UpstreamStatusCode                                    int
	UpstreamRequestID, UpstreamURL, Kind, Message, Detail string
}

type RetryOptions struct {
	BaseURL              func() string
	Do                   func(*http.Request) (*http.Response, error)
	ApplyHeaders         func(*http.Request)
	LogBody              bool
	LogMaxBytes          int
	TruncateString       func(string, int) string
	TruncateForLog       func([]byte, int) string
	SafeURL              func(string) string
	Observe              func(RetryObservation)
	SetError             func(int, string, string)
	ReadErrorBody        func(*http.Response) []byte
	ApplyErrorPolicy     func(int, http.Header, []byte) (bool, int, error)
	HandleError          func(int, http.Header, []byte)
	SetModelLimits       func(string, int, time.Time, bool) bool
	ClearSticky          func()
	CreditsModel         func(string) string
	ClearCredits         func()
	CreditsFailure       func(string, *http.Response, error)
	Internal500Exhausted func()
	ResetInternal500     func()
}

type RetryAdapter struct{ Options RetryOptions }

// AntigravityRetryLoopResult holds the response returned by the retry loop.
type AntigravityRetryLoopResult struct {
	Resp *http.Response
}

// SmartRetryAction selects the retry loop's next step.
type SmartRetryAction int

// SmartRetryResult carries a response, failure, or provider-switch request.
type SmartRetryResult struct {
	Action      SmartRetryAction
	Resp        *http.Response
	Err         error
	SwitchError *AntigravityProviderSwitchError // requests a different provider after model throttling
}

// AntigravitySmartRetryInfo contains the model and delay parsed from Google RPC errors.
type AntigravitySmartRetryInfo struct {
	RetryDelay               time.Duration // delay before another attempt
	ModelName                string        // normalized model name
	IsModelCapacityExhausted bool          // MODEL_CAPACITY_EXHAUSTED was reported
}

func (p RetryInput) String() string {
	return fmt.Sprintf("antigravity retry provider=%d", p.ProviderID)
}

// ResolveAntigravityForwardBaseURL chooses the endpoint for the requested mode and tier.
//
// An explicit daily or sandbox mode selects the second endpoint when available.
// With no mode, paid accounts use that endpoint; other accounts use the first.
//
// Production OAuth tokens can be rejected by a sandbox endpoint. Keep free accounts
// on the production endpoint by default so forwarding uses the same token audience
// as account-management calls.
// See #3611 and #2962 for endpoint and token-audience failures.
func ResolveAntigravityForwardBaseURL(mode string, paid bool) string {
	baseURLs := BaseURLs
	if len(baseURLs) == 0 {
		return ""
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if (mode == "daily" || mode == "sandbox") && len(baseURLs) > 1 {
		return baseURLs[1]
	}
	if mode == "" && paid && len(baseURLs) > 1 {
		return baseURLs[1]
	}
	return baseURLs[0]
}

// HandleSmartRetry handles credit overages, model delays, and capacity retries for 429/503.
func (s *RetryAdapter) HandleSmartRetry(p RetryInput, resp *http.Response, respBody []byte, baseURL string, urlIdx int, availableURLs []string) *SmartRetryResult {
	// A generic exhaustion 429 may permit another endpoint.
	if resp.StatusCode == http.StatusTooManyRequests && IsURLLevelRateLimit(respBody) && urlIdx < len(availableURLs)-1 {
		logger.LegacyPrintf("service.antigravity_gateway", "%s URL fallback (429): %s -> %s", p.Prefix, baseURL, availableURLs[urlIdx+1])
		return &SmartRetryResult{Action: SmartRetryActionContinueURL}
	}

	category := Antigravity429Unknown
	if resp.StatusCode == http.StatusTooManyRequests {
		category = ClassifyAntigravity429(respBody)
	}

	shouldSmartRetry, shouldRateLimitModel, waitDuration, modelName, isModelCapacityExhausted := ShouldTriggerAntigravitySmartRetry(p.Native, respBody)

	// Credit overages require an explicit free-quota exhaustion response.
	if resp.StatusCode == http.StatusTooManyRequests &&
		category == Antigravity429QuotaExhausted &&
		p.OveragesEnabled &&
		!p.CreditsExhausted() {
		result := s.AttemptCreditsOveragesRetry(p, baseURL, modelName, waitDuration, resp.StatusCode, respBody)
		if result.Handled && result.Resp != nil {
			return &SmartRetryResult{
				Action: SmartRetryActionBreakWithResp,
				Resp:   result.Resp,
			}
		}
	}

	// Long model delays ask the caller to select another provider.
	if shouldRateLimitModel {
		// A single-provider 503 waits in place so local throttling does not add another delay.
		if resp.StatusCode == http.StatusServiceUnavailable && p.SingleProvider {
			return s.HandleSingleProviderRetryInPlace(p, resp, respBody, baseURL, waitDuration, modelName)
		}

		rateLimitDuration := waitDuration
		if rateLimitDuration <= 0 {
			rateLimitDuration = AntigravityDefaultRateLimitDuration
		}
		logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d oauth_long_delay model=%s provider=%d upstream_retry_delay=%v body=%s (model rate limit, switch provider)",
			p.Prefix, resp.StatusCode, modelName, p.ProviderID, rateLimitDuration, s.Options.TruncateForLog(respBody, 200))

		resetAt := time.Now().Add(rateLimitDuration)
		if !s.Options.SetModelLimits(modelName, resp.StatusCode, resetAt, false) {
			s.Options.HandleError(resp.StatusCode, resp.Header, respBody)
			logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d rate_limited provider=%d (no model mapping)", p.Prefix, resp.StatusCode, p.ProviderID)
		}
		s.Options.ClearSticky()

		return &SmartRetryResult{
			Action: SmartRetryActionBreakWithResp,
			SwitchError: &AntigravityProviderSwitchError{
				OriginalProviderID: p.ProviderID,
				RateLimitedModel:   modelName,
				IsStickySession:    p.IsStickySession,
			},
		}
	}

	// Short delays and shared model capacity use provider-local retries.
	if shouldSmartRetry {
		var lastRetryResp *http.Response
		var lastRetryBody []byte

		// Shared capacity has its own fixed retry interval and attempt budget.
		maxAttempts := AntigravitySmartRetryMaxAttempts
		if isModelCapacityExhausted {
			maxAttempts = AntigravityModelCapacityRetryMaxAttempts
			waitDuration = AntigravityModelCapacityRetryWait

			// A cooldown from an exhausted retry sequence suppresses another sequence.
			if modelName != "" {
				modelCapacityExhaustedMu.RLock()
				cooldownUntil, exists := modelCapacityExhaustedUntil[modelName]
				modelCapacityExhaustedMu.RUnlock()
				if exists && time.Now().Before(cooldownUntil) {
					log.Printf("%s status=%d model_capacity_exhausted_dedup model=%s provider=%d cooldown_until=%v (skip retry)",
						p.Prefix, resp.StatusCode, modelName, p.ProviderID, cooldownUntil.Format("15:04:05"))
					return &SmartRetryResult{
						Action: SmartRetryActionBreakWithResp,
						Resp: &http.Response{
							StatusCode: resp.StatusCode,
							Header:     resp.Header.Clone(),
							Body:       io.NopCloser(bytes.NewReader(respBody)),
						},
					}
				}
			}
		}

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			log.Printf("%s status=%d oauth_smart_retry attempt=%d/%d delay=%v model=%s provider=%d",
				p.Prefix, resp.StatusCode, attempt, maxAttempts, waitDuration, modelName, p.ProviderID)

			timer := time.NewTimer(waitDuration)
			select {
			case <-p.Ctx.Done():
				timer.Stop()
				log.Printf("%s status=context_canceled_during_smart_retry", p.Prefix)
				return &SmartRetryResult{Action: SmartRetryActionBreakWithResp, Err: p.Ctx.Err()}
			case <-timer.C:
			}

			retryReq, err := NewAPIRequestWithURL(p.Ctx, baseURL, p.Action, p.AccessToken, p.Body)
			if err != nil {
				logger.LegacyPrintf("service.antigravity_gateway", "%s status=smart_retry_request_build_failed error=%v", p.Prefix, err)
				s.Options.HandleError(resp.StatusCode, resp.Header, respBody)
				return &SmartRetryResult{
					Action: SmartRetryActionBreakWithResp,
					Resp: &http.Response{
						StatusCode: resp.StatusCode,
						Header:     resp.Header.Clone(),
						Body:       io.NopCloser(bytes.NewReader(respBody)),
					},
				}
			}
			s.Options.ApplyHeaders(retryReq)

			retryResp, retryErr := s.Options.Do(retryReq)
			if retryErr == nil && retryResp != nil && retryResp.StatusCode != http.StatusTooManyRequests && retryResp.StatusCode != http.StatusServiceUnavailable {
				log.Printf("%s status=%d smart_retry_success attempt=%d/%d", p.Prefix, retryResp.StatusCode, attempt, maxAttempts)
				// A response other than 429/503 clears the model capacity cooldown.
				if isModelCapacityExhausted && modelName != "" {
					modelCapacityExhaustedMu.Lock()
					delete(modelCapacityExhaustedUntil, modelName)
					modelCapacityExhaustedMu.Unlock()
				}
				return &SmartRetryResult{Action: SmartRetryActionBreakWithResp, Resp: retryResp}
			}

			// Transport failures consume an attempt and allow the next retry.
			if retryErr != nil || retryResp == nil {
				log.Printf("%s status=smart_retry_network_error attempt=%d/%d error=%v", p.Prefix, attempt, maxAttempts, retryErr)
				continue
			}

			if lastRetryResp != nil {
				_ = lastRetryResp.Body.Close()
			}
			lastRetryResp = retryResp
			if retryResp != nil {
				lastRetryBody, _ = io.ReadAll(io.LimitReader(retryResp.Body, 8<<10))
				_ = retryResp.Body.Close()
			}

			// Use updated delays for rate limits; shared capacity keeps its fixed interval.
			if !isModelCapacityExhausted && attempt < maxAttempts && lastRetryBody != nil {
				newShouldRetry, _, newWaitDuration, _, _ := ShouldTriggerAntigravitySmartRetry(p.Native, lastRetryBody)
				if newShouldRetry && newWaitDuration > 0 {
					waitDuration = newWaitDuration
				}
			}
		}

		rateLimitDuration := waitDuration
		if rateLimitDuration <= 0 {
			rateLimitDuration = AntigravityDefaultRateLimitDuration
		}
		retryBody := lastRetryBody
		if retryBody == nil {
			retryBody = respBody
		}

		// Shared capacity failure remains a response from this provider.
		// Provider switching would retry the same shared capacity pool.
		if isModelCapacityExhausted {
			// Suppress subsequent capacity retry sequences until the cooldown expires.
			if modelName != "" {
				modelCapacityExhaustedMu.Lock()
				modelCapacityExhaustedUntil[modelName] = time.Now().Add(AntigravityModelCapacityCooldown)
				modelCapacityExhaustedMu.Unlock()
			}
			log.Printf("%s status=%d smart_retry_exhausted_model_capacity attempts=%d model=%s provider=%d body=%s (model capacity exhausted, not switching provider)",
				p.Prefix, resp.StatusCode, maxAttempts, modelName, p.ProviderID, s.Options.TruncateForLog(retryBody, 200))
			return &SmartRetryResult{
				Action: SmartRetryActionBreakWithResp,
				Resp: &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(retryBody)),
				},
			}
		}

		// In single-provider mode, return the 503 to the caller for final handling.
		// Leave model cooldown decisions to the outer orchestration.
		if resp.StatusCode == http.StatusServiceUnavailable && p.SingleProvider {
			logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d smart_retry_exhausted_single_provider attempts=%d model=%s provider=%d body=%s (return 503 directly)",
				p.Prefix, resp.StatusCode, AntigravitySmartRetryMaxAttempts, modelName, p.ProviderID, s.Options.TruncateForLog(retryBody, 200))
			return &SmartRetryResult{
				Action: SmartRetryActionBreakWithResp,
				Resp: &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(retryBody)),
				},
			}
		}

		log.Printf("%s status=%d smart_retry_exhausted attempts=%d model=%s provider=%d upstream_retry_delay=%v body=%s (switch provider)",
			p.Prefix, resp.StatusCode, maxAttempts, modelName, p.ProviderID, rateLimitDuration, s.Options.TruncateForLog(retryBody, 200))

		resetAt := time.Now().Add(rateLimitDuration)
		s.Options.SetModelLimits(modelName, resp.StatusCode, resetAt, true)

		// Remove affinity so the next request can select an eligible provider.
		s.Options.ClearSticky()

		return &SmartRetryResult{
			Action: SmartRetryActionBreakWithResp,
			SwitchError: &AntigravityProviderSwitchError{
				OriginalProviderID: p.ProviderID,
				RateLimitedModel:   modelName,
				IsStickySession:    p.IsStickySession,
			},
		}
	}

	return &SmartRetryResult{Action: SmartRetryActionContinue}
}

// HandleSingleProviderRetryInPlace retries a long-delay 503 on the current provider.
//
// Waiting here avoids adding a local model cooldown wait to the upstream retry delay.
// Each response can update the next delay, subject to the limits below.
//
// Retry limits:
//   - Each wait is capped by AntigravitySingleProviderSmartRetryMaxWait.
//   - Total waits are capped by AntigravitySingleProviderSmartRetryTotalMaxWait.
//   - Attempts are capped by AntigravitySingleProviderSmartRetryMaxAttempts.
func (s *RetryAdapter) HandleSingleProviderRetryInPlace(
	p RetryInput,
	resp *http.Response,
	respBody []byte,
	baseURL string,
	waitDuration time.Duration,
	modelName string,
) *SmartRetryResult {
	if waitDuration > AntigravitySingleProviderSmartRetryMaxWait {
		waitDuration = AntigravitySingleProviderSmartRetryMaxWait
	}
	if waitDuration < AntigravitySmartRetryMinWait {
		waitDuration = AntigravitySmartRetryMinWait
	}

	logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d single_provider_503_retry_in_place model=%s provider=%d upstream_retry_delay=%v (retrying in-place instead of rate-limiting)",
		p.Prefix, resp.StatusCode, modelName, p.ProviderID, waitDuration)

	var lastRetryResp *http.Response
	var lastRetryBody []byte
	totalWaited := time.Duration(0)

	for attempt := 1; attempt <= AntigravitySingleProviderSmartRetryMaxAttempts; attempt++ {
		// Shorten the final wait to fit the remaining budget.
		if totalWaited+waitDuration > AntigravitySingleProviderSmartRetryTotalMaxWait {
			remaining := AntigravitySingleProviderSmartRetryTotalMaxWait - totalWaited
			if remaining <= 0 {
				logger.LegacyPrintf("service.antigravity_gateway", "%s single_provider_503_retry: total_wait_exceeded total=%v max=%v, giving up",
					p.Prefix, totalWaited, AntigravitySingleProviderSmartRetryTotalMaxWait)
				break
			}
			waitDuration = remaining
		}

		logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d single_provider_503_retry attempt=%d/%d delay=%v total_waited=%v model=%s provider=%d",
			p.Prefix, resp.StatusCode, attempt, AntigravitySingleProviderSmartRetryMaxAttempts, waitDuration, totalWaited, modelName, p.ProviderID)

		timer := time.NewTimer(waitDuration)
		select {
		case <-p.Ctx.Done():
			timer.Stop()
			logger.LegacyPrintf("service.antigravity_gateway", "%s status=context_canceled_during_single_provider_retry", p.Prefix)
			return &SmartRetryResult{Action: SmartRetryActionBreakWithResp, Err: p.Ctx.Err()}
		case <-timer.C:
		}
		totalWaited += waitDuration

		retryReq, err := NewAPIRequestWithURL(p.Ctx, baseURL, p.Action, p.AccessToken, p.Body)
		if err != nil {
			logger.LegacyPrintf("service.antigravity_gateway", "%s single_provider_503_retry: request_build_failed error=%v", p.Prefix, err)
			break
		}
		s.Options.ApplyHeaders(retryReq)

		retryResp, retryErr := s.Options.Do(retryReq)
		if retryErr == nil && retryResp != nil && retryResp.StatusCode != http.StatusTooManyRequests && retryResp.StatusCode != http.StatusServiceUnavailable {
			logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d single_provider_503_retry_success attempt=%d/%d total_waited=%v",
				p.Prefix, retryResp.StatusCode, attempt, AntigravitySingleProviderSmartRetryMaxAttempts, totalWaited)
			if lastRetryResp != nil {
				_ = lastRetryResp.Body.Close()
			}
			return &SmartRetryResult{Action: SmartRetryActionBreakWithResp, Resp: retryResp}
		}

		// A transport failure consumes an attempt; the next attempt may still succeed.
		if retryErr != nil || retryResp == nil {
			logger.LegacyPrintf("service.antigravity_gateway", "%s single_provider_503_retry: network_error attempt=%d/%d error=%v",
				p.Prefix, attempt, AntigravitySingleProviderSmartRetryMaxAttempts, retryErr)
			continue
		}

		if lastRetryResp != nil {
			_ = lastRetryResp.Body.Close()
		}
		lastRetryResp = retryResp
		lastRetryBody, _ = io.ReadAll(io.LimitReader(retryResp.Body, 8<<10))
		_ = retryResp.Body.Close()

		// Apply the latest upstream delay within the per-wait bounds.
		if attempt < AntigravitySingleProviderSmartRetryMaxAttempts && lastRetryBody != nil {
			_, _, newWaitDuration, _, _ := ShouldTriggerAntigravitySmartRetry(p.Native, lastRetryBody)
			if newWaitDuration > 0 {
				waitDuration = max(min(newWaitDuration, AntigravitySingleProviderSmartRetryMaxWait), AntigravitySmartRetryMinWait)
			}
		}
	}

	// Return the original status and the latest available error body when retries stop.
	// The caller handles any remaining retry or failure policy.
	retryBody := lastRetryBody
	if retryBody == nil {
		retryBody = respBody
	}
	logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d single_provider_503_retry_exhausted attempts=%d total_waited=%v model=%s provider=%d body=%s (return 503 directly)",
		p.Prefix, resp.StatusCode, AntigravitySingleProviderSmartRetryMaxAttempts, totalWaited, modelName, p.ProviderID, s.Options.TruncateForLog(retryBody, 200))

	return &SmartRetryResult{
		Action: SmartRetryActionBreakWithResp,
		Resp: &http.Response{
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Body:       io.NopCloser(bytes.NewReader(retryBody)),
		},
	}
}

// AntigravityRetryLoop executes provider-local retries against the configured endpoint.
func (s *RetryAdapter) AntigravityRetryLoop(p RetryInput) (*AntigravityRetryLoopResult, error) {
	// Enabled credit overages can bypass an existing free-quota model limit.
	overagesInjected := false
	if p.RequestedModel != "" && p.Native &&
		p.OveragesEnabled && !p.CreditsExhausted() &&
		p.ModelLimited(p.Ctx, p.RequestedModel) {
		if creditsBody := InjectEnabledCreditTypes(p.Body); creditsBody != nil {
			p.Body = creditsBody
			overagesInjected = true
			logger.LegacyPrintf("service.antigravity_gateway", "%s pre_check: model_rate_limited_credits_inject model=%s provider=%d (injecting enabledCreditTypes)",
				p.Prefix, p.RequestedModel, p.ProviderID)
		}
	}

	// Existing limits request a provider switch unless credits or single-provider mode apply.
	if p.RequestedModel != "" {
		if remaining := p.ModelRemaining(p.Ctx, p.RequestedModel); remaining > 0 {
			// Credit-backed requests may proceed despite the ordinary model limit.
			if overagesInjected {
				logger.LegacyPrintf("service.antigravity_gateway", "%s pre_check: credits_injected_ignore_rate_limit remaining=%v model=%s provider=%d",
					p.Prefix, remaining.Truncate(time.Millisecond), p.RequestedModel, p.ProviderID)
			} else if p.SingleProvider {
				// Single-provider mode sends the request and handles a 503 with in-place retries.
				// Waiting for the local limit here would add another delay before that retry loop.
				logger.LegacyPrintf("service.antigravity_gateway", "%s pre_check: single_provider_retry skipping rate_limit remaining=%v model=%s provider=%d (will retry in-place if 503)",
					p.Prefix, remaining.Truncate(time.Millisecond), p.RequestedModel, p.ProviderID)
			} else {
				logger.LegacyPrintf("service.antigravity_gateway", "%s pre_check: rate_limit_switch remaining=%v model=%s provider=%d",
					p.Prefix, remaining.Truncate(time.Millisecond), p.RequestedModel, p.ProviderID)
				return nil, &AntigravityProviderSwitchError{
					OriginalProviderID: p.ProviderID,
					RateLimitedModel:   p.RequestedModel,
					IsStickySession:    p.IsStickySession,
				}
			}
		}
	}

	baseURL := s.Options.BaseURL()
	if baseURL == "" {
		return nil, errors.New("no antigravity forward base url configured")
	}
	availableURLs := []string{baseURL}

	var resp *http.Response
	var usedBaseURL string
	logBody := s.Options.LogBody
	maxBytes := 2048
	if s.Options.LogMaxBytes > 0 {
		maxBytes = s.Options.LogMaxBytes
	}
	getUpstreamDetail := func(body []byte) string {
		if !logBody {
			return ""
		}
		return s.Options.TruncateString(string(body), maxBytes)
	}

urlFallbackLoop:
	for urlIdx, baseURL := range availableURLs {
		usedBaseURL = baseURL
		allAttemptsInternal500 := true // cleared by a nonmatching retried HTTP error
		for attempt := 1; attempt <= AntigravityMaxRetries; attempt++ {
			select {
			case <-p.Ctx.Done():
				logger.LegacyPrintf("service.antigravity_gateway", "%s status=context_canceled error=%v", p.Prefix, p.Ctx.Err())
				return nil, p.Ctx.Err()
			default:
			}

			upstreamReq, err := NewAPIRequestWithURL(p.Ctx, baseURL, p.Action, p.AccessToken, p.Body)
			if err != nil {
				return nil, err
			}
			s.Options.ApplyHeaders(upstreamReq)

			resp, err = s.Options.Do(upstreamReq)
			if err == nil && resp == nil {
				err = errors.New("upstream returned nil response")
			}
			if err != nil {
				safeErr := logredact.SanitizeUpstreamQueries(err.Error())
				s.Options.Observe(RetryObservation{
					ProviderID:         p.ProviderID,
					ProviderName:       p.ProviderName,
					UpstreamStatusCode: 0,
					UpstreamURL:        s.Options.SafeURL(upstreamReq.URL.String()),
					Kind:               "request_error",
					Message:            safeErr,
				})
				if ShouldAntigravityFallbackToNextURL(err, 0) && urlIdx < len(availableURLs)-1 {
					logger.LegacyPrintf("service.antigravity_gateway", "%s URL fallback (connection error): %s -> %s", p.Prefix, baseURL, availableURLs[urlIdx+1])
					continue urlFallbackLoop
				}
				if attempt < AntigravityMaxRetries {
					logger.LegacyPrintf("service.antigravity_gateway", "%s status=request_failed retry=%d/%d error=%v", p.Prefix, attempt, AntigravityMaxRetries, err)
					if !SleepAntigravityBackoffWithContext(p.Ctx, attempt) {
						logger.LegacyPrintf("service.antigravity_gateway", "%s status=context_canceled_during_backoff", p.Prefix)
						return nil, p.Ctx.Err()
					}
					continue
				}
				logger.LegacyPrintf("service.antigravity_gateway", "%s status=request_failed retries_exhausted error=%v", p.Prefix, err)
				s.Options.SetError(0, safeErr, "")
				return nil, fmt.Errorf("upstream request failed after retries: %w", err)
			}

			if resp.StatusCode >= 400 {
				respBody := s.Options.ReadErrorBody(resp)
				_ = resp.Body.Close()

				if overagesInjected && ShouldMarkCreditsExhausted(resp, respBody, nil) {
					modelKey := s.Options.CreditsModel("")
					s.Options.CreditsFailure(modelKey, &http.Response{
						StatusCode: resp.StatusCode,
						Header:     resp.Header.Clone(),
						Body:       io.NopCloser(bytes.NewReader(respBody)),
					}, nil)
				}

				// Apply configured status and scheduling policy before platform retry handling.
				if handled, outStatus, policyErr := s.Options.ApplyErrorPolicy(resp.StatusCode, resp.Header, respBody); handled {
					if policyErr != nil {
						return nil, policyErr
					}
					resp = &http.Response{
						StatusCode: outStatus,
						Header:     resp.Header.Clone(),
						Body:       io.NopCloser(bytes.NewReader(respBody)),
					}
					break urlFallbackLoop
				}

				// Distinguish endpoint limits, model delays, and shared capacity failures.
				if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
					smartResult := s.HandleSmartRetry(p, resp, respBody, baseURL, urlIdx, availableURLs)
					switch smartResult.Action {
					case SmartRetryActionContinueURL:
						continue urlFallbackLoop
					case SmartRetryActionBreakWithResp:
						if smartResult.Err != nil {
							return nil, smartResult.Err
						}
						if smartResult.SwitchError != nil {
							return nil, smartResult.SwitchError
						}
						resp = smartResult.Resp
						break urlFallbackLoop
					}

					// Unrecognized limits use the ordinary exponential-backoff attempt budget.
					if attempt < AntigravityMaxRetries {
						upstreamMsg := strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody))
						upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
						s.Options.Observe(RetryObservation{
							ProviderID:         p.ProviderID,
							ProviderName:       p.ProviderName,
							UpstreamStatusCode: resp.StatusCode,
							UpstreamRequestID:  resp.Header.Get("x-request-id"),
							UpstreamURL:        s.Options.SafeURL(upstreamReq.URL.String()),
							Kind:               "retry",
							Message:            upstreamMsg,
							Detail:             getUpstreamDetail(respBody),
						})
						logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d retry=%d/%d body=%s", p.Prefix, resp.StatusCode, attempt, AntigravityMaxRetries, s.Options.TruncateForLog(respBody, 200))
						if !SleepAntigravityBackoffWithContext(p.Ctx, attempt) {
							logger.LegacyPrintf("service.antigravity_gateway", "%s status=context_canceled_during_backoff", p.Prefix)
							return nil, p.Ctx.Err()
						}
						continue
					}

					// Report the exhausted rate limit through the provider error handler.
					s.Options.HandleError(resp.StatusCode, resp.Header, respBody)
					logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d rate_limited base_url=%s body=%s", p.Prefix, resp.StatusCode, baseURL, s.Options.TruncateForLog(respBody, 200))
					resp = &http.Response{
						StatusCode: resp.StatusCode,
						Header:     resp.Header.Clone(),
						Body:       io.NopCloser(bytes.NewReader(respBody)),
					}
					break urlFallbackLoop
				}

				// Other retryable failures include 500, 502, 504, and 529.
				if ShouldRetryAntigravityError(resp.StatusCode) {
					if attempt < AntigravityMaxRetries {
						upstreamMsg := strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody))
						upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
						s.Options.Observe(RetryObservation{
							ProviderID:         p.ProviderID,
							ProviderName:       p.ProviderName,
							UpstreamStatusCode: resp.StatusCode,
							UpstreamRequestID:  resp.Header.Get("x-request-id"),
							UpstreamURL:        s.Options.SafeURL(upstreamReq.URL.String()),
							Kind:               "retry",
							Message:            upstreamMsg,
							Detail:             getUpstreamDetail(respBody),
						})
						logger.LegacyPrintf("service.antigravity_gateway", "%s status=%d retry=%d/%d body=%s", p.Prefix, resp.StatusCode, attempt, AntigravityMaxRetries, s.Options.TruncateForLog(respBody, 500))
						if !SleepAntigravityBackoffWithContext(p.Ctx, attempt) {
							logger.LegacyPrintf("service.antigravity_gateway", "%s status=context_canceled_during_backoff", p.Prefix)
							return nil, p.Ctx.Err()
						}
						// A different retried HTTP error breaks the INTERNAL 500 sequence.
						if !IsAntigravityInternalServerError(resp.StatusCode, respBody) {
							allAttemptsInternal500 = false
						}
						continue
					}
				}

				// Escalate the INTERNAL 500 counter when the tracked HTTP failures match.
				if allAttemptsInternal500 && IsAntigravityInternalServerError(resp.StatusCode, respBody) {
					s.Options.Internal500Exhausted()
				}

				// Rebuild the consumed body for the caller when retry handling ends.
				resp = &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(respBody)),
				}
				break urlFallbackLoop
			}

			break urlFallbackLoop
		}
	}

	if resp != nil && resp.StatusCode < 400 && usedBaseURL != "" {
		DefaultURLAvailability.MarkSuccess(usedBaseURL)
	}

	// Every successful path, including smart retries, resets the INTERNAL 500 counter.
	if resp != nil && resp.StatusCode < 400 {
		s.Options.ResetInternal500()
	}

	return &AntigravityRetryLoopResult{Resp: resp}, nil
}

// ShouldRetryAntigravityError reports statuses eligible for another attempt.
func ShouldRetryAntigravityError(statusCode int) bool {
	switch statusCode {
	case 429, 500, 502, 503, 504, 529:
		return true
	default:
		return false
	}
}

// IsURLLevelRateLimit recognizes generic exhaustion suitable for endpoint retry.
// Model-specific capacity wording is excluded from this classification.
func IsURLLevelRateLimit(body []byte) bool {
	bodyStr := string(body)
	return strings.Contains(bodyStr, "Resource has been exhausted") &&
		!strings.Contains(bodyStr, "capacity on this model")
}

// IsAntigravityConnectionError reports timeouts and network operation errors.
func IsAntigravityConnectionError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// ShouldAntigravityFallbackToNextURL permits another endpoint on connection errors or 429.
func ShouldAntigravityFallbackToNextURL(err error, statusCode int) bool {
	if IsAntigravityConnectionError(err) {
		return true
	}
	return statusCode == http.StatusTooManyRequests
}

// SleepAntigravityBackoffWithContext waits with exponential backoff and jitter.
// It returns false if cancellation interrupts the wait; attempt must be at least one.
func SleepAntigravityBackoffWithContext(ctx context.Context, attempt int) bool {
	delay := min(AntigravityRetryBaseDelay*time.Duration(1<<uint(attempt-1)), AntigravityRetryMaxDelay)

	// +/- 20% jitter
	r := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	jitter := time.Duration(float64(delay) * 0.2 * (r.Float64()*2 - 1))
	sleepFor := max(delay+jitter, 0)

	timer := time.NewTimer(sleepFor)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false
	case <-timer.C:
		return true
	}
}

// ParseAntigravitySmartRetryInfo parses model rate-limit and capacity error details.
// Invalid JSON, missing model names, and unmatched status/reason pairs return nil.
//
// Supported status/reason pairs:
// RESOURCE_EXHAUSTED with RATE_LIMIT_EXCEEDED:
//   - error.status == "RESOURCE_EXHAUSTED"
//   - error.details[].reason == "RATE_LIMIT_EXCEEDED"
//
// UNAVAILABLE with MODEL_CAPACITY_EXHAUSTED:
//   - error.status == "UNAVAILABLE"
//   - error.details[].reason == "MODEL_CAPACITY_EXHAUSTED"
//
// RetryInfo.retryDelay accepts Go duration syntax.
// Missing, invalid, or nonpositive delays use AntigravityDefaultRateLimitDuration.
func ParseAntigravitySmartRetryInfo(body []byte) *AntigravitySmartRetryInfo {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}

	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		return nil
	}

	status, _ := errObj["status"].(string)
	isResourceExhausted := status == GoogleRPCStatusResourceExhausted
	isUnavailable := status == GoogleRPCStatusUnavailable

	if !isResourceExhausted && !isUnavailable {
		return nil
	}

	details, ok := errObj["details"].([]any)
	if !ok {
		return nil
	}

	var retryDelay time.Duration
	var modelName string
	var hasRateLimitExceeded bool      // required for RESOURCE_EXHAUSTED
	var hasModelCapacityExhausted bool // required for UNAVAILABLE

	for _, d := range details {
		dm, ok := d.(map[string]any)
		if !ok {
			continue
		}

		atType, _ := dm["@type"].(string)

		if atType == GoogleRPCTypeErrorInfo {
			if meta, ok := dm["metadata"].(map[string]any); ok {
				if model, ok := meta["model"].(string); ok {
					modelName = NormalizeAntigravityModelName(model)
				}
			}
			if reason, ok := dm["reason"].(string); ok {
				if reason == GoogleRPCReasonModelCapacityExhausted {
					hasModelCapacityExhausted = true
				}
				if reason == GoogleRPCReasonRateLimitExceeded {
					hasRateLimitExceeded = true
				}
			}
			continue
		}

		if atType == GoogleRPCTypeRetryInfo {
			delay, ok := dm["retryDelay"].(string)
			if !ok || delay == "" {
				continue
			}
			// Accept Go duration syntax, including subsecond and compound values.
			dur, err := time.ParseDuration(delay)
			if err != nil {
				logger.LegacyPrintf("service.antigravity_gateway", "[Antigravity] failed to parse retryDelay: %s error=%v", delay, err)
				continue
			}
			retryDelay = dur
		}
	}

	// The status must agree with its corresponding ErrorInfo reason.
	if isResourceExhausted && !hasRateLimitExceeded {
		return nil
	}
	if isUnavailable && !hasModelCapacityExhausted {
		return nil
	}

	// A model name is required for per-model retry and cooldown decisions.
	if modelName == "" {
		return nil
	}

	// Use the default when no positive delay was parsed.
	if retryDelay <= 0 {
		retryDelay = AntigravityDefaultRateLimitDuration
	}

	return &AntigravitySmartRetryInfo{
		RetryDelay:               retryDelay,
		ModelName:                modelName,
		IsModelCapacityExhausted: hasModelCapacityExhausted,
	}
}

// ShouldTriggerAntigravitySmartRetry selects a retry policy for native requests.
// Results:
//   - shouldRetry: short model delay or shared model capacity exhaustion.
//   - shouldRateLimitModel: model delay at or above AntigravityRateLimitThreshold.
//   - waitDuration: bounded retry wait or model cooldown duration.
//   - modelName: normalized model reported by the upstream.
//   - isModelCapacityExhausted: shared capacity exhaustion was reported.
func ShouldTriggerAntigravitySmartRetry(native bool, respBody []byte) (shouldRetry bool, shouldRateLimitModel bool, waitDuration time.Duration, modelName string, isModelCapacityExhausted bool) {
	if !native {
		return false, false, 0, "", false
	}

	info := ParseAntigravitySmartRetryInfo(respBody)
	if info == nil {
		return false, false, 0, "", false
	}

	// Shared model capacity uses a fixed wait on the current provider.
	if info.IsModelCapacityExhausted {
		return true, false, AntigravityModelCapacityRetryWait, info.ModelName, true
	}

	// Long provider-model limits request a cooldown and provider switch.
	// Missing upstream delays have already received the default duration.
	if info.RetryDelay >= AntigravityRateLimitThreshold {
		return false, true, info.RetryDelay, info.ModelName, false
	}

	// Short delays still respect the minimum retry wait.
	waitDuration = max(info.RetryDelay, AntigravitySmartRetryMinWait)

	return true, false, waitDuration, info.ModelName, false
}

// NormalizeAntigravityModelName removes resource path prefixes and lowercases the model.
func NormalizeAntigravityModelName(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(normalized, "/publishers/google/models/"); idx != -1 {
		normalized = normalized[idx+len("/publishers/google/models/"):]
	} else if idx := strings.LastIndex(normalized, "/publishers/anthropic/models/"); idx != -1 {
		normalized = normalized[idx+len("/publishers/anthropic/models/"):]
	} else if idx := strings.LastIndex(normalized, "/models/"); idx != -1 {
		normalized = normalized[idx+len("/models/"):]
	} else {
		normalized = strings.TrimPrefix(normalized, "publishers/google/models/")
		normalized = strings.TrimPrefix(normalized, "publishers/anthropic/models/")
		normalized = strings.TrimPrefix(normalized, "models/")
	}
	return normalized
}

// IsAntigravityInternalServerError matches the 500 code, message, and INTERNAL status.
func IsAntigravityInternalServerError(statusCode int, body []byte) bool {
	if statusCode != http.StatusInternalServerError {
		return false
	}
	return gjson.GetBytes(body, "error.code").Int() == 500 &&
		gjson.GetBytes(body, "error.message").String() == "Internal error encountered." &&
		gjson.GetBytes(body, "error.status").String() == "INTERNAL"
}

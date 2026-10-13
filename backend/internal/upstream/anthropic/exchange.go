package anthropic

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// A provider can reject Messages history for signature or thinking-budget
// constraints before generating output. Exchange treats those HTTP 400
// responses as opportunities for selected request-body repairs, while other
// retryable statuses use the configured attempt count and elapsed budget.
//
// Signature recovery first filters thinking blocks and can then downgrade tool
// blocks when the later signature error names a tool form. Budget recovery uses
// a separate body rectifier. Successful repaired wire bytes are published
// through ReplaceBody before the live response is returned; failed recovery
// retains diagnostic response bytes for downstream handling. Responses consumed
// for a retry are closed before waiting, and the final response body belongs to
// the caller. The elapsed budget gates retries and delay, so transport timeouts
// remain the responsibility of the injected request context and sender.

// ExchangeNotice contains provider-local recovery or retry details passed to
// the synchronous observation callback for diagnostics.
type ExchangeNotice struct {
	Passthrough bool // API-key passthrough notice marker
	// Platform: selected platform; ProviderName: diagnostic provider name
	Platform, ProviderName string
	ProviderID             int64 // selected provider record ID
	UpstreamStatusCode     int   // observed upstream HTTP status
	// UpstreamRequestID: response request ID; UpstreamURL: safe target URL;
	// Kind: recovery category; Message: parsed error message; Detail: optional
	// body excerpt
	UpstreamRequestID, UpstreamURL, Kind, Message, Detail string
}

// ExchangeOptions binds request construction, dispatch, recovery rules, and
// diagnostics for one provider. Callers supply the callbacks reached by the
// chosen exchange path and retain them through response handoff.
type ExchangeOptions struct {
	// SynchronizeBody enables passthrough publication of changed wire bytes
	// through ReplaceBody before dispatch. Ordinary Exchange publishes body
	// changes on selected successful recovery paths.
	SynchronizeBody bool
	ProviderID      int64 // selected provider record ID
	// Platform: selected platform; ProviderName: diagnostic provider name
	Platform, ProviderName string
	// Stream: request context mode; DebugHeaders: successful response
	// diagnostics
	Stream, DebugHeaders bool
	// MaxAttempts bounds the outer status-retry loop. BudgetTokens and
	// BudgetMaxTokens describe configured repair values in logs; the injected
	// RectifyBudget callback performs the actual body repair and extra recovery
	// dispatches. MaxAttempts: status retry bound; BudgetTokens: logged
	// thinking budget; BudgetMaxTokens: logged output cap
	MaxAttempts, BudgetTokens, BudgetMaxTokens int
	// MaxElapsed gates extra recovery dispatches and limits status-retry
	// delays. Transport duration can extend beyond that budget, so sender and
	// context policy govern request deadlines.
	MaxElapsed time.Duration
	// Context supplies each built request's context and a release function.
	// Release runs immediately after Build and before Do, so it must leave that
	// request usable for dispatch.
	Context func(context.Context, bool) (context.Context, context.CancelFunc)
	// Build returns the HTTP request and prepared wire bytes for each dispatch.
	// A build error stops the outer attempt; selected recovery build errors
	// retain an inspectable prior response.
	Build func(context.Context, []byte) (*http.Request, []byte, error)
	// provider HTTP dispatch callback
	Do func(*http.Request) (*http.Response, error)
	// ReadErrorBody consumes diagnostic bytes from an error response. Exchange
	// decides when to close or reconstruct the consumed body; failed
	// status-retry reads still allow retry observation.
	ReadErrorBody func(*http.Response) ([]byte, error)
	// ShouldRectify decides whether an initial HTTP 400 enters signature
	// recovery. IsSignatureError checks the later thinking-filtered response
	// before a tool-related message can select the next downgrade step.
	// ShouldRectify: initial repair selection; IsSignatureError: later
	// signature recognition
	ShouldRectify, IsSignatureError func(context.Context, []byte) bool
	BudgetEnabled                   func(context.Context) bool // budget rectifier enablement callback
	// ShouldRetry selects HTTP error statuses other than 400 for the outer
	// retry loop. Transport errors pass through TransportError and return
	// before this status decision.
	ShouldRetry func(int) bool
	// FilterThinking prepares the first signature-recovery body, and
	// FilterTools prepares the later tool downgrade from the supplied attempt
	// body. Build applies normal provider preparation to each selected result.
	// FilterThinking: thinking repair transform; FilterTools: tool downgrade
	// transform
	FilterThinking, FilterTools func([]byte) []byte
	IsBudgetError               func(string) bool           // budget constraint message matcher
	RectifyBudget               func([]byte) ([]byte, bool) // thinking budget repair callback
	// ReplaceBody publishes selected successful recovery wire bytes to the
	// caller's request state. Passthrough can also invoke it before dispatch
	// when SynchronizeBody is enabled; its error aborts that path.
	ReplaceBody func([]byte) error
	// Delay supplies the wait for a status-retry attempt index. Exchange clips
	// it to the remaining elapsed budget and stops retrying when the selected
	// wait is nonpositive.
	Delay   func(int) time.Duration
	Observe func(ExchangeNotice) // synchronous exchange notice consumer
	// SafeURL: diagnostic address filter; Sanitize: transport message filter
	SafeURL, Sanitize func(string) string
	// ErrorMessage: parsed body message; Detail: optional diagnostic body text
	ErrorMessage, Detail func([]byte) string
	// transport failure classification callback
	TransportError func(context.Context, error, string) error
}

// Exchange sends provider-local attempts and returns the selected response with
// the last recorded wire body. Callers configure positive attempt limits and
// the callbacks used by their recovery paths, then read or close the returned
// response body. A transport failure closes any accompanying body and passes
// the error through TransportError.
//
// A recognized HTTP 400 signature error can trigger thinking and tool recovery;
// a budget constraint can trigger its rectifier. Successful repairs update
// ReplaceBody before response handoff. Successfully captured diagnostic bytes
// can back a reconstructed rejection; when the initial diagnostic read fails,
// the original response remains selected with its reader at the resulting
// position.
//
// Other retryable statuses close their bodies before a context-aware delay
// selected under the elapsed budget. Context release follows request building
// and precedes Do, so its callback must leave the built request usable.
func Exchange(ctx context.Context, body []byte, options ExchangeOptions) (*http.Response, []byte, error) {
	var resp *http.Response
	lastWireBody := body
	retryStart := time.Now()
	for attempt := 1; attempt <= options.MaxAttempts; attempt++ {
		// Request construction borrows the selected context until Build
		// returns. The injected release runs before Do; this call sequence
		// relies on a release that leaves the built request live.
		upstreamCtx, releaseUpstreamCtx := options.Context(ctx, options.Stream)
		upstreamReq, wireBody, err := options.Build(upstreamCtx, body)
		releaseUpstreamCtx()
		if err != nil {
			return nil, lastWireBody, err
		}

		lastWireBody = wireBody

		resp, err = options.Do(upstreamReq)
		if err != nil {
			// A sender can return both an error and response handle. Close that
			// unused body before transport-error classification returns control
			// to the outer attempt.
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			return nil, lastWireBody, options.TransportError(ctx, err, upstreamReq.URL.String())
		}

		if resp.StatusCode == http.StatusBadRequest {
			respBody, readErr := options.ReadErrorBody(resp)
			// A successful diagnostic read permits body replay and
			// rectification. A failed read leaves the original handle selected,
			// retaining its current reader position for downstream HTTP-error
			// handling.
			if readErr == nil {
				// The initial 400 body has been captured for recovery
				// decisions. Close its transport handle now; if recovery cannot
				// supply another response, these bytes back a replacement
				// reader.
				_ = resp.Body.Close()

				if options.ShouldRectify(ctx, respBody) {
					options.Observe(ExchangeNotice{
						Platform:           options.Platform,
						ProviderID:         options.ProviderID,
						ProviderName:       options.ProviderName,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-request-id"),
						UpstreamURL:        options.SafeURL(upstreamReq.URL.String()),
						Kind:               "signature_error",
						Message:            options.ErrorMessage(respBody),
						Detail:             options.Detail(respBody),
					})

					looksLikeToolSignatureError := func(msg string) bool {
						m := strings.ToLower(msg)
						return strings.Contains(m, "tool_use") ||
							strings.Contains(m, "tool_result") ||
							strings.Contains(m, "functioncall") ||
							strings.Contains(m, "function_call") ||
							strings.Contains(m, "functionresponse") ||
							strings.Contains(m, "function_response")
					}

					// Signature recovery consumes the same elapsed budget as
					// status retries. At exhaustion, return the captured
					// rejection through a fresh reader instead of starting
					// another dispatch.
					if time.Since(retryStart) >= options.MaxElapsed {
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						break
					}
					logger.LegacyPrintf("service.gateway", "[warn] Provider %d: thinking blocks have invalid signature, retrying with filtered blocks", options.ProviderID)

					// The first signature rejection selects thinking-filtered
					// history for an extra request. Normal Build preparation
					// runs again so its wire identity and headers match the
					// repaired payload.
					filteredBody := options.FilterThinking(body)
					retryCtx, releaseRetryCtx := options.Context(ctx, options.Stream)
					retryReq, retryWireBody, buildErr := options.Build(retryCtx, filteredBody)
					releaseRetryCtx()
					if buildErr == nil {
						retryResp, retryErr := options.Do(retryReq)
						if retryErr == nil {
							if retryResp.StatusCode < 400 {
								lastWireBody = retryWireBody
								// Publish the successful repaired wire body
								// before response handoff so the caller's
								// parsed state follows accepted input. A
								// publication error discards and closes that
								// successful response.
								if err := options.ReplaceBody(retryWireBody); err != nil {
									_ = retryResp.Body.Close()
									return nil, lastWireBody, err
								}
								logger.LegacyPrintf("service.gateway", "Provider %d: thinking block retry succeeded (blocks downgraded)", options.ProviderID)
								resp = retryResp
								break
							}

							// A failed thinking retry needs its captured
							// rejection for the next signature decision or
							// final response. Close the consumed handle before
							// rebuilding an inspectable body.
							retryRespBody, retryReadErr := options.ReadErrorBody(retryResp)
							_ = retryResp.Body.Close()
							if retryReadErr == nil && retryResp.StatusCode == http.StatusBadRequest && options.IsSignatureError(ctx, retryRespBody) {
								options.Observe(ExchangeNotice{
									Platform:           options.Platform,
									ProviderID:         options.ProviderID,
									ProviderName:       options.ProviderName,
									UpstreamStatusCode: retryResp.StatusCode,
									UpstreamRequestID:  retryResp.Header.Get("x-request-id"),
									UpstreamURL:        options.SafeURL(retryReq.URL.String()),
									Kind:               "signature_retry_thinking",
									Message:            options.ErrorMessage(retryRespBody),
									Detail:             options.Detail(retryRespBody),
								})
								msg2 := options.ErrorMessage(retryRespBody)
								// A repeated 400 signature error names a tool
								// form before tool downgrade is selected.
								// Rebuild from the supplied attempt body so
								// this second repair has its own input
								// transform.
								if looksLikeToolSignatureError(msg2) && time.Since(retryStart) < options.MaxElapsed {
									logger.LegacyPrintf("service.gateway", "Provider %d: signature retry still failing and looks tool-related, retrying with tool blocks downgraded", options.ProviderID)
									filteredBody2 := options.FilterTools(body)
									retryCtx2, releaseRetryCtx2 := options.Context(ctx, options.Stream)
									retryReq2, retryWireBody2, buildErr2 := options.Build(retryCtx2, filteredBody2)
									releaseRetryCtx2()
									if buildErr2 == nil {
										retryResp2, retryErr2 := options.Do(retryReq2)
										if retryErr2 == nil {
											if retryResp2.StatusCode < 400 {
												lastWireBody = retryWireBody2
												// The tool-repaired request can become the
												// selected response only after its
												// successful wire bytes reach caller
												// state. Close the response if that
												// publication step fails.
												if err := options.ReplaceBody(retryWireBody2); err != nil {
													_ = retryResp2.Body.Close()
													return nil, lastWireBody, err
												}
											}
											resp = retryResp2
											break
										}
										// A failed tool dispatch may still leave a
										// response handle. Close that unused body
										// before observing its transport error and
										// retaining the prior captured rejection.
										if retryResp2 != nil && retryResp2.Body != nil {
											_ = retryResp2.Body.Close()
										}
										options.Observe(ExchangeNotice{
											Platform:           options.Platform,
											ProviderID:         options.ProviderID,
											ProviderName:       options.ProviderName,
											UpstreamStatusCode: 0,
											UpstreamURL:        options.SafeURL(retryReq2.URL.String()),
											Kind:               "signature_retry_tools_request_error",
											Message:            options.Sanitize(retryErr2.Error()),
										})
										logger.LegacyPrintf("service.gateway", "Provider %d: tool-downgrade signature retry failed: %v", options.ProviderID, retryErr2)
									} else {
										logger.LegacyPrintf("service.gateway", "Provider %d: tool-downgrade signature retry build failed: %v", options.ProviderID, buildErr2)
									}
								}
							}

							// The thinking-retry transport body was consumed
							// and closed. Reconstruct its status, copied
							// headers, and captured bytes so final error
							// handling can still inspect that response.
							resp = &http.Response{
								StatusCode: retryResp.StatusCode,
								Header:     retryResp.Header.Clone(),
								Body:       io.NopCloser(bytes.NewReader(retryRespBody)),
							}
							break
						}
						// Thinking-retry transport failure leaves the initial
						// rejection as the selected response. Close any
						// accompanying retry handle before restoring the
						// captured initial response bytes.
						if retryResp != nil && retryResp.Body != nil {
							_ = retryResp.Body.Close()
						}
						logger.LegacyPrintf("service.gateway", "Provider %d: signature error retry failed: %v", options.ProviderID, retryErr)
					} else {
						logger.LegacyPrintf("service.gateway", "Provider %d: signature error retry build request failed: %v", options.ProviderID, buildErr)
					}

					// Recovery construction or dispatch failed after consuming
					// the initial body. Reattach its captured rejection so
					// downstream inspection can read it after the transport
					// handle was closed.
					resp.Body = io.NopCloser(bytes.NewReader(respBody))
					break
				}

				errMsg := options.ErrorMessage(respBody)
				// Budget recovery is selected from the captured constraint
				// message and current enablement setting. Its body rectifier
				// runs separately from thinking-signature history downgrade.
				if options.IsBudgetError(errMsg) && options.BudgetEnabled(ctx) {
					options.Observe(ExchangeNotice{
						Platform:           options.Platform,
						ProviderID:         options.ProviderID,
						ProviderName:       options.ProviderName,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-request-id"),
						UpstreamURL:        options.SafeURL(upstreamReq.URL.String()),
						Kind:               "budget_constraint_error",
						Message:            errMsg,
						Detail:             options.Detail(respBody),
					})

					rectifiedBody, applied := options.RectifyBudget(body)
					// A rectifier reports whether it prepared a changed body.
					// Dispatch that result only while elapsed budget remains,
					// letting an unchanged or expired case keep the captured
					// rejection.
					if applied && time.Since(retryStart) < options.MaxElapsed {
						logger.LegacyPrintf("service.gateway", "Provider %d: detected budget_tokens constraint error, retrying with rectified budget (budget_tokens=%d, max_tokens=%d)", options.ProviderID, options.BudgetTokens, options.BudgetMaxTokens)
						budgetRetryCtx, releaseBudgetRetryCtx := options.Context(ctx, options.Stream)
						budgetRetryReq, budgetWireBody, buildErr := options.Build(budgetRetryCtx, rectifiedBody)
						releaseBudgetRetryCtx()
						if buildErr == nil {
							budgetRetryResp, retryErr := options.Do(budgetRetryReq)
							if retryErr == nil {
								if budgetRetryResp.StatusCode < 400 {
									lastWireBody = budgetWireBody
									// A successful budget repair updates caller
									// request state before response handoff. If
									// that update fails, close the live
									// repaired response and return the
									// publication error.
									if err := options.ReplaceBody(budgetWireBody); err != nil {
										_ = budgetRetryResp.Body.Close()
										return nil, lastWireBody, err
									}
								}
								resp = budgetRetryResp
								break
							}
							// Budget-retry transport failure retains the
							// initial rejection for final handling. Close any
							// unused retry response before the initial captured
							// bytes are made readable again.
							if budgetRetryResp != nil && budgetRetryResp.Body != nil {
								_ = budgetRetryResp.Body.Close()
							}
							logger.LegacyPrintf("service.gateway", "Provider %d: budget rectifier retry failed: %v", options.ProviderID, retryErr)
						} else {
							logger.LegacyPrintf("service.gateway", "Provider %d: budget rectifier retry build failed: %v", options.ProviderID, buildErr)
						}
					}
				}

				// The ordinary 400 path consumed its body while checking
				// recoverable constraints. Restore those captured bytes for
				// downstream error handling when no replacement response was
				// selected.
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
			}
		}

		// Other configured HTTP failures use the outer attempt budget. Preserve
		// the final response when attempts or elapsed time are exhausted so the
		// caller can apply provider policy.
		if resp.StatusCode >= 400 && resp.StatusCode != http.StatusBadRequest && options.ShouldRetry(resp.StatusCode) {
			if attempt < options.MaxAttempts {
				elapsed := time.Since(retryStart)
				if elapsed >= options.MaxElapsed {
					break
				}

				delay := options.Delay(attempt)
				remaining := options.MaxElapsed - elapsed
				// Clip the selected wait to the remaining budget observed
				// before diagnostic reads. This limits scheduled backoff while
				// a later dispatch retains its own transport policy.
				if delay > remaining {
					delay = remaining
				}
				if delay <= 0 {
					break
				}

				// Status retry diagnostics are best effort. Capture available
				// bytes and close the discarded response before observing the
				// failure and waiting for another provider-local dispatch.
				respBody, _ := options.ReadErrorBody(resp)
				_ = resp.Body.Close()
				options.Observe(ExchangeNotice{
					Platform:           options.Platform,
					ProviderID:         options.ProviderID,
					ProviderName:       options.ProviderName,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					UpstreamURL:        options.SafeURL(upstreamReq.URL.String()),
					Kind:               "retry",
					Message:            options.ErrorMessage(respBody),
					Detail:             options.Detail(respBody),
				})
				logger.LegacyPrintf("service.gateway", "Provider %d: upstream error %d, retry %d/%d after %v (elapsed=%v/%v)",
					options.ProviderID, resp.StatusCode, attempt, options.MaxAttempts, delay, elapsed, options.MaxElapsed)
				// The backoff waits on the caller context, allowing
				// cancellation to stop the next dispatch after the discarded
				// response has already been released.
				if err := upstream.WaitContext(ctx, delay); err != nil {
					return nil, lastWireBody, err
				}
				continue
			}

			break
		}

		if resp.StatusCode < 400 && options.DebugHeaders {
			logger.LegacyPrintf("service.gateway", "[DEBUG] Gemini API Response Headers for provider %d:", options.ProviderID)
			for k, v := range resp.Header {
				logger.LegacyPrintf("service.gateway", "[DEBUG]   %s: %v", k, v)
			}
		}
		break
	}

	return resp, lastWireBody, nil
}

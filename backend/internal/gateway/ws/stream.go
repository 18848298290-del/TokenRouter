package ws

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// RelayTurn runs one pooled-connection turn, collecting output, usage, first-token timing, and failures.
func RelayTurn(ctx context.Context, p StreamPort, lease StreamLease, input ClientPayload, turn int, o StreamOptions, hooks *StreamHooks) (*ForwardResult, error) {
	payload, payloadBytes, originalModel, routingModel := input.PayloadRaw, input.PayloadBytes, input.OriginalModel, input.RoutingModel
	imageBillingModel, imageSizeTier, imageInputSize := input.ImageBillingModel, input.ImageSizeTier, input.ImageInputSize
	requestedReasoningEffort := input.RequestedReasoningEffort
	p.BeginObservation()
	var modelObserver protocol.ResponseModelObserver
	if lease == nil {
		return nil, errors.New("upstream websocket lease is nil")
	}
	turnStart := time.Now()
	wroteDownstream := false
	if err := lease.WriteRequest(ctx, payload, o.WriteTimeout); err != nil {
		return nil, WrapIngressTurnError(
			"write_upstream",
			fmt.Errorf("write upstream websocket request: %w", err),
			false,
		)
	}
	if o.Debug {
		p.Debug(fmt.Sprintf(
			"ingress_ws_turn_request_sent provider_id=%d turn=%d conn_id=%s payload_bytes=%d",
			o.ProviderID,
			turn,
			p.Truncate(lease.ConnID(), 64),
			payloadBytes,
		))
	}

	responseID := ""
	usage := wire.ForwardUsage{}
	imageCounter := p.ImageCounter()
	var firstTokenMs *int
	reqStream := p.Streaming(payload)
	turnPreviousResponseID := payloadString(payload, "previous_response_id")
	turnPreviousResponseIDKind := p.ClassifyPrevious(turnPreviousResponseID)
	turnPromptCacheKey := payloadString(payload, "prompt_cache_key")
	turnStoreDisabled := p.StoreDisabled(payload)
	turnHasFunctionCallOutput := p.HasToolOutput(payload)
	eventCount := 0
	tokenEventCount := 0
	terminalEventCount := 0
	replayCollector := p.ReplayCollector()
	firstEventType := ""
	lastEventType := ""
	needModelReplace := false
	clientDisconnected := false
	mappedModel := ""
	var mappedModelBytes []byte
	if routingModel != "" {
		mappedModel = p.MappedModel(routingModel)
		needModelReplace = mappedModel != "" && mappedModel != originalModel
		if needModelReplace {
			mappedModelBytes = []byte(mappedModel)
		}
	}
	for {
		upstreamMessage, readErr := lease.ReadEvent(ctx, o.ReadTimeout)
		if readErr != nil {
			lease.MarkBroken()
			return nil, WrapIngressTurnError(
				"read_upstream",
				fmt.Errorf("read upstream websocket event: %w", readErr),
				wroteDownstream,
			)
		}
		if normalized, changed := p.NormalizeCompleted(upstreamMessage); changed {
			upstreamMessage = normalized
		}

		eventType, eventResponseID, _ := p.Envelope(upstreamMessage)
		p.ObserveModel(upstreamMessage, eventType)
		modelObserver.ObserveOpenAI(upstreamMessage, eventType)
		if responseID == "" && eventResponseID != "" {
			responseID = eventResponseID
		}
		if eventType != "" {
			eventCount++
			if firstEventType == "" {
				firstEventType = eventType
			}
			lastEventType = eventType
		}
		// Capture usage and risk evidence before early error returns so AfterTurn can process them.
		// @project-doc docs/domains/content_moderation.md#upstream_cyber_policy
		if p.ShouldParseUsage(eventType) {
			p.ParseUsage(upstreamMessage, &usage)
		}
		if eventType == "error" || eventType == "response.failed" {
			p.MarkCyber(upstreamMessage, &usage)
		}
		if eventType == "error" {
			canonicalModel := p.SchedulingModel(routingModel)
			errorDecision := p.ErrorDecision(ctx, canonicalModel, lease.Headers(), upstreamMessage)
			errCodeRaw, errTypeRaw, errMsgRaw := p.ErrorFields(upstreamMessage)
			errorStatus := p.ErrorStatus(upstreamMessage)
			fallbackReason, _ := p.ClassifyError(errCodeRaw, errTypeRaw, errMsgRaw)
			if fallbackReason == "invalid_encrypted_content" {
				// Remember rejected ciphertext digests and forward the error; later turns strip those digests before sending.
				if digests := p.EncryptedDigests(payload); len(digests) > 0 {
					p.MarkEncrypted(digests)
					p.Log(fmt.Sprintf(
						"ingress_ws_invalid_encrypted_lineage_mark provider_id=%d turn=%d digests=%d",
						o.ProviderID,
						turn,
						len(digests),
					))
				}
			}
			errCode, errType, errMessage := p.SummarizeError(errCodeRaw, errTypeRaw, errMsgRaw)
			recoverablePrevNotFound := fallbackReason == IngressStagePreviousResponseNotFound &&
				turnPreviousResponseID != "" &&
				!turnHasFunctionCallOutput &&
				o.PreviousRecovery &&
				!wroteDownstream
			if recoverablePrevNotFound {
				// Avoid error in the recoverable log label because LegacyPrintf uses it to select ERROR severity.
				p.Log(fmt.Sprintf(
					"ingress_ws_prev_response_recoverable provider_id=%d turn=%d conn_id=%s idx=%d reason=%s code=%s type=%s message=%s previous_response_id=%s previous_response_id_kind=%s response_id=%s store_disabled=%v has_prompt_cache_key=%v",
					o.ProviderID,
					turn,
					p.Truncate(lease.ConnID(), 64),
					eventCount,
					p.Truncate(fallbackReason, 160),
					errCode,
					errType,
					errMessage,
					p.Truncate(turnPreviousResponseID, 64),
					p.NormalizeLog(turnPreviousResponseIDKind),
					p.Truncate(responseID, 64),
					turnStoreDisabled,
					turnPromptCacheKey != "",
				))
			} else {
				p.Log(fmt.Sprintf(
					"ingress_ws_error_event provider_id=%d turn=%d conn_id=%s idx=%d fallback_reason=%s err_code=%s err_type=%s err_message=%s previous_response_id=%s previous_response_id_kind=%s response_id=%s store_disabled=%v has_prompt_cache_key=%v",
					o.ProviderID,
					turn,
					p.Truncate(lease.ConnID(), 64),
					eventCount,
					p.Truncate(fallbackReason, 160),
					errCode,
					errType,
					errMessage,
					p.Truncate(turnPreviousResponseID, 64),
					p.NormalizeLog(turnPreviousResponseIDKind),
					p.Truncate(responseID, 64),
					turnStoreDisabled,
					turnPromptCacheKey != "",
				))
			}
			// Suppress a recoverable previous_response_not_found event while ingress
			// removes previous_response_id and replays the current turn once.
			if recoverablePrevNotFound {
				lease.MarkBroken()
				errMsg := strings.TrimSpace(errMsgRaw)
				if errMsg == "" {
					errMsg = "previous response not found"
				}
				return nil, WrapIngressTurnError(
					IngressStagePreviousResponseNotFound,
					errors.New(errMsg),
					false,
				)
			}
			if turn == 1 && !wroteDownstream && errorDecision.Generic {
				lease.MarkBroken()
				return nil, p.GenericError(errorStatus)
			}
			if turn == 1 && !wroteDownstream && errorStatus != 0 && errorDecision.Failover {
				lease.MarkBroken()
				return nil, p.RawFailure(errorStatus, lease.Headers(), upstreamMessage, errorDecision.RetrySame)
			}
		}
		if warning := p.Warning(eventType, upstreamMessage); warning != nil && hooks != nil && hooks.OnUpstreamError != nil {
			hooks.OnUpstreamError(turn, originalModel, warning.StatusCode, warning.ResponseBody, warning.Message)
		}
		isTokenEvent := p.IsToken(eventType)
		if isTokenEvent {
			tokenEventCount++
		}
		isTerminalEvent := p.IsTerminal(eventType)
		if isTerminalEvent {
			terminalEventCount++
		}
		if firstTokenMs == nil && isTokenEvent {
			ms := int(time.Since(turnStart).Milliseconds())
			firstTokenMs = &ms
		}
		imageCounter.AddSSEData(upstreamMessage)
		terminalPolicy := TerminalPolicy{
			TerminalEvent: p.NormalizeTerminal(eventType),
			Decision:      ErrorPolicy{},
		}
		if isTerminalEvent {
			canonicalModel := p.SchedulingModel(routingModel)
			terminalPolicy = p.TerminalDecision(ctx, canonicalModel, lease.Headers(), upstreamMessage)
		}
		if eventType == "response.failed" {
			if turn == 1 && !wroteDownstream && terminalPolicy.Decision.Generic {
				lease.MarkBroken()
				return nil, p.GenericError(terminalPolicy.StatusCode)
			}
			if turn == 1 && !wroteDownstream && terminalPolicy.Decision.Failover {
				lease.MarkBroken()
				return nil, p.Failure(
					terminalPolicy.StatusCode,
					lease.Headers(),
					upstreamMessage,
					p.Message(upstreamMessage),
					terminalPolicy.Decision.RetrySame,
				)
			}
			if terminalPolicy.Decision.Generic {
				// After leading events are delivered, finish with a generic WS error.
				upstreamMessage = p.GenericEvent()
			}
		}

		if !clientDisconnected {
			if needModelReplace && len(mappedModelBytes) > 0 && p.MayContainModel(eventType) && bytes.Contains(upstreamMessage, mappedModelBytes) {
				upstreamMessage = p.ReplaceModel(upstreamMessage, mappedModel, originalModel)
			}
			if p.MayContainTools(eventType) && p.LikelyTools(upstreamMessage) {
				if corrected, changed := p.CorrectTools(upstreamMessage); changed {
					upstreamMessage = corrected
				}
			}
			replayCollector.AddEvent(eventType, upstreamMessage)
			// Rewrite capacity codes in the client copy so Codex can back off and retry.
			// HTTP/SSE and the HTTP bridge use the same rewriting for server_is_overloaded and slow_down.
			// Health classification retains the unmodified capacity codes in upstreamMessage.
			clientMessage := upstreamMessage
			if eventType == "error" || eventType == "response.failed" {
				if rewritten, changed := p.CapacityShed(clientMessage); changed {
					clientMessage = rewritten
				}
			}
			if err := p.WriteClient(clientMessage); err != nil {
				if p.IsDisconnect(err) {
					clientDisconnected = true
					closeStatus, closeReason := p.SummarizeClose(err)
					p.Log(fmt.Sprintf(
						"ingress_ws_client_disconnected_drain provider_id=%d turn=%d conn_id=%s close_status=%s close_reason=%s",
						o.ProviderID,
						turn,
						p.Truncate(lease.ConnID(), 64),
						closeStatus,
						p.Truncate(closeReason, 120),
					))
				} else {
					return nil, WrapIngressTurnError(
						"write_client",
						fmt.Errorf("write client websocket event: %w", err),
						wroteDownstream,
					)
				}
			} else {
				wroteDownstream = true
			}
		}
		if isTerminalEvent {
			// Mark a disconnected client's lease broken so the pool discards the connection.
			if clientDisconnected {
				lease.MarkBroken()
			}
			firstTokenMsValue := -1
			if firstTokenMs != nil {
				firstTokenMsValue = *firstTokenMs
			}
			if o.Debug {
				p.Debug(fmt.Sprintf(
					"ingress_ws_turn_completed provider_id=%d turn=%d conn_id=%s response_id=%s duration_ms=%d events=%d token_events=%d terminal_events=%d first_event=%s last_event=%s first_token_ms=%d client_disconnected=%v",
					o.ProviderID,
					turn,
					p.Truncate(lease.ConnID(), 64),
					p.Truncate(responseID, 64),
					time.Since(turnStart).Milliseconds(),
					eventCount,
					tokenEventCount,
					terminalEventCount,
					p.Truncate(firstEventType, 160),
					p.Truncate(lastEventType, 160),
					firstTokenMsValue,
					clientDisconnected,
				))
			}
			imageCount := imageCounter.Count()
			result := &ForwardResult{
				RequestID:                   responseID,
				Usage:                       usage,
				Model:                       originalModel,
				UpstreamModel:               mappedModel,
				UpstreamResponseServiceTier: p.ResponseTier(),
				UpstreamResponseModel:       modelObserver.Model(),
				ServiceTier:                 p.ResolvedTier(payload),
				ReasoningEffort:             p.Reasoning(payload, mappedModel, originalModel),
				RequestedReasoningEffort:    requestedReasoningEffort,
				Stream:                      reqStream,
				OpenAIWSMode:                true,
				UpstreamTerminalEvent:       terminalPolicy.TerminalEvent,
				ResponseHeaders:             lease.Headers(),
				Duration:                    time.Since(turnStart),
				FirstTokenMs:                firstTokenMs,
			}
			if replayInput := replayCollector.Items(); len(replayInput) > 0 {
				result.WSReplayInput = replayInput
				result.WSReplayInputExists = true
			}
			if imageCount > 0 {
				result.ImageCount = imageCount
				result.ImageSize = imageSizeTier
				result.ImageInputSize = imageInputSize
				result.ImageOutputSizes = imageCounter.Sizes()
				result.BillingModel = imageBillingModel
			}
			return result, nil
		}
	}
}
